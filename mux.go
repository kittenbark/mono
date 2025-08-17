package mono

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"golang.org/x/crypto/acme/autocert"
	"log"
	"net/http"
	"net/http/httputil"
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
	Proxy(source, destination string) Server
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

func (server *serverDev) Proxy(source, destination string) Server {
	dest, err := url.Parse(destination)
	if err != nil {
		return server.error(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(dest)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, source)
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
	}
	return server.Handler(source, func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		req.URL.Path = strings.TrimPrefix(req.URL.Path, source)
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		proxy.ServeHTTP(rw, req)
		return nil
	})
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
			Log.Error("handle error", "err", err.Error())
			_ = responseError(rw, http.StatusInternalServerError)
			return
		}
	}
	server.handlersMap[pattern] = "dynamic"

	return server
}

func (server *serverDev) Static(pattern string, static Static) Server {
	page, err := static.Apply(&Context{Url: pattern})
	if err != nil {
		return server.error(err)
	}

	for subpattern, subdata := range page.Subpattern {
		patternJoined, err := url.JoinPath(pattern, subpattern)
		if err != nil {
			return server.error(err)
		}
		server.Static(patternJoined, subdata)
	}
	if len(page.Data) == 0 {
		return server
	}

	// Note: this section might be CPU intensive, could be a good place for parallelization.
	var gzipData []byte
	if strings.HasPrefix(page.ContentType, "text/") {
		gzipResult := bytes.NewBuffer(nil)
		gzipCompressor, _ := gzip.NewWriterLevel(gzipResult, gzip.BestCompression)
		if _, err := gzipCompressor.Write(page.Data); err != nil {
			server.buildError = errors.Join(server.buildError, err)
		}
		if err := gzipCompressor.Close(); err != nil {
			server.buildError = errors.Join(server.buildError, err)
		}
		gzipData = gzipResult.Bytes()
	}
	defer func() {
		// Note: this is a hack — server.Handler sets handlers[pattern]=dynamic, we override it as static.
		if gzipData == nil {
			server.handlersMap[pattern] = fmt.Sprintf("static [%s] (%s)", sizeof(page.Data), page.ContentType)
		} else {
			server.handlersMap[pattern] = fmt.Sprintf("static [%s (%s)] (%s)", sizeof(gzipData), sizeof(page.Data), page.ContentType)
		}
	}()

	return server.Handler(pattern, func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		h := rw.Header()
		if page.ContentType != "" {
			h.Set("Content-Type", page.ContentType)
		}
		if strings.HasPrefix(page.ContentType, "text/css") {
			h.Set("Cache-Control", "public, max-age=604800")
			h.Set("Expires", time.Now().Add(7*24*time.Hour).Format(http.TimeFormat))
		}
		data := page.Data
		if gzipData != nil && strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") {
			data = gzipData
			h.Set("Content-Encoding", "gzip")
		}
		if _, err := rw.Write(data); err != nil {
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
		stats = append(stats, fmt.Sprintf("%s%s -> %s", server.hostname(), pattern, type_))
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
	if !enableTLS() {
		Log.Debug("mono.TLS: dev build, skipping tls")
		return server
	}

	if err != nil {
		data, ok := err.(*cursedTLSDataAsError)
		Log.Debug("mono.TLS: setting server.cert", "cfg", cfg != nil, "cert", ok)
		if !ok {
			return server.error(err)
		}
		server.cert = data.manager
	}
	server.tls = cfg
	return server
}

func (server *serverDev) Start() (err error) {
	defer func() {
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}()

	if server.buildError != nil {
		return server.buildError
	}

	if server.cert != nil {
		server.addr = ":443"
		go func() {
			Log.Debug("mono.Start: have cert, proxying 80 to 443")
			if err := http.ListenAndServe(":80", server.cert.HTTPHandler(nil)); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("HTTP server error: %v", err)
			}
		}()
	}

	server.robotsTxt()
	mux := http.NewServeMux()
	for pattern, handler := range server.handlers {
		mux.Handle(pattern, handler)
	}
	server.internal = http.Server{
		Addr:      server.addr,
		Handler:   mux,
		TLSConfig: server.tls,
	}

	Log.Info(fmt.Sprintf(
		"Built in %s. Starting server at %s",
		time.Since(server.buildStart).String(),
		server.hostname(),
	))
	if server.tls != nil {
		Log.Debug("mono.Start: tls != nil => ListenAndServeTLS")
		return server.internal.ListenAndServeTLS("", "")
	}
	Log.Debug("mono.Start: tls == nil => ListenAndServe")
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

func (server *serverDev) hostname() string {
	if server.tls == nil {
		return fmt.Sprintf("http://localhost%s", server.addr)
	}
	return "https://" + server.tls.ServerName
}

func (server *serverDev) robotsTxt() {
	if _, ok := server.handlersMap["/robots.txt"]; ok {
		return
	}

	const schema = `User-agent: *
Allow: /
Disallow: /mono/cdn/*`
	server.Static("/robots.txt", StaticPage{Data: []byte(schema), ContentType: "text/plain"})
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
