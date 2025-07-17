package mono

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"golang.org/x/crypto/acme/autocert"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

type Server interface {
	Static(pattern string, static Static) Server
	Handler(pattern string, fn HandlerFunc) Server
	Middleware(fn MiddlewareFunc) Server
	Stats() Server
	Addr(addr string) Server
	TLS(cfg *tls.Config, err error) Server
	Start() error
	Stop()
}

var _ Server = (*serverDev)(nil)

type HandlerFunc func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error

func Mux() Server {
	result := &serverDev{}
	result.init()
	return result.Middleware(SaneHeaders)
}

type serverDev struct {
	addr         string
	ctx          context.Context
	ctxCancel    func()
	ctxTimeout   time.Duration
	internal     http.Server
	tls          *tls.Config
	cert         *autocert.Manager
	middleware   []MiddlewareFunc
	buildError   error
	buildStart   time.Time
	handlersLock sync.RWMutex
	handlersMap  map[string]string
	handlers     map[string]http.HandlerFunc
}

func (server *serverDev) Handler(pattern string, fn HandlerFunc) Server {
	for _, middleware := range server.middleware {
		fn = middleware(fn)
	}

	server.handlersLock.Lock()
	defer server.handlersLock.Unlock()
	server.handlers[pattern] = func(rw http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(server.ctx, server.ctxTimeout)
		defer cancel()

		if err := fn(ctx, rw, req); err != nil {
			slog.Error("handle error", "err", err.Error())
			_ = responseError(rw, http.StatusInternalServerError)
			return
		}
	}
	server.handlersMap[pattern] = "dynamic"

	return server
}

func (server *serverDev) Static(pattern string, static Static) Server {
	data, err := static.Apply(&Context{Url: pattern})
	if err != nil {
		return server.error(err)
	}

	for subpattern, subdata := range data.Subpattern {
		patternJoined, err := url.JoinPath(pattern, subpattern)
		if err != nil {
			return server.error(err)
		}
		_ = server.Handler(patternJoined, func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
			if subdata.ContentType != "" {
				rw.Header().Set("Content-Type", subdata.ContentType)
			}
			if _, err = rw.Write(subdata.Data); err != nil {
				return err
			}
			return nil
		})
		server.handlersMap[patternJoined] = fmt.Sprintf("static [%s] (%s)", sizeof(subdata.Data), subdata.ContentType)
	}

	if len(data.Data) == 0 {
		return server
	}

	defer func() {
		server.handlersMap[pattern] = fmt.Sprintf("static [%s] (%s)", sizeof(data.Data), data.ContentType)
	}()

	return server.Handler(pattern, func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		h := rw.Header()
		if data.ContentType != "" {
			h.Set("Content-Type", data.ContentType)
		}
		if strings.HasPrefix(data.ContentType, "text/css") {
			h.Set("Cache-Control", "public, max-age=604800")
			h.Set("Expires", time.Now().Add(7*24*time.Hour).Format(http.TimeFormat))
		}
		if _, err = rw.Write(data.Data); err != nil {
			return err
		}
		return nil
	})
}

func (server *serverDev) Addr(addr string) Server {
	server.addr = addr
	return server
}

func (server *serverDev) Middleware(fn MiddlewareFunc) Server {
	server.middleware = append(server.middleware, fn)
	return server
}

func (server *serverDev) Stats() Server {
	stats := []string{}
	for pattern, type_ := range server.handlersMap {
		stats = append(stats, fmt.Sprintf("http://localhost%s%s -> %s", server.addr, pattern, type_))
	}
	slices.SortStableFunc(stats, func(a, b string) int {
		if len(a) == len(b) {
			return strings.Compare(a, b)
		}
		return cmp.Compare(len(a), len(b))
	})
	println(strings.Join(stats, "\n"))
	return server
}

func (server *serverDev) TLS(cfg *tls.Config, err error) Server {
	if IsDev() {
		slog.Info("dev build, skipping tls")
		return server
	}

	if err != nil {
		data, ok := err.(*cursedTLSDataAsError)
		if !ok {
			return server.error(err)
		}
		server.cert = data.manager
	}
	server.tls = cfg
	return server
}

func (server *serverDev) Start() error {
	if server.buildError != nil {
		return server.buildError
	}

	mux := http.NewServeMux()
	for pattern, handler := range server.handlers {
		mux.Handle(pattern, handler)
	}
	server.internal = http.Server{
		Addr:      server.addr,
		Handler:   mux,
		TLSConfig: server.tls,
	}

	if server.cert != nil {
		server.addr = ":443"
		go func() {
			if err := http.ListenAndServe(":80", server.cert.HTTPHandler(nil)); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("HTTP server error: %v", err)
			}
		}()
	}

	slog.Info(fmt.Sprintf(
		"Built in %s. Starting server at %s%s",
		func() string {
			if server.tls == nil {
				return "http://localhost"
			}
			return "https://" + server.tls.ServerName
		}(),
		time.Since(server.buildStart).String(),
		server.addr,
	))
	if server.tls != nil {
		return server.internal.ListenAndServeTLS("", "")
	}
	return server.internal.ListenAndServe()
}

func (server *serverDev) Stop() {
	server.ctxCancel()
	_ = server.internal.Shutdown(server.ctx)
}

func (server *serverDev) error(err error) Server {
	if err != nil {
		server.buildError = errors.Join(server.buildError, buildError(err, 1))
	}
	return server
}

func (server *serverDev) init() {
	if server.addr == "" {
		server.addr = ":3000"
	}
	if server.ctxTimeout == 0 {
		server.ctxTimeout = time.Second * 10
	}
	if len(server.middleware) == 0 {
		server.middleware = []MiddlewareFunc{interpretPanicsAsError}
	}
	server.ctx, server.ctxCancel = context.WithCancel(context.Background())
	server.buildStart = time.Now()
	server.handlersMap = make(map[string]string)
	server.handlers = make(map[string]http.HandlerFunc)
}

var sizeofSuffix = []string{"b", "kb", "mb", "gb", "tb", "pb"}

func sizeof(data []byte) string {
	result := float64(len(data))
	i := 0
	for result > 1024 {
		result /= 1024
		i += 1
	}
	return fmt.Sprintf("%0.2f%s", result, sizeofSuffix[i])
}

func alt[T comparable](value T, otherwise T) T {
	var defaultValue T
	if value == defaultValue {
		return otherwise
	}
	return value
}

func unwrap[T any](value []T, otherwise T) T {
	if len(value) == 0 {
		return otherwise
	}
	return value[0]
}

type BuildError struct {
	Caller     string
	CallerFrom string
	Err        error
}

var _ error = BuildError{}

func (err BuildError) Error() string {
	result := []string{}
	if err.Err != nil {
		result = append(result, fmt.Sprintf("Err=%v", err.Err))
	}
	if err.Caller != "" {
		result = append(result, fmt.Sprintf("Func=%v", err.Caller))
	}
	if err.CallerFrom != "" {
		result = append(result, fmt.Sprintf("At [%s]", err.CallerFrom))
	}
	return fmt.Sprintf("mono.BuildError: %s", strings.Join(result, ","))
}

func buildError(err error, depth ...int) error {
	_, caller, _, _ := runtime.Caller(unwrap(depth, 0) + 1)
	_, callerFrom, _, _ := runtime.Caller(unwrap(depth, 0) + 2)
	return BuildError{
		Caller:     caller,
		CallerFrom: callerFrom,
		Err:        err,
	}
}
