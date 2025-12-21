package mono

import (
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Extension interface {
	Apply(funcs template.FuncMap) error
	SideEffects(result *BuiltPage) error
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

func (mp FuncMap) SideEffects(result *BuiltPage) error { return nil }

type NextjsEnv map[string]string

func (n NextjsEnv) Apply(funcs template.FuncMap) error {
	funcs["_mono_env_map"] = func() map[string]string { return maps.Clone(n) }
	return nil
}

func (n NextjsEnv) SideEffects(result *BuiltPage) error { return nil }

type extensionFile struct {
	mutex        sync.Mutex
	files        []string
	urls         map[string]string
	filetypes    map[string]string
	mimeHints    map[string]string
	mimeHitsLock sync.RWMutex
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
		mimeHints: map[string]string{},
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

	funcs["file_src"] = func(filename string, mimeType ...string) (template.URL, error) {
		extension.mutex.Lock()
		defer extension.mutex.Unlock()

		_, err := os.Stat(filename)
		if err != nil {
			return "", err
		}

		url, cached := extension.url(filename, mimeType...)
		if !cached {
			extension.files = append(extension.files, filename)
		}
		return template.URL(url), nil
	}

	return nil
}

func (extension *extensionFile) SideEffects(result *BuiltPage) error {
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
		result.Subpattern[url] = &BuiltPage{
			ContentType: extension.getContentType(filename, data),
			Data:        data,
		}
		extension.urls[filename] = url
	}
	extension.files = []string{}
	return nil
}

func (extension *extensionFile) url(filename string, mimeType ...string) (url string, cached bool) {
	if url, cached = extension.urls[filename]; cached {
		return
	}
	if len(mimeType) > 0 {
		extension.mimeHitsLock.Lock()
		defer extension.mimeHitsLock.Unlock()
		extension.mimeHints[filename] = strings.Join(mimeType, " ")
	}
	return fmt.Sprintf("/mono/cdn/file/%s%s", hashFile(filename), filepath.Ext(filename)), false
}

func (extension *extensionFile) getContentType(filename string, data []byte) string {
	extension.mimeHitsLock.RLock()
	defer extension.mimeHitsLock.RUnlock()
	if mime, ok := extension.mimeHints[filename]; ok {
		return mime
	}
	return http.DetectContentType(data)
}

func containsDynamicContent(data []byte) bool {
	return strings.HasPrefix(http.DetectContentType(data), "text/") && strings.Contains(string(data), "{${") && strings.Contains(string(data), "}$}")
}
