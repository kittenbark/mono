package mono

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

type SimpleAuth struct {
	Filename       string
	Prefixes       []string
	OnUnauthorized HandlerFunc

	users       map[string]string
	logins      map[string]string
	usersMutex  sync.RWMutex
	loginsMutex sync.RWMutex
}

func (auth *SimpleAuth) Middleware() MiddlewareFunc {
	return func(handler HandlerFunc) HandlerFunc {
		return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
			if auth.requestSuits(req) && !auth.isAuthed(req) {
				return auth.OnUnauthorized(ctx, rw, req)
			}
			return handler(ctx, rw, req)
		}
	}
}

func (auth *SimpleAuth) HandleLogin() HandlerFunc {
	return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		type Request struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		var parsed Request
		if err := json.NewDecoder(req.Body).Decode(&parsed); err != nil {
			auth.usersMutex.Unlock()
			return err
		}
		parsed.Password = auth.hashed(strings.TrimSpace(parsed.Password))

		auth.usersMutex.RLock()
		pw, ok := auth.users[parsed.Username]
		auth.usersMutex.RUnlock()
		if !ok || pw != parsed.Password {
			http.NotFound(rw, req)
			return nil
		}

		return nil
	}
}

func (auth *SimpleAuth) requestSuits(req *http.Request) bool {
	if len(auth.Prefixes) == 0 {
		return true
	}
	url := strings.ToLower(req.URL.Path)
	for _, prefix := range auth.Prefixes {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

func (auth *SimpleAuth) hashed(password string) string {
	result := sha256.New()
	result.Write([]byte(password))
	return hex.EncodeToString(result.Sum(nil))[:16]

}

func (auth *SimpleAuth) isAuthed(req *http.Request) bool {
	for _, cookie := range req.CookiesNamed("mono_auth") {
		data, _ := json.Marshal(cookie)
		slog.Info("auth#cookie", "val", string(data))
		type Data struct {
			Username  string `json:"username"`
			AuthToken string `json:"auth_token"`
		}
		var parsed Data
		if err := json.Unmarshal(data, &parsed); err != nil {
			slog.Warn("auth#parse_cookie_err", "val", string(data), "err", err)
			return false
		}
		auth.loginsMutex.RLock()
		_, ok := auth.logins[parsed.Username]
		auth.loginsMutex.RUnlock()
		if ok {
			return true
		}
	}
	return false
}

func Auth(prefix string, authed func(req *http.Request) bool, unauthorized ...HandlerFunc) MiddlewareFunc {
	unauthorizedFn := def(unauthorized, func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		http.Error(rw, "403 unauthorized", http.StatusUnauthorized)
		return nil
	})
	prefix = strings.ToLower(prefix)
	return func(handler HandlerFunc) HandlerFunc {
		return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
			if strings.HasPrefix(strings.ToLower(req.URL.Path), prefix) && authed(req) {
				return unauthorizedFn(ctx, rw, req)
			}
			return handler(ctx, rw, req)
		}
	}
}
