package mono_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/kittenbark/mono"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

var (
	port atomic.Int64
)

func init() {
	port.Store(30000)
}

func StartForT(t *testing.T, server mono.Server, timeout time.Duration, after time.Duration) {
	time.AfterFunc(after, server.Stop)
	go func() {
		if err := server.Start(); err != nil {
			println(err.Error())
			t.Errorf("err: '%v'", err)
			return
		}
	}()
	time.Sleep(timeout)
}

func PrepareTest() (MonoClient, mono.Server) {
	addr := fmt.Sprintf(":%d", port.Add(1))
	client := MonoClient{url: fmt.Sprintf("http://localhost%s", addr)}
	server := mono.New().
		Addr(addr)
	return client, server
}

func ReadFile(t *testing.T, expectedFile string) []byte {
	expectedData, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatal(err)
	}
	return expectedData
}

type MonoClient struct {
	url string
}

func (client *MonoClient) Get(t *testing.T, path string) []byte {
	link, err := url.JoinPath(client.url, path)
	if err != nil {
		t.Fatalf("client get: %v (path=%s)", err, path)
	}

	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func (client *MonoClient) GetTimeout(t *testing.T, timeout time.Duration, path string) []byte {
	link, err := url.JoinPath(client.url, path)
	if err != nil {
		t.Fatalf("client get: %v (path=%s)", err, path)
	}

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func TestDev_File(t *testing.T) {
	t.Parallel()

	cl, server := PrepareTest()
	server.
		Handler("/single", mono.File("./testdata/mux/test_hello.html")).
		Handler("/single_2", mono.FileLazy("./testdata/mux/test_hello.html")).
		Static("/static", mono.FileHtml("./testdata/mux/test_hello.html")).
		Handler("/lazy", mono.Lazy(mono.FileHtml("./testdata/mux/test_hello.html")))
	StartForT(t, server, time.Millisecond*10, time.Millisecond*100)

	for _, link := range []string{"/single", "/single_2", "/static", "/lazy"} {
		if expected, actual := ReadFile(t, "./testdata/mux/test_hello.html"), cl.Get(t, link); !bytes.Equal(expected, actual) {
			t.Fatalf(`%s: expected "%s" got "%s"`, link, expected, actual)
		}
	}
}

func def[T any](value []T, otherwise T) T {
	if len(value) == 0 {
		return otherwise
	}
	return value[0]
}
