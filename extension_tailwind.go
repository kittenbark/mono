package mono

import (
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed extension_tailwind.css
var Stylesheet string

type Tailwind struct {
	CLI      string
	CSS      string
	InputCSS string
	Context  context.Context
	Timeout  time.Duration
	NoInline bool
	tags     map[string]struct{}
	tagsLock sync.Mutex
}

func (tailwind *Tailwind) Apply(funcs template.FuncMap) (err error) {
	if tailwind.CSS == "" {
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, rand.Uint64())
		tailwind.CSS = fmt.Sprintf("%s.css", hex.EncodeToString(buf))
	}
	if tailwind.tags == nil {
		tailwind.tags = map[string]struct{}{}
	}
	if !filepath.IsAbs(tailwind.CLI) {
		tailwind.CLI, err = filepath.Abs(tailwind.CLI)
		if err != nil {
			return
		}
	}

	funcs["tailwind"] = tailwind.tag
	return nil
}

func (tailwind *Tailwind) SideEffects(result *StaticPage) error {
	if tailwind.CLI == "" {
		tailwind.CLI = "npx @tailwindcss/cli"
	}
	if tailwind.InputCSS == "" {
		tailwind.InputCSS = Stylesheet
	}

	dir, err := os.MkdirTemp(TempDir, "mono_tailwind_*")
	if err != nil {
		return err
	}
	defer func(path string) { err = errors.Join(err, removeTemp(path)) }(dir)

	contentDir := filepath.Join(dir, "content")
	if err := os.Mkdir(filepath.Join(dir, "content"), 0777); err != nil {
		return err
	}

	htmls := []string{}
	for _, page := range result.Subpattern {
		if page.ContentType == contentTypeHTML {
			file, err := os.CreateTemp(contentDir, "*.html")
			if err != nil {
				return err
			}
			defer func(file *os.File) { _ = file.Close() }(file)
			if _, err = file.Write(page.Data); err != nil {
				return err
			}
			htmls = append(htmls, file.Name())
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "tailwind.config.js"), []byte(`module.exports = {content: ["./content/*"], theme: {extend: {}}, plugins: []}`), 0755); err != nil {
		return err
	}

	inputCSS := filepath.Join(dir, "input.css")
	if err := os.WriteFile(inputCSS, []byte(tailwind.InputCSS), 0644); err != nil {
		return err
	}

	outputCSS := filepath.Join(dir, "output.css")
	args := append(strings.Fields(tailwind.CLI),
		"-i", inputCSS,
		"-o", outputCSS,
		"-m",
	)
	ctx, cancel := context.WithTimeout(alt(tailwind.Context, context.Background()), alt(tailwind.Timeout, time.Second*10))
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("err: %v (%s)", err, string(output))
	}

	dataCSS, err := os.ReadFile(outputCSS)
	if err != nil {
		return err
	}

	if tailwind.NoInline {
		result.Subpattern[tailwind.urlCSS()] = &StaticPage{
			ContentType: "text/css; charset=utf-8",
			Data:        dataCSS,
		}
		return nil
	}

	replaces := []string{}
	styleTag, err := SchemaApply(`<style>{{.Data}}</style>`, nil, struct{ Data template.CSS }{template.CSS(dataCSS)})
	if err != nil {
		return err
	}

	for tag, _ := range tailwind.tags {
		replaces = append(replaces, tag, string(styleTag))
	}

	inliner := strings.NewReplacer(replaces...)
	if len(result.Data) > 0 {
		result.Data = []byte(inliner.Replace(string(result.Data)))
	}
	for _, page := range result.Subpattern {
		page.Data = []byte(inliner.Replace(string(page.Data)))
	}
	return nil
}

func (tailwind *Tailwind) urlCSS() string {
	return fmt.Sprintf("/mono/cdn/tailwind/%s", tailwind.CSS)
}

func (tailwind *Tailwind) tag(extra ...string) template.HTML {
	tailwind.tagsLock.Lock()
	defer tailwind.tagsLock.Unlock()
	result := fmt.Sprintf(`<link rel="stylesheet" href="%s" %s>`, tailwind.urlCSS(), strings.Join(extra, " "))
	tailwind.tags[result] = struct{}{}
	return template.HTML(result)
}

func removeTemp(path string) error {
	if TempDirClean {
		return os.RemoveAll(path)
	}
	return nil
}
