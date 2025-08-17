package mono

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
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
	return limiter.Apply
}

func RpsLimitGlobal(quota int64, handler429 ...HandlerFunc) MiddlewareFunc {
	limiter := &RpsLimiterGlobal{
		Quota:      quota,
		Timeout:    time.Second,
		Handler429: def(handler429, defaultHandler429),
	}
	return limiter.Apply
}

type RpsLimiterGlobal struct {
	Quota      int64
	Timeout    time.Duration
	Handler429 HandlerFunc
	Cleans     chan time.Time
	checkedEnv bool
	state      atomic.Int64
	cleaning   atomic.Bool
}

func (limiter *RpsLimiterGlobal) Apply(handler HandlerFunc) HandlerFunc {
	if limiter.Timeout == 0 {
		limiter.Timeout = time.Second
	}
	if limiter.Handler429 == nil {
		limiter.Handler429 = defaultHandler429
	}
	if limiter.Cleans == nil {
		limiter.Cleans = make(chan time.Time, limiter.Quota)
	}
	if !tryQuotaFromEnv(EnvMonoRps, &limiter.checkedEnv, &limiter.Quota) {
		return handler
	}

	if limiter.cleaning.CompareAndSwap(false, true) {
		go func() {
			for clean := range limiter.Cleans {
				if diff := clean.Sub(time.Now()); diff > 0 {
					time.Sleep(diff)
				}
				limiter.state.Add(-1)
			}
		}()
	}

	return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		defer func() { limiter.Cleans <- time.Now().Add(limiter.Timeout) }()
		if limiter.state.Add(1) > limiter.Quota {
			return limiter.Handler429(ctx, rw, req)
		}
		return handler(ctx, rw, req)
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
	if !tryQuotaFromEnv(EnvMonoRpsClients, &limit.checkedEnv, &limit.Quota) {
		return handler
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

	limit.cleans = append(
		limit.cleans[cleaned+1:],
		limitClean{
			RemoteAddr: addr,
			After:      current.Add(limit.Timeout),
		},
	)
	return limit.limits[addr]
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

func tryQuotaFromEnv(env string, checked *bool, quota *int64) (ok bool) {
	if *quota > 0 {
		return true
	}
	if *checked {
		return false
	}
	*checked = true

	val, ok := os.LookupEnv(env)
	if !ok {
		return false
	}
	parsed, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return false
	}
	*quota = parsed
	return *quota > 0
}
