package mono

import (
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
)

var ConfigNextjsSpecialFiles = []nextjsContextSpecialFile{
	{
		Filename: "mono.env",
		Action: func(ctx *nextjsContext, path string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("while reading env %s: %w", path, err)
			}
			for line := range strings.SplitSeq(string(data), "\n") {
				name, value, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				name, value = strings.TrimSpace(name), strings.TrimSpace(value)
				ctx.Env[name] = value
			}
			return nil
		},
	},
	{
		Filename: "layout.gohtml",
		Action: func(ctx *nextjsContext, path string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			ctx.layoutSchema = string(data)
			return nil
		},
	},
	{
		Filename: "favicon.ico",
		Action: func(ctx *nextjsContext, path string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			ctx.Page("/favicon.ico", []byte(data))
			return nil
		},
	},
	{
		Filename: "index.gohtml",
		Action: func(ctx *nextjsContext, path string) error {
			pageMain, err := SchemaFileApply(path, ctx.Funcs, ctx.Context)
			if err != nil {
				return err
			}
			ctx.Funcs["children"] = func() template.HTML { return pageMain }
			page, err := SchemaApply(ctx.layoutSchema, path, ctx.Funcs, ctx.Context)
			if err != nil {
				return err
			}
			ctx.Page(ctx.Url, []byte(page))
			return nil
		},
	},
	{
		Filename: "index.md",
		Action: func(ctx *nextjsContext, path string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			pageMain, err := Markdown(string(data))
			if err != nil {
				return err
			}
			ctx.Funcs["children"] = func() template.HTML { return template.HTML(pageMain) }
			page, err := SchemaApply(ctx.layoutSchema, path, ctx.Funcs, ctx.Context)
			if err != nil {
				return err
			}
			ctx.Page(ctx.Url, []byte(page))
			return nil
		},
	},
	{
		Filename: "index.html",
		Action: func(ctx *nextjsContext, path string) error {
			pageMain, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			ctx.Funcs["children"] = func() template.HTML { return template.HTML(pageMain) }
			page, err := SchemaApply(ctx.layoutSchema, path, ctx.Funcs, ctx.Context)
			if err != nil {
				return err
			}
			ctx.Page(ctx.Url, []byte(page))
			return nil
		},
	},
}

func Nextjs(root string, extensions ...Extension) Page {
	page, err := func() (page Page, err error) {
		extensions = append(extensions, newExtensionFile())

		baseContext, err := newNextjsContext(root)
		if err != nil {
			return nil, err
		}

		extensionsSideEffects, err := nextjsExtensionsApplied(baseContext, extensions)
		if err != nil {
			return nil, err
		}
		defer extensionsSideEffects(&err)

		if err = nextjsDir(baseContext, "."); err != nil {
			return nil, err
		}
		if err = walkDirFuncParallel(baseContext.dir, ".", nextjsWalkDir(baseContext)); err != nil {
			return nil, err
		}
		return baseContext.result, nil
	}()
	if err != nil {
		return staticError(err)
	}
	return page
}

func newNextjsContext(root string) (*nextjsContext, error) {
	ctx := &nextjsContext{
		Context: &Context{
			Env:   make(map[string]string),
			Funcs: make(template.FuncMap),
		},
		root: root,
		dir:  os.DirFS(root),
		result: &BuiltPage{
			Subpattern: make(map[string]*BuiltPage),
		},
		resultLock:   &sync.Mutex{},
		specialFiles: ConfigNextjsSpecialFiles,
	}
	return ctx.Updated("."), nil
}

type nextjsContextSpecialFile struct {
	Filename string
	Action   func(ctx *nextjsContext, path string) error
}

type nextjsContext struct {
	*Context
	result       *BuiltPage
	resultLock   *sync.Mutex
	layoutSchema string
	specialFiles []nextjsContextSpecialFile
	templateData any
	dir          fs.FS
	root         string
	err          error
}

