package mono

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Tailwind struct {
	CLI     string
	CSS     string
	Context context.Context
	Timeout time.Duration
}

func (tailwind *Tailwind) Apply(funcs template.FuncMap) error {
	if tailwind.CSS == "" {
		tailwind.CSS = fmt.Sprintf("%d.css", time.Now().UnixNano())
	}

	funcs["tailwind"] = func() template.HTML {
		if IsDev() {
			return `<script src="https://cdn.tailwindcss.com"></script>`
		}
		return template.HTML(fmt.Sprintf(`<link href="%s" rel="stylesheet">`, tailwind.urlCSS()))
	}
	return nil
}

func (tailwind *Tailwind) SideEffects(result *StaticPage) error {
	if tailwind.CLI == "" {
		tailwind.CLI = "npx @tailwindcss/cli"
	}

	if IsDev() {
		return nil
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

	inputCSS := filepath.Join(dir, "input.css")
	if err := os.WriteFile(inputCSS, []byte(`@import "tailwindcss"; @tailwind base; @tailwind utilities;`), 0644); err != nil {
		return err
	}

	outputCSS := filepath.Join(dir, "output.css")
	args := append(strings.Fields(tailwind.CLI),
		`--content`, contentDir+"/*",
		"-i", inputCSS,
		"-o", outputCSS,
		"-m",
	)
	ctx, cancel := context.WithTimeout(alt(tailwind.Context, context.Background()), alt(tailwind.Timeout, time.Second*10))
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("err: %v (%s)", err, string(output))
	}

	resultData, err := os.ReadFile(outputCSS)
	if err != nil {
		return err
	}
	result.Subpattern[tailwind.urlCSS()] = StaticPage{
		ContentType: "text/css; charset=utf-8",
		Data:        resultData,
	}
	return nil
}

func (tailwind *Tailwind) urlCSS() string {
	return fmt.Sprintf("/mono/tailwind/%s", tailwind.CSS)
}

func removeTemp(path string) error {
	if TempDirClean {
		return os.RemoveAll(path)
	}
	return nil
}
