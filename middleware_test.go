package mono_test

import (
	"context"
	"fmt"
	"github.com/kittenbark/mono"
	"math/rand"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type Stats struct {
	RemoteAddr string
	Ok         atomic.Int64
	Bad        atomic.Int64
}

func TestRpsLimitClients(t *testing.T) {
	t.Parallel()

	p95 := func(duration time.Duration, freq time.Duration) int64 {
		return int64(float64(duration) / float64(freq) * 0.95)
	}

	requests := func(t *testing.T, duration time.Duration, tick time.Duration, addr string, req mono.HandlerFunc) {
		ticker := time.NewTicker(tick)
		time.AfterFunc(duration, ticker.Stop)
		for ; ; <-ticker.C {
			if err := req(t.Context(), nil, &http.Request{RemoteAddr: addr}); err != nil {
				t.Error(err)
			}
		}
	}

	t.Run("[default] good and bad", func(t *testing.T) {
		t.Parallel()

		good := &Stats{
			RemoteAddr: "127.0.0.1:777",
		}
		bad := &Stats{
			RemoteAddr: "127.0.0.1:666",
		}

		limiter := mono.RpsLimitClients(10, func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
			switch req.RemoteAddr {
			case "127.0.0.1:777":
				good.Bad.Add(1)
			case "127.0.0.1:666":
				bad.Bad.Add(1)
			default:
				t.Error("bad remote address")
			}
			return nil
		})

		req := limiter(func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
			switch req.RemoteAddr {
			case "127.0.0.1:777":
				good.Ok.Add(1)
			case "127.0.0.1:666":
				bad.Ok.Add(1)
			default:
				t.Error("bad remote address")
			}
			return nil
		})

		duration := time.Second * 3
		goodTick := time.Millisecond * 101
		badTick := time.Millisecond * 50

		go requests(t, duration, goodTick, "127.0.0.1:777", req)
		go requests(t, duration, badTick, "127.0.0.1:666", req)
		time.Sleep(duration)

		if good.Ok.Load() < p95(duration, goodTick) || good.Bad.Load() > 0 {
			t.Errorf(
				"good was timeouted (ok=%d {p95(duration, goodTick)=%d}, bad=%d)",
				good.Ok.Load(),
				p95(duration, goodTick),
				good.Bad.Load(),
			)
		}
		if bad.Ok.Load() != 10 || bad.Bad.Load() < (p95(duration, badTick)-10) {
			t.Errorf(
				"unexpected bad actor 429 (ok=%d, bad=%d {p95(duration, badTick)=%d})",
				bad.Ok.Load(),
				bad.Bad.Load(),
				p95(duration, badTick),
			)
		}
	})

	t.Run("[RpsLimiter.Timeout=10ms] good and bad", func(t *testing.T) {
		t.Parallel()

		good := &Stats{
			RemoteAddr: "127.0.0.1:777",
		}
		bad := &Stats{
			RemoteAddr: "127.0.0.1:666",
		}
		worse := &Stats{
			RemoteAddr: "127.0.0.1:6666",
		}

		limiter := &mono.RpsLimiterClients{
			Quota:   100,
			Timeout: 10 * time.Millisecond,
			Handler429: func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
				switch req.RemoteAddr {
				case "127.0.0.1:777":
					good.Bad.Add(1)
				case "127.0.0.1:666":
					bad.Bad.Add(1)
				case "127.0.0.1:6666":
					worse.Bad.Add(1)
				default:
					t.Error("bad remote address")
				}
				return nil
			},
		}

		req := limiter.Apply(func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
			switch req.RemoteAddr {
			case "127.0.0.1:777":
				good.Ok.Add(1)
			case "127.0.0.1:666":
				bad.Ok.Add(1)
			case "127.0.0.1:6666":
				worse.Ok.Add(1)
			default:
				t.Error("bad remote address")
			}
			return nil
		})

		duration := time.Second
		goodTick := time.Microsecond * 1010
		badTick := time.Microsecond * 50
		worseTick := time.Microsecond * 10
		go requests(t, duration, goodTick, "127.0.0.1:777", req)
		go requests(t, duration, badTick, "127.0.0.1:666", req)
		go requests(t, duration, worseTick, "127.0.0.1:6666", req)
		time.Sleep(duration)

		if good.Ok.Load() < p95(duration, goodTick) || good.Bad.Load() > 0 {
			t.Errorf(
				"good was timeouted (ok=%d {p95(duration, goodTick)=%d}, bad=%d)",
				good.Ok.Load(),
				p95(duration, goodTick),
				good.Bad.Load(),
			)
		}
		if bad.Ok.Load() != 100 || bad.Bad.Load() < (p95(duration, badTick)-100) {
			t.Errorf(
				"unexpected bad actor 429 (ok=%d, bad=%d {p95(duration, badTick)=%d})",
				bad.Ok.Load(),
				bad.Bad.Load(),
				p95(duration, badTick),
			)
		}
		if worse.Ok.Load() != 100 || worse.Bad.Load() < (p95(duration, worseTick)-100) {
			t.Errorf(
				"unexpected worse actor 429 (ok=%d, bad=%d {p95(duration, worseTick)=%d}) %t",
				worse.Ok.Load(),
				worse.Bad.Load(),
				p95(duration, worseTick),
				worse.Bad.Load() < (p95(duration, worseTick)-100),
			)
		}
	})
}

// -- CLASSIC LEAKY BUCKET ALGORITHM
// goos: darwin
// goarch: arm64
// pkg: github.com/kittenbark/mono
// cpu: Apple M2
// BenchmarkRpsLimiterClients
// BenchmarkRpsLimiterClients-8   	 5643919	       204.1 ns/op
//
// -- OLD CLEANING WITH GOROUTINES
// goos: darwin
// goarch: arm64
// pkg: github.com/kittenbark/mono
// cpu: Apple M2
// BenchmarkRpsLimiterClientsRoutines
// BenchmarkRpsLimiterClientsRoutines-8   	 1542751	       729.1 ns/op
func BenchmarkRpsLimiterClients(b *testing.B) {
	clients := []*Stats{}
	for i := 0; i < 100; i++ {
		clients = append(clients,
			&Stats{RemoteAddr: fmt.Sprintf("127.0.0.1:8%03d", i)},
		)
	}

	timeouts := atomic.Int64{}
	limiter := &mono.RpsLimiterClients{
		Quota:   10,
		Timeout: time.Microsecond * 100,
		Handler429: func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
			timeouts.Add(1)
			return nil
		},
	}

	for b.Loop() {
		stat := clients[rand.Intn(len(clients))]
		err := limiter.Apply(func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
			stat.Ok.Add(1)
			return nil
		})(
			b.Context(),
			nil,
			&http.Request{RemoteAddr: stat.RemoteAddr},
		)
		if err != nil {
			b.Fatal(err)
		}
	}

	for _, stat := range clients {
		if stat.Ok.Load() == 0 {
			b.Fatal("too slow, a remote address never got called")
		}
	}
	if timeouts.Load() == 0 {
		b.Fatal("no timeout?")
	}

}
