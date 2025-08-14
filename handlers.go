package mono

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"runtime"
)

type Context struct {
	Url      string
	Filename string
	Funcs    template.FuncMap
}

func File(filename string, contentType ...string) HandlerFunc {
	data, err := os.ReadFile(filename)
	if err != nil {
		panic(fmt.Sprintf("File error: %v (file=%s)", err, filename))
	}
	headerContentType := ""
	if len(contentType) > 0 {
		headerContentType = contentType[0]
	} else {
		headerContentType = http.DetectContentType(data)
	}

	return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		rw.Header().Set("Content-Type", headerContentType)
		if _, err := rw.Write(data); err != nil {
			return err
		}
		return nil
	}
}

func FileLazy(filename string, contentType ...string) HandlerFunc {
	return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		reader, err := os.Open(filename)
		if err != nil {
			return fmt.Errorf("FileLazy error: %v (file=%s)", err, filename)
		}
		if len(contentType) > 0 {
			rw.Header().Set("Content-Type", contentType[0])
		}
		if _, err := io.Copy(rw, reader); err != nil {
			return err
		}
		return nil
	}
}

const contentTypeHTML = "text/html; charset=utf-8"

type Static interface {
	Apply(ctx *Context) (StaticPage, error)
}

func (page StaticPage) Apply(ctx *Context) (StaticPage, error) { return page, nil }

type StaticFunc func(ctx *Context) (StaticPage, error)

func (fn StaticFunc) Apply(ctx *Context) (StaticPage, error) { return fn(ctx) }

type StaticPage struct {
	Data        []byte
	ContentType string
	Subpattern  map[string]StaticPage
}

func Lazy(static Static) HandlerFunc {
	return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		data, err := static.Apply(&Context{Url: req.URL.Path})
		if err != nil {
			return fmt.Errorf("lazy: %w", err)
		}
		if data.ContentType != "" {
			rw.Header().Add("Content-Type", data.ContentType)
		}
		if _, err = rw.Write(data.Data); err != nil {
			return err
		}
		return nil
	}
}

func Html(code template.HTML) Static {
	return StaticFunc(func(ctx *Context) (StaticPage, error) {
		return StaticPage{
			Data:        []byte(code),
			ContentType: contentTypeHTML,
		}, nil
	})
}

func FileHtml(filename string) Static {
	data, err := os.ReadFile(filename)
	if err != nil {
		return staticError(err)
	}
	return Html(template.HTML(data))
}

func FileMedia(filename string) Static {
	data, err := os.ReadFile(filename)
	if err != nil {
		return StaticFunc(func(ctx *Context) (StaticPage, error) { return StaticPage{}, err })
	}
	return StaticFunc(func(ctx *Context) (StaticPage, error) {
		return StaticPage{
			Data:        data,
			ContentType: http.DetectContentType(data),
		}, nil
	},
	)
}

func staticError(err error) Static {
	trace := []string{}
	for i := range 10 {
		_, file, line, ok := runtime.Caller(i + 1)
		if !ok {
			break
		}
		trace = append(trace, fmt.Sprintf("%d. %s:%d", i+1, file, line))
	}
	Log.Error("mono: static error", "err", err, "trace", trace)

	return StaticFunc(func(ctx *Context) (StaticPage, error) {
		return StaticPage{}, err
	})
}

func staticPage(page StaticPage) Static {
	return StaticFunc(func(ctx *Context) (StaticPage, error) { return page, nil })
}

func def[T any](value []T, otherwise T) T {
	if len(value) == 0 {
		return otherwise
	}
	return value[0]
}

func hash(str string) string {
	result := sha256.New224()
	result.Write([]byte(str))
	return hex.EncodeToString(result.Sum(nil))
}
