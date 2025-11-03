package mono

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"maps"
	"net/http"
	"os"
	"runtime"
	"slices"
	"sync"
)

type Context struct {
	Url      string
	Filename string
	Funcs    template.FuncMap
	Env      map[string]string
}

func (ctx *Context) Clone() *Context {
	return &Context{
		Url:      ctx.Url,
		Filename: ctx.Filename,
		Funcs:    maps.Clone(ctx.Funcs),
		Env:      maps.Clone(ctx.Env),
	}
}

func (ctx *Context) asFunc() func() Context {
	return func() Context { return *ctx }
}

func (ctx *Context) funcSetEnv() func(keyValues ...string) string {
	lock := sync.Mutex{}
	return func(keyValues ...string) string {
		lock.Lock()
		defer lock.Unlock()
		for keyValue := range slices.Chunk(keyValues, 2) {
			ctx.Env[keyValue[0]] = keyValue[1]
		}
		return ""
	}
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

type Page interface {
	Apply(ctx *Context) (BuiltPage, error)
	IsDynamic() bool
}

type StaticFunc func(ctx *Context) (BuiltPage, error)

func (fn StaticFunc) Apply(ctx *Context) (BuiltPage, error) { return fn(ctx) }

func (fn StaticFunc) IsDynamic() bool { return false }

type BuiltPage struct {
	Data        []byte
	ContentType string
	Subpattern  map[string]*BuiltPage

	Dynamic      bool
	DynamicFuncs template.FuncMap
	DynamicData  func(ctx context.Context, req *http.Request) any
}

func (page BuiltPage) Apply(ctx *Context) (BuiltPage, error) { return page, nil }

func (page BuiltPage) IsDynamic() bool { return page.Dynamic }

func Lazy(page Page) HandlerFunc {
	return func(ctx context.Context, rw http.ResponseWriter, req *http.Request) error {
		data, err := page.Apply(&Context{Url: req.URL.Path})
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

func Html(code template.HTML) Page {
	return StaticFunc(func(ctx *Context) (BuiltPage, error) {
		return BuiltPage{
			Data:        []byte(code),
			ContentType: contentTypeHTML,
		}, nil
	})
}

func FileHtml(filename string) Page {
	data, err := os.ReadFile(filename)
	if err != nil {
		return staticError(err)
	}
	return Html(template.HTML(data))
}

func FileMedia(filename string) Page {
	data, err := os.ReadFile(filename)
	if err != nil {
		return StaticFunc(func(ctx *Context) (BuiltPage, error) { return BuiltPage{}, err })
	}
	return StaticFunc(func(ctx *Context) (BuiltPage, error) {
		return BuiltPage{
			Data:        data,
			ContentType: http.DetectContentType(data),
		}, nil
	},
	)
}

func staticError(err error) Page {
	trace := []string{}
	for i := range 10 {
		_, file, line, ok := runtime.Caller(i + 1)
		if !ok {
			break
		}
		trace = append(trace, fmt.Sprintf("%d. %s:%d", i+1, file, line))
	}
	Log.Error("mono: page error", "err", err, "trace", trace)

	return StaticFunc(func(ctx *Context) (BuiltPage, error) {
		return BuiltPage{}, err
	})
}

func staticPage(page BuiltPage) Page {
	return StaticFunc(func(ctx *Context) (BuiltPage, error) { return page, nil })
}

func def[T any](value []T, otherwise T) T {
	if len(value) == 0 {
		return otherwise
	}
	return value[0]
}

func hashFile(filename string) string {
	result := sha256.New()
	err := func() error {
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, file.Close()) }()
		if _, err = io.Copy(result, file); err != nil {
			result.Write([]byte(filename))
		}
		return nil
	}
	if err != nil {
		result.Write([]byte(filename))
	}
	return hex.EncodeToString(result.Sum(nil))[:16]
}
