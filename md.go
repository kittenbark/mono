package mono

import (
	"cmp"
	"fmt"
	"html/template"
	"slices"
	"strings"
	"unicode"
)

var MarkdownTags = []MarkdownTag{
	&MarkdownGenericTag{
		Triggers:        []string{"```go"},
		OnNewline:       true,
		TriggersClosing: []string{"\n```"},
		Transformation:  template.Must(template.New("code_go").Funcs(map[string]any{"transform": func(data template.HTML) template.HTML { return data[5 : len(data)-3] }}).Parse(`<pre>{{transform .Children}}</pre>`)),
	},
	&MarkdownGenericTag{
		Triggers:  []string{"```"},
		Insertion: []string{"<pre><code>", "</code></pre>"},
	},
	&MarkdownGenericTag{
		Triggers:  []string{"`"},
		Insertion: []string{`<code class="bg-muted relative rounded px-[0.3rem] py-[0.2rem] font-mono text-sm font-semibold">`, "</code>"},
	},
	&MarkdownGenericTag{
		Triggers:        []string{"#### "},
		OnNewline:       true,
		TriggersClosing: []string{"\n"},
		Insertion:       []string{`<h4 class="scroll-m-20 text-xl font-semibold tracking-tight">`, "</h2>\n"},
		Window:          []rune{'\n'},
	},
	&MarkdownGenericTag{
		Triggers:        []string{"### "},
		OnNewline:       true,
		TriggersClosing: []string{"\n"},
		Insertion:       []string{`<h3 class="scroll-m-20 text-2xl font-semibold tracking-tight">`, "</h2>\n"},
		Window:          []rune{'\n'},
	},
	&MarkdownGenericTag{
		Triggers:        []string{"## "},
		OnNewline:       true,
		TriggersClosing: []string{"\n"},
		Insertion:       []string{`<h2 class="scroll-m-20 border-b pb-2 text-3xl font-semibold tracking-tight first:mt-0">`, "</h2>\n"},
		Window:          []rune{'\n'},
	},
	&MarkdownGenericTag{
		Triggers:        []string{"# "},
		OnNewline:       true,
		TriggersClosing: []string{"\n"},
		Insertion:       []string{`<h1 class="scroll-m-20 text-center text-4xl font-extrabold tracking-tight text-balance">`, "</h1>\n"},
		Window:          []rune{'\n'},
	},
	&MarkdownGenericTag{
		Triggers:        []string{"> "},
		OnNewline:       true,
		TriggersClosing: []string{"\n"},
		Insertion:       []string{`<blockquote class="mt-6 border-l-2 pl-6 italic">`, "</blockquote>\n"},
		Window:          []rune{'\n'},
	},
	&MarkdownTagLink{},
	&MarkdownGenericTag{
		Triggers:  []string{"***", "___"},
		Insertion: []string{"<b><i>", "</i></b>"},
	},
	&MarkdownGenericTag{
		Triggers:  []string{"**", "__"},
		Insertion: []string{"<b>", "</b>"},
	},
	&MarkdownGenericTag{
		Triggers:  []string{"*", "_"},
		Insertion: []string{"<i>", "</i>"},
	},
}

type MarkdownTagAction struct {
	Index          int
	Insertion      string
	Transformation *template.Template
	SkipRange      []int
}

type MarkdownTag interface {
	Next(index int, rn rune) []MarkdownTagAction
}

type MarkdownGenericTag struct {
	Triggers        []string
	TriggersClosing []string
	OnNewline       bool
	DisableSkip     bool
	Transformation  *template.Template
	Insertion       []string
	Window          []rune

	windowSize  int
	opened      bool
	openedWith  string
	openedIndex int
	skip        bool
	oldline     bool
}

