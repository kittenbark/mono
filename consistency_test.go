package mono_test

import (
	"context"
	"encoding/json"
	"github.com/kittenbark/mono"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConsistency(t *testing.T) {
	t.Parallel()

	client, server := PrepareTest()

	server.
		Middleware(func(handler mono.HandlerFunc) mono.HandlerFunc {
			return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
				if err := handler(ctx, rw, req); err != nil {
					t.Errorf("%s: unexpected error: %v", req.RequestURI, err)
				}
				return nil
			}
		}).
		Handler("/handler", func() mono.HandlerFunc {
			type Data struct {
				Call int64 `json:"call"`
			}
			calls := atomic.Int64{}
			return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
				data, err := json.Marshal(Data{Call: calls.Add(1)})
				if err != nil {
					return err
				}
				if _, err := rw.Write(data); err != nil {
					return err
				}
				return nil
			}
		}()).
		Static("/nextjs", mono.Nextjs("./testdata/consistency/source",
			mono.FuncMap{
				"component_value":        func() string { return "component_value" },
				"component_with_context": func(ctx mono.Context) string { return ctx.Url },
				"component_file": func(path string) (string, error) {
					data, err := os.ReadFile(path)
					if err != nil {
						return "", err
					}
					return strings.TrimSpace(string(data)), nil
				},
			}))
	StartForT(t, server, time.Second, time.Second*5)

	for i := 1; i <= 100; i++ {
		data := map[string]int64{}
		if err := json.Unmarshal(client.GetTimeout(t, time.Millisecond*10, "/handler"), &data); err != nil {
			t.Fatal(err)
		}
		if data["call"] != int64(i) {
			t.Fatalf("call != i (%d != %d)", data["call"], i)
		}
	}

	check := func(path string, expected string) {
		if resp := client.GetTimeout(t, time.Millisecond*10, path); strings.TrimSpace(string(resp)) != strings.TrimSpace(expected) {
			t.Fatalf("at '%s', expected != response, %s != %s", path, expected, string(resp))
		}
	}

	check(
		"/nextjs",
		"<html lang=\"en\">\n<head><title>Test</title></head>\n<body>\ncomponent_value\n/\nroot\n</body>\n</html>",
	)

	check(
		"/nextjs/alt",
		"<html lang=\"en\">\n<head><title>Test</title></head>\n<body>\ncomponent_value\n/alt\nalt\n\n</body>\n</html>",
	)

	check(
		"/nextjs/sub",
		"<html lang=\"en\">\n<head><title>Test</title></head>\n<body>\ncomponent_value\n/sub\n<div>\n<h1 class=\"scroll-m-20 text-center text-4xl font-extrabold tracking-tight text-balance mt-6 first:mt-0\">sub</h1>\n\n</div>\n</body>\n</html>",
	)

	check(
		"/nextjs/sub/subsub",
		"<html lang=\"en\">\n<head><title>Test</title></head>\n<body>\ncomponent_value\n/sub/subsub\nsubsub\n</body>\n</html>",
	)
}
