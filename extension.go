package mono

import (
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

type Extension interface {
	Apply(funcs template.FuncMap) error
	SideEffects(result *StaticPage) error
}

var (
	_ Extension = (*FuncMap)(nil)
	_ Extension = (*Tailwind)(nil)
	_ Extension = (*extensionFile)(nil)
	_ Extension = (NextjsEnv)(nil)
)

type FuncMap template.FuncMap

func (mp FuncMap) Apply(funcs template.FuncMap) error {
	for k, v := range mp {
		funcs[k] = v
	}
	return nil
}

func (mp FuncMap) SideEffects(result *StaticPage) error { return nil }

type NextjsEnv map[string]string

func (n NextjsEnv) Apply(funcs template.FuncMap) error {
	funcs["_mono_env_map"] = func() map[string]string { return maps.Clone(n) }
	return nil
}

func (n NextjsEnv) SideEffects(result *StaticPage) error { return nil }

type extensionFile struct {
	mutex     sync.Mutex
	files     []string
	urls      map[string]string
	filetypes map[string]string
}

func newExtensionFile() *extensionFile {
	filetypes := map[string]string{}
	for type_, exts := range Filetypes {
		for _, ext := range exts {
			filetypes[ext] = type_
		}
	}
	return &extensionFile{
		files:     []string{},
		urls:      map[string]string{},
		filetypes: filetypes,
	}
}

func (extension *extensionFile) Apply(funcs template.FuncMap) error {
	funcs["file"] = func(filename string) (template.HTML, error) {
		extension.mutex.Lock()
		defer extension.mutex.Unlock()

		_, err := os.Stat(filename)
		if err != nil {
			return "", err
		}

		filetype, ok := extension.filetypes[filepath.Ext(filename)]
		if !ok {
			return "", fmt.Errorf("unexpected file extension: %s", filename)
		}

		url, cached := extension.url(filename)
		if !cached {
			extension.files = append(extension.files, filename)
		}
		return template.HTML(fmt.Sprintf(FiletypesTags[filetype], url, url)), nil
	}

	funcs["file_src"] = func(filename string) (template.URL, error) {
		extension.mutex.Lock()
		defer extension.mutex.Unlock()

		_, err := os.Stat(filename)
		if err != nil {
			return "", err
		}

		url, cached := extension.url(filename)
		if !cached {
			extension.files = append(extension.files, filename)
		}
		return template.URL(url), nil
	}

	return nil
}

func (extension *extensionFile) SideEffects(result *StaticPage) error {
	extension.mutex.Lock()
	defer extension.mutex.Unlock()

	for _, filename := range extension.files {
		stat, err := os.Stat(filename)
		if err != nil {
			return err
		}
		if stat.IsDir() {
			return fmt.Errorf("%s is a directory, not a file", filename)
		}
		if stat.Size() > InMemoryFilesizeThreshold {
			Log.Warn("file size too large (unsupported for now)")
		}

		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		url, _ := extension.url(filename)
		result.Subpattern[url] = &StaticPage{
			ContentType: http.DetectContentType(data),
			Data:        data,
		}
		extension.urls[filename] = url
	}
	extension.files = []string{}
	return nil
}

func (extension *extensionFile) url(filename string) (url string, cached bool) {
	if url, cached = extension.urls[filename]; cached {
		return
	}
	return fmt.Sprintf("/mono/cdn/file/%s%s", hashFile(filename), filepath.Ext(filename)), false
}