func (tag *MarkdownGenericTag) Next(index int, rn rune) []MarkdownTagAction {
	if len(tag.TriggersClosing) == 0 {
		tag.TriggersClosing = tag.Triggers
	}
	if tag.windowSize == 0 {
		tag.windowSize = max(len(tag.Triggers[0]), len(tag.TriggersClosing[0]))
	}
	if rn == '\n' {
		defer func() { tag.oldline = false }()
	} else if unicode.IsSpace(rn) {
		defer func() { tag.oldline = true }()
	}

	tag.Window = append(tag.Window, rn)
	if len(tag.Window) > tag.windowSize {
		tag.Window = tag.Window[1:]
	}
	window := string(tag.Window)
	smallerWindow := window[min(tag.windowSize-len(tag.TriggersClosing[0]), len(tag.Window)):]

	switch {
	case tag.skip:
		tag.skip = false
	case !tag.DisableSkip && rn == '\\':
		tag.skip = true
	case !tag.opened && slices.Contains(tag.Triggers, window) && (!tag.OnNewline || !tag.oldline):
		tag.opened = true
		tag.openedWith = window
		tag.openedIndex = index + 1 - len(window)
	case tag.opened && slices.Contains(tag.TriggersClosing, smallerWindow) && (len(tag.Triggers) == 1 || tag.openedWith == window):
		tag.opened = false
		if tag.Transformation != nil {
			return []MarkdownTagAction{{
				Transformation: tag.Transformation,
				SkipRange:      []int{tag.openedIndex, index + 1},
			}}
		}

		return []MarkdownTagAction{
			{
				Index:     tag.openedIndex,
				Insertion: tag.Insertion[0],
				SkipRange: []int{tag.openedIndex, tag.openedIndex + len(tag.openedWith)},
			},
			{
				Index:     index,
				Insertion: tag.Insertion[1],
				SkipRange: []int{index + 1 - len(smallerWindow), index + 1},
			},
		}
	}

	return nil
}

type MarkdownTagLink struct {
	Parser    func(template.HTML) (template.HTML, error)
	skip      bool
	openHint  bool
	openLink  bool
	openIndex int
	window    [2]rune
	openAt    int
	link      string
	template  *template.Template
}

func (tag *MarkdownTagLink) Next(index int, rn rune) []MarkdownTagAction {
	if tag.Parser == nil {
		tag.Parser = func(data template.HTML) (template.HTML, error) {
			hint, link, _ := strings.Cut(string(data[1:len(data)-1]), "](")
			schema := fmt.Sprintf(`<a class="font-medium text-primary underline underline-offset-4" href="%s">%s</a>`, link, hint)
			return ExecuteSchema(template.Must(template.New("").Parse(schema)), nil)
		}
	}
	if tag.template == nil {
		tag.template = template.Must(template.New("").
			Funcs(map[string]interface{}{"transform": tag.Parser}).
			Parse(`{{transform .Children}}`))
	}

	tag.window[0], tag.window[1] = tag.window[1], rn
	switch {
	case tag.openLink && rn == ')':
		tag.openLink = false
		return []MarkdownTagAction{{
			Index:          tag.openIndex,
			Transformation: tag.template,
			SkipRange:      []int{tag.openIndex, index + 1},
		}}
	case tag.openLink:

	case tag.skip:
		tag.skip = false
	case rn == '\\':
		tag.skip = true

	case tag.openHint && tag.window[0] == ']' && tag.window[1] == '(':
		tag.openHint = false
		tag.openLink = true
	case tag.openHint && tag.window[0] == ']':
		tag.openHint = false
	case tag.openHint:

	case rn == '[':
		tag.openHint = true
		tag.openIndex = index
	}

	return nil
}

func Markdown(data string) (template.HTML, error) {
	actions := make([][]MarkdownTagAction, len(data))
	skip := make([]bool, len(data))
	for _, tag := range MarkdownTags {
		for index, rn := range data {
			if skip[index] {
				continue
			}

			for _, action := range tag.Next(index, rn) {
				if len(action.SkipRange) > 1 {
					for i := action.SkipRange[0]; i < action.SkipRange[1]; i++ {
						skip[i] = true
					}
				}

				if action.Transformation == nil {
					actions[action.Index] = append(actions[action.Index], action)
					continue
				}

				target := template.HTML(data[action.SkipRange[0]:action.SkipRange[1]])
				transformed, err := ExecuteSchema(action.Transformation, struct{ Children template.HTML }{target})
				if err != nil {
					return "", err
				}
				actions[action.SkipRange[0]] = append(actions[action.Index], MarkdownTagAction{
					Index:     action.SkipRange[0],
					Insertion: string(transformed),
				})
			}
		}
	}

	result := []rune{}
	for i, rn := range data {
		slices.SortStableFunc(actions[i], func(a, b MarkdownTagAction) int { return -cmp.Compare(a.Index, b.Index) })
		for _, action := range actions[i] {
			result = append(result, []rune(action.Insertion)...)
		}

		if !skip[i] {
			result = append(result, rn)
		}
	}
	return template.HTML(fmt.Sprintf("<div>%s</div>", string(result))), nil
}
