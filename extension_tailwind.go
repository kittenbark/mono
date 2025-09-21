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
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	//go:embed extension_tailwind.css
	DefaultTailwindStylesheet string
	//go:embed extension_tailwind.config.js
	DefaultTailwindConfigJs    string
	DefaultTailwindThemeButton template.HTML = `<button data-slot="button"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-all disabled:pointer-events-none disabled:opacity-50 [&amp;_svg]:pointer-events-none [&amp;_svg:not([class*='size-'])]:size-4 shrink-0 [&amp;_svg]:shrink-0 outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px] aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive hover:bg-accent hover:text-accent-foreground dark:hover:bg-accent/50 group/toggle extend-touch-target size-8"
        title="Toggle theme"
        onclick="localStorage.theme = document.documentElement.classList.toggle('dark') ? 'dark' : 'light'"
>
    <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="size-4.5">
        <path stroke="none" d="M0 0h24v24H0z" fill="none"></path>
        <path d="M12 12m-9 0a9 9 0 1 0 18 0a9 9 0 1 0 -18 0"></path>
        <path d="M12 3l0 18"></path>
        <path d="M12 9l4.65 -4.65"></path>
        <path d="M12 14.3l7.37 -7.37"></path>
        <path d="M12 19.6l8.85 -8.85"></path>
    </svg>
    <span class="sr-only">Toggle theme</span>
</button>`
)

// Tailwind — wrapper around tailwind-cli (https://tailwindcss.com/docs/installation/tailwind-cli),
// which is run as a child process, usually it's fast (<1s). We try to use the same stylesheet between all pages.
// input.css could be set as Tailwind.InputCSS, if nothing is specified DefaultTailwindStylesheet is used.
// tailwind.config.js  could be set as Tailwind.InputCSS, if nothing is specified DefaultTailwindConfigJs is used.
//
// Flags supported:
//   - "exe" "./your_path_to_binary_here", default: "npx @tailwindcss/cli"
//   - "inline" "true"/"false", default: "true"
//   - "theme" "light"/"dark"/"system", default: "system"
//
// Examples:
// 1. <head> ... {{tailwind}} ... </head>
// 2. <head> ... {{tailwind "theme" "dark"}} ... </head>
type Tailwind struct {
	// default: npx @tailwindcss/cli
	CLI      string
	CSS      string
	InputCSS string
	ConfigJS string
	Context  context.Context
	Timeout  time.Duration
	noInline bool
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
	funcs["tailwind_theme_button"] = func() template.HTML { return DefaultTailwindThemeButton }
	return nil
}

func (tailwind *Tailwind) SideEffects(result *StaticPage) error {
	if tailwind.CLI == "" {
		tailwind.CLI = "npx @tailwindcss/cli"
	}
	if tailwind.InputCSS == "" {
		tailwind.InputCSS = DefaultTailwindStylesheet
	}
	if tailwind.ConfigJS == "" {
		tailwind.ConfigJS = DefaultTailwindConfigJs
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

	if err := os.WriteFile(filepath.Join(dir, "tailwind.config.js"), []byte(tailwind.ConfigJS), 0755); err != nil {
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

	if tailwind.noInline {
		result.Subpattern[tailwind.urlCSS()] = &StaticPage{
			ContentType: "text/css; charset=utf-8",
			Data:        dataCSS,
		}
		return nil
	}

	replaces := []string{}
	styleTag, err := SchemaApply(
		`<style>{{.Data}}</style>`,
		fmt.Sprintf("tailwind%s", tailwind.urlCSS()),
		nil,
		struct{ Data template.CSS }{template.CSS(dataCSS)},
	)
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

func (tailwind *Tailwind) tag(args ...string) (res template.HTML, err error) {
	tailwind.tagsLock.Lock()
	defer tailwind.tagsLock.Unlock()

	additional := []string{}
	var themeScript string
	for _, arg := range args {
		field, value, _ := strings.Cut(arg, "=")
		switch field {
		case "inline":
			if inline, err := strconv.ParseBool(value); err == nil {
				tailwind.noInline = !inline
			}
		case "theme":
			themeScript, err = tailwind.buildThemeScript(value)
			if err != nil {
				return "", err
			}
		case "exe":
			tailwind.CLI = strings.TrimSpace(value)
		default:
			additional = append(additional, field)
		}
	}

	result := fmt.Sprintf(`<link rel="stylesheet" href="%s" %s>`, tailwind.urlCSS(), strings.Join(additional, " "))
	tailwind.tags[result] = struct{}{}
	return template.HTML(themeScript + result), nil
}

func (tailwind *Tailwind) buildThemeScript(value string) (string, error) {
	const themeScriptTemplate = `<script>
try {
    const is_dark = localStorage.theme === 'dark' %s;
    document.documentElement.classList.toggle('dark', is_dark);
    if (is_dark) {
    	document.querySelector('meta[name="theme-color"]')?.setAttribute('content', '#09090b');    
    }
} catch (_) {}
</script>
`

	var themeScript string
	switch strings.Trim(value, `"`) {
	case "light":
		themeScript = fmt.Sprintf(themeScriptTemplate, "")
	case "dark":
		themeScript = fmt.Sprintf(themeScriptTemplate, `|| !localStorage.theme`)
	case "system":
		themeScript = fmt.Sprintf(themeScriptTemplate,
			`|| ((!('theme' in localStorage) || localStorage.theme === 'system') && window.matchMedia('(prefers-color-scheme: dark)').matches);`,
		)
	case "disable":
		themeScript = ""
	default:
		return "", errors.New(fmt.Sprintf("tailwind: unknown theme tag: %s", value))
	}
	return themeScript, nil
}

func removeTemp(path string) error {
	if TempDirClean {
		return os.RemoveAll(path)
	}
	return nil
}