func (ctx *nextjsContext) Page(name string, data []byte, contentType ...string) {
	var ct string
	if len(contentType) > 0 {
		ct = contentType[0]
	} else {
		ct = http.DetectContentType(data)
	}

	ctx.resultLock.Lock()
	defer ctx.resultLock.Unlock()
	ctx.result.Subpattern[name] = &BuiltPage{
		Data:        data,
		ContentType: ct,
	}
}

func (ctx *nextjsContext) Clone() *nextjsContext {
	return &nextjsContext{
		Context:      ctx.Context.Clone(),
		result:       ctx.result,
		resultLock:   ctx.resultLock,
		specialFiles: ctx.specialFiles,
		layoutSchema: ctx.layoutSchema,
		templateData: ctx.templateData,
		root:         ctx.root,
		dir:          ctx.dir,
	}
}

func (ctx *nextjsContext) Updated(path string) *nextjsContext {
	ctx.Filename = filepath.Base(path)
	ctx.Url = filepath.Clean("/" + path)

	ctx.Funcs["ctx"] = ctx.Context.asFunc()
	ctx.Funcs["set_env"] = ctx.funcSetEnv()
	ctx.Funcs["rel"] = func(filename string) string { return filepath.Join(ctx.root, path, filename) }
	ctx.Funcs["env"] = func(name string) template.HTML { return template.HTML(ctx.Env[name]) }
	return ctx
}

func (ctx *nextjsContext) Error() error { return ctx.err }

func walkDirFuncParallel(fsys fs.FS, root string, fn fs.WalkDirFunc) error {
	wg := &sync.WaitGroup{}
	errs := make(chan error)

	var parallel fs.WalkDirFunc = func(path string, f fs.DirEntry, err error) error {
		wg.Add(1)
		go func() {
			defer func() {
				if err := recover(); err != nil {
					errs <- fmt.Errorf("panic recovered: %v (at %s)\n%s", err, path, string(debug.Stack()))
				}
				wg.Done()
			}()
			if err := fn(path, f, err); err != nil {
				errs <- fmt.Errorf("%v (at %s)", err, path)
			}
		}()
		return nil
	}

	_ = fs.WalkDir(fsys, root, parallel)
	go func() {
		wg.Wait()
		close(errs)
	}()

	errsList := make([]error, 0)
	for err := range errs {
		errsList = append(errsList, err)
	}
	if err := errors.Join(errsList...); err != nil {
		return err
	}
	return nil
}

func nextjsWalkDir(baseContext *nextjsContext) fs.WalkDirFunc {
	return func(path string, dirEntry fs.DirEntry, err error) error {
		if err != nil || !dirEntry.IsDir() {
			return nil
		}
		ctx := baseContext.Clone().Updated(path)
		return nextjsDir(ctx, path)
	}
}

func nextjsDir(ctx *nextjsContext, path string) error {
	files := map[string]fs.DirEntry{}
	for _, file := range must(fs.ReadDir(ctx.dir, path)) {
		if !file.IsDir() {
			files[file.Name()] = file
		}
	}

	for _, special := range ctx.specialFiles {
		if file, ok := files[special.Filename]; ok {
			if err := special.Action(ctx, filepath.Join(ctx.root, path, file.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func must[T any](result T, err error) T {
	if err != nil {
		panic(err)
	}
	return result
}

func nextjsExtensionsApplied(ctx *nextjsContext, extensions []Extension) (func(*error), error) {
	for _, extension := range extensions {
		if err := extensionApply(extension, ctx.Funcs); err != nil {
			return func(*error) {}, err
		}
	}
	return func(err *error) {
		if err == nil || *err != nil {
			return
		}
		for _, ext := range extensions {
			if e := extensionSideEffects(ext, ctx.result); e != nil {
				*err = e
				return
			}
		}
	}, nil
}

func extensionApply(extension Extension, funcs template.FuncMap) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Join(err, fmt.Errorf("panic: %v", r))
		}
	}()
	return extension.Apply(funcs)
}

func extensionSideEffects(extension Extension, result *BuiltPage) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Join(err, fmt.Errorf("panic: %v", r))
		}
	}()
	return extension.SideEffects(result)
}
