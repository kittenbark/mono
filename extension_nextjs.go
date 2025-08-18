package mono

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func Nextjs(root string, extensions ...Extension) Static {
	dir := os.DirFS(root)

	funcs := template.FuncMap{}
	extensions = append(extensions, newExtensionFile())
	for _, extension := range extensions {
		if err := extensionApply(extension, funcs); err != nil {
			return staticError(err)
		}
	}
	funcs["ctx"] = func() Context { return Context{Funcs: funcs} }

	result := StaticPage{
		Subpattern: make(map[string]*StaticPage),
	}
	resultLock := sync.Mutex{}
	wg, errCh, parallelize := newParallelize()
	if err := fs.WalkDir(dir, ".", parallelize(nextjsWalk(&result, &resultLock, root, dir, funcs))); err != nil {
		return staticError(err)
	}
	go func() {
		wg.Wait()
		close(errCh)
	}()
	var joinedErr error
	for err := range errCh {
		joinedErr = errors.Join(joinedErr, err)
	}
	if joinedErr != nil {
		return staticError(joinedErr)
	}

	_, err := directoryFS(dir, map[string]func(filename string) (any, error){
		"favicon.ico": func(filename string) (any, error) {
			data, err := fs.ReadFile(dir, filename)
			if err != nil {
				return nil, err
			}
			result.Subpattern["/favicon.ico"] = &StaticPage{
				Data:        data,
				ContentType: http.DetectContentType(data),
			}
			return nil, nil
		},
	})
	if err != nil {
		return staticError(err)
	}

	for _, ext := range extensions {
		if err := extensionSideEffects(ext, &result); err != nil {
			return staticError(err)
		}
	}

	return staticPage(result)
}

func newParallelize() (*sync.WaitGroup, chan error, func(fn fs.WalkDirFunc) fs.WalkDirFunc) {
	wg := &sync.WaitGroup{}
	errCh := make(chan error)
	return wg, errCh, func(fn fs.WalkDirFunc) fs.WalkDirFunc {
		return func(path string, d fs.DirEntry, err error) error {
			wg.Add(1)
			go func() {
				defer func() {
					wg.Done()
					if r := recover(); r != nil {
						errCh <- fmt.Errorf("panic: %v, root=%s", r, path)
					}
				}()
				if err := fn(path, d, err); err != nil {
					errCh <- err
				}
			}()
			return nil
		}
	}
}

func nextjsWalk(
	result *StaticPage,
	resultLock *sync.Mutex,
	root string,
	dir fs.FS,
	funcs template.FuncMap,
) fs.WalkDirFunc {
	return func(path string, d fs.DirEntry, err error) error {
		dirname := filepath.Base(path)
		if err != nil || !d.IsDir() || strings.HasSuffix(dirname, ".") && dirname != "." {
			return err
		}
		funcs := maps.Clone(funcs)
		funcs["rel"] = func(filename string) string { return filepath.Join(root, path, filename) }

		list, err := fs.ReadDir(dir, path)
		if err != nil {
			return err
		}
		filesOnly := []fs.DirEntry{}
		for _, el := range list {
			if !el.IsDir() {
				filesOnly = append(filesOnly, el)
			}
		}

		type Data struct {
			Context  Context
			Children template.HTML
		}
		ctx := Data{Context: Context{
			Funcs:    funcs,
			Filename: filepath.Base(path),
			Url:      filepath.Clean("/" + path),
		}}
		funcs["ctx"] = func() Context { return ctx.Context }

		subdir, err := fs.Sub(dir, path)
		if err != nil {
			return err
		}
		switch len(filesOnly) {
		case 0:
			return nil
		case 1:
			children, err := directoryFS(subdir, nextJsWalkSpecials(subdir, funcs, ctx))
			if err != nil {
				return err
			}
			if len(children) > 0 {
				ctx.Children = children
				break
			}

			data, err := fs.ReadFile(dir, filepath.Join(path, filesOnly[0].Name()))
			if err != nil {
				return err
			}
			ctx.Children = template.HTML(data)
		default:
			ctx.Children, err = directoryFS(subdir, nextJsWalkSpecials(subdir, funcs, ctx))
		}
		if err != nil {
			return err
		}

		buff := bytes.Buffer{}
		layout, err := dirLayout(dir, funcs, path)
		if err != nil {
			return err
		}
		if err = layout.Execute(&buff, ctx); err != nil {
			return fmt.Errorf("layout.Execute: %w", err)
		}
		page := &StaticPage{
			Data:        buff.Bytes(),
			ContentType: http.DetectContentType(buff.Bytes()),
		}

		resultLock.Lock()
		defer resultLock.Unlock()
		result.Subpattern[path] = page
		return nil
	}
}

func nextJsWalkSpecials[T any](subdir fs.FS, funcs template.FuncMap, ctx T) map[string]func(filename string) (template.HTML, error) {
	return map[string]func(filename string) (template.HTML, error){
		"index.gohtml": func(filename string) (template.HTML, error) {
			schema, err := fs.ReadFile(subdir, filename)
			if err != nil {
				return "", err
			}
			return SchemaApply(string(schema), funcs, ctx)
		},
		"index.md": func(filename string) (template.HTML, error) {
			data, err := fs.ReadFile(subdir, filename)
			if err != nil {
				return "", err
			}
			return Markdown(string(data))
		},
		"index.html": func(filename string) (template.HTML, error) {
			data, err := fs.ReadFile(subdir, filename)
			if err != nil {
				return "", err
			}
			return template.HTML(data), nil
		},
	}
}

func dirLayout(dir fs.FS, funcs template.FuncMap, path string) (*template.Template, error) {
	schema := `{{.Children}}`
	_, err := directoryFS[string](dir, map[string]func(filename string) (string, error){
		"layout.gohtml": func(filename string) (string, error) {
			data, err := fs.ReadFile(dir, filename)
			if err != nil {
				return "", err
			}
			schema = string(data)
			return "", nil
		},
	})
	if err != nil {
		return nil, err
	}
	return Schema(schema, funcs, path)
}

func directory[T any](dir string, mapped map[string]func(filename string) (T, error)) (T, error) {
	return directoryFS(os.DirFS(dir), mapped)
}

func directoryFS[T any](dir fs.FS, mapped map[string]func(filename string) (T, error)) (T, error) {
	entries, err := fs.ReadDir(dir, ".")
	var null T
	if err != nil {
		return null, fmt.Errorf("directory: %w", err)
	}
	for _, entry := range entries {
		if action, ok := mapped[entry.Name()]; ok {
			return action(entry.Name())
		}
	}
	return null, nil
}

func extensionApply(extension Extension, funcs template.FuncMap) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Join(err, fmt.Errorf("panic: %v", r))
		}
	}()

	return extension.Apply(funcs)
}

func extensionSideEffects(extension Extension, result *StaticPage) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Join(err, fmt.Errorf("panic: %v", r))
		}
	}()
	return extension.SideEffects(result)
}
