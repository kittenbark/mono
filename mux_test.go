package mono_test

import (
	"bytes"
	"fmt"
	"github.com/kittenbark/mono"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

func TestDev_File(t *testing.T) {
	t.Parallel()

	c, addr := testUrl()
	go func() {
		err := closedAfter(time.Millisecond*20, mono.Mux()).
			Addr(addr).
			Handler("/single", mono.File("./testdata/test_hello.html")).
			Handler("/single_2", mono.FileLazy("./testdata/test_hello.html")).
			Static("/static", mono.FileHtml("./testdata/test_hello.html")).
			Handler("/lazy", mono.Lazy(mono.FileHtml("./testdata/test_hello.html"))).
			Start()
		if err != nil {
			t.Error(err)
			return
		}
	}()
	time.Sleep(time.Millisecond * 10)

	for _, link := range []string{"/single", "/single_2", "/static", "/lazy"} {
		if expected, actual := readFile(t, "./testdata/test_hello.html"), c.Get(t, link); !bytes.Equal(expected, actual) {
			t.Fatalf(`%s: expected "%s" got "%s"`, link, expected, actual)
		}
	}
}

func readFile(t *testing.T, expectedFile string) []byte {
	expectedData, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatal(err)
	}
	return expectedData
}

var (
	testAddrIndex = 9000
	testAddrMutex sync.Mutex
)

func testAddr() string {
	testAddrMutex.Lock()
	defer testAddrMutex.Unlock()
	testAddrIndex++
	return fmt.Sprintf(":%d", testAddrIndex)
}

func testUrl() (*client, string) {
	addr := testAddr()
	return &client{url: "http://localhost" + addr}, addr
}

func closedAfter(after time.Duration, server mono.Server) mono.Server {
	time.AfterFunc(after, server.Stop)
	return server
}

func unwrap[T any](value []T, otherwise T) T {
	if len(value) == 0 {
		return otherwise
	}
	return value[0]
}

type client struct {
	url string
}

func (client *client) Get(t *testing.T, path string) []byte {
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
