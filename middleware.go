package mono

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
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

// RpsLimitClients shows 429 for each client, which len(requests) > quota in the last second.
// Use RpsLimiterClients if you need a different timeout from 1s.
func RpsLimitClients(quota int64, handler429 ...HandlerFunc) MiddlewareFunc {
	limiter := &RpsLimiterClients{
		Quota:      quota,
		Timeout:    time.Second,
		Handler429: def(handler429, defaultHandler429),
	}
	return func(handler HandlerFunc) HandlerFunc {
		return limiter.Apply(handler)
	}
}

type RpsLimiterClients struct {
	Quota      int64
	Timeout    time.Duration
	Handler429 HandlerFunc
	mutex      sync.Mutex
	limits     map[string]int64
	checkedEnv bool
	cleans     []limitClean
}

func (limit *RpsLimiterClients) Apply(handler HandlerFunc) HandlerFunc {
	if limit.Quota <= 0 {
		if limit.checkedEnv {
			return handler
		}
		if !limit.tryQuotaFromEnv() {
			return handler
		}
	}
	if limit.limits == nil {
		limit.limits = map[string]int64{}
	}
	if limit.Timeout == 0 {
		limit.Timeout = time.Second
	}
	if limit.Handler429 == nil {
		limit.Handler429 = defaultHandler429
	}

	return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		if limit.rate(req.RemoteAddr) > limit.Quota {
			return limit.Handler429(ctx, rw, req)
		}
		return handler(ctx, rw, req)
	}
}

func (limit *RpsLimiterClients) rate(addr string) int64 {
	limit.mutex.Lock()
	defer limit.mutex.Unlock()

	limit.limits[addr] += 1
	current := time.Now()
	cleaned := -1
	for i, clean := range limit.cleans {
		if clean.After.After(current) {
			break
		}
		cleaned = i
		limit.limits[clean.RemoteAddr] -= 1
		if limit.limits[clean.RemoteAddr] == 0 {
			delete(limit.limits, clean.RemoteAddr)
		}
	}
	if cleaned >= 0 {
		limit.cleans = limit.cleans[cleaned+1:]
	}

	limit.cleans = append(limit.cleans, limitClean{
		RemoteAddr: addr,
		After:      current.Add(limit.Timeout),
	})
	return limit.limits[addr]
}

func (limit *RpsLimiterClients) tryQuotaFromEnv() (ok bool) {
	if limit.checkedEnv {
		return false
	}
	limit.checkedEnv = true
	monoRpsClientsEnv, ok := os.LookupEnv(EnvMonoRpsClients)
	if !ok {
		return false
	}
	monoRpsClients, err := strconv.ParseInt(monoRpsClientsEnv, 10, 64)
	if err != nil {
		return false
	}
	limit.Quota = monoRpsClients
	if limit.Quota <= 0 {
		return false
	}
	return true
}

type limitClean struct {
	After      time.Time
	RemoteAddr string
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

func defaultHandler429(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
	return responseError(rw, http.StatusTooManyRequests)
}
