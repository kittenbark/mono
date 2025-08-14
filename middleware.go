package mono

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type MiddlewareFunc = func(handler HandlerFunc) HandlerFunc

func SaneHeaders(handler HandlerFunc) HandlerFunc {
	return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		h := rw.Header()

		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")

		if IsLocal() || IsDev() {
			h.Set("Cache-Control", "no-cache, no-store, must-revalidate")
			h.Set("Pragma", "no-cache")
			h.Set("Expires", "0")
		} else {
			// NOTE: this blocks <script src="https://cdn.tailwind.com"/>
			h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'")
			h.Set("Cache-Control", "public, max-age=86400")
			h.Set("Expires", time.Now().Add(24*time.Hour).Format(http.TimeFormat))
		}

		return handler(ctx, rw, req)
	}
}

type RpsLimiter struct {
	Quota   int64
	Timeout time.Duration
	mutex   sync.Mutex
	limits  map[string]int64
}

func (limit *RpsLimiter) Apply(handler HandlerFunc) HandlerFunc {
	if limit.limits == nil {
		limit.limits = map[string]int64{}
	}
	if limit.Quota == 0 {
		limit.Quota = RpsLimiterDefaultQuota.Load()
	}
	if limit.Timeout == 0 {
		limit.Timeout = time.Second
	}

	return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		if limit.rate(req.RemoteAddr) > limit.Quota {
			return responseError(rw, http.StatusTooManyRequests)
		}
		return handler(ctx, rw, req)
	}
}

func (limit *RpsLimiter) rate(addr string) int64 {
	limit.mutex.Lock()
	defer limit.mutex.Unlock()

	limit.limits[addr] += 1
	time.AfterFunc(limit.Timeout, func() {
		limit.mutex.Lock()
		defer limit.mutex.Unlock()
		limit.limits[addr] -= 1
		if limit.limits[addr] == 0 {
			delete(limit.limits, addr)
		}
	})
	return limit.limits[addr]
}

var rpsLimiter = sync.OnceValue(func() *RpsLimiter { return &RpsLimiter{} })

func RpsLimit(quota int64) MiddlewareFunc {
	rpsLimiter().Quota = quota
	return func(handler HandlerFunc) HandlerFunc {
		limiter := rpsLimiter()
		return limiter.Apply(handler)
	}
}

func interpretPanicsAsError(handler HandlerFunc) HandlerFunc {
	return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = errors.Join(err, fmt.Errorf("panic: %v", r))
			}
		}()

		return errors.Join(handler(ctx, rw, req), ctx.Err())
	}
}
