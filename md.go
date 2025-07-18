package mono

import (
	"cmp"
	"fmt"
	"html/template"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode"
)

var (
	MarkdownTags = []MarkdownTag{
		&MarkdownTagCode{
			Transformations: map[string]*template.Template{
				"default": template.Must(template.New("code").
					Funcs(map[string]any{"transform": func(data template.HTML) template.HTML {
						from, to := strings.Index(string(data), "\n"), strings.LastIndex(string(data), "\n")
						if from == to {
							return ""
						}
						return data[from+1 : to]
					}}).
					Parse(`<div class="bg-muted relative rounded mt-5 first:mt-0"><pre class="font-mono text-sm p-[0.5rem]"><code>{{transform .Children}}</code></pre></div>`),
				),
			},
		},
		&MarkdownGenericTag{
			Triggers:  []string{"`"},
			Insertion: []string{`<code class="bg-muted relative rounded px-[0.3rem] py-[0.2rem] font-mono text-sm font-semibold">`, "</code>"},
		},
		&MarkdownGenericTag{
			Triggers:        []string{"#### "},
			OnNewline:       true,
			TriggersClosing: []string{"\n"},
			Insertion:       []string{`<h4 class="scroll-m-20 text-xl font-semibold tracking-tight mt-5 first:mt-0">`, "</h2>\n"},
			Window:          []rune{'\n'},
		},
		&MarkdownGenericTag{
			Triggers:        []string{"### "},
			OnNewline:       true,
			TriggersClosing: []string{"\n"},
			Insertion:       []string{`<h3 class="scroll-m-20 text-2xl font-semibold tracking-tight mt-5 first:mt-0">`, "</h2>\n"},
			Window:          []rune{'\n'},
		},
		&MarkdownGenericTag{
			Triggers:        []string{"## "},
			OnNewline:       true,
			TriggersClosing: []string{"\n"},
			Insertion:       []string{`<h2 class="scroll-m-20 border-b pb-2 text-3xl font-semibold tracking-tight mt-6 first:mt-0">`, "</h2>\n"},
			Window:          []rune{'\n'},
		},
		&MarkdownGenericTag{
			Triggers:        []string{"# "},
			OnNewline:       true,
			TriggersClosing: []string{"\n"},
			Insertion:       []string{`<h1 class="scroll-m-20 text-center text-4xl font-extrabold tracking-tight text-balance mt-6 first:mt-0">`, "</h1>\n"},
			Window:          []rune{'\n'},
		},
		&MarkdownGenericTag{
			Triggers:        []string{"> "},
			OnNewline:       true,
			TriggersClosing: []string{"\n"},
			Insertion:       []string{`<blockquote class="mt-5 border-l-2 pl-2 italic">`, "</blockquote>\n"},
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

	MarkdownTagParagraph = []string{`<p class="leading-5 [&:not(:first-child)]:mt-5">`, `</p>`}

	markdownLock = sync.Mutex{}
)

func Markdown(data string) (template.HTML, error) {
	markdownLock.Lock()
	defer markdownLock.Unlock()

	actions := make([][]MarkdownTagAction, len(data))
	skip := make([]bool, len(data))
	paragraphs := make([]bool, len(data))

	if err := markdownApplyTags(data, skip, actions, paragraphs); err != nil {
		return "", err
	}
	markdownApplyParagraphs(actions, paragraphs, data)

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
	return template.HTML(fmt.Sprintf("<div>\n%s\n</div>", string(result))), nil
}

type MarkdownTagAction struct {
	Index          int
	Insertion      string
	Transformation *template.Template
	Range          []int
	IsNewBlock     bool
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
	if index == 0 {
		tag.oldline = false
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
				Range:          []int{tag.openedIndex, index + 1},
				IsNewBlock:     tag.OnNewline,
			}}
		}

		return []MarkdownTagAction{
			{
				Index:      tag.openedIndex,
				Insertion:  tag.Insertion[0],
				Range:      []int{tag.openedIndex, tag.openedIndex + len(tag.openedWith)},
				IsNewBlock: tag.OnNewline,
			},
			{
				Index:      index,
				Insertion:  tag.Insertion[1],
				Range:      []int{index + 1 - len(smallerWindow), index + 1},
				IsNewBlock: tag.OnNewline,
			},
		}
	}

	return nil
}

type MarkdownTagCode struct {
	Transformations map[string]*template.Template
	state           string
	window          []rune
	hint            []rune
	start           int
}

func (tag *MarkdownTagCode) Next(index int, rn rune) []MarkdownTagAction {
	tag.window = append(tag.window, rn)
	if len(tag.window) > 3 {
		tag.window = tag.window[1:]
	}
	window := string(tag.window)
	switch tag.state {
	case "":
		tag.state = "new"
		fallthrough
	case "new":
		tag.hint = []rune{}
		if window == "```" {
			tag.state = "hint"
			tag.start = index - 2
		}
	case "hint":
		if rn == '\n' {
			tag.state = "body"
			return nil
		}
		tag.hint = append(tag.hint, rn)
	case "body":
		if window != "```" {
			break
		}
		tag.state = "new"
		transformation := tag.Transformations["default"]
		if specific, ok := tag.Transformations[string(tag.hint)]; ok {
			transformation = specific
		}
		return []MarkdownTagAction{{
			Index:          tag.start,
			Range:          []int{tag.start, index + 1},
			Transformation: transformation,
			IsNewBlock:     true,
		}}
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
			Range:          []int{tag.openIndex, index + 1},
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

func markdownApplyTags(data string, skip []bool, actions [][]MarkdownTagAction, paragraphs []bool) error {
	for _, tag := range MarkdownTags {
		for index, rn := range data {
			if skip[index] {
				continue
			}

			isNewlineBased := false
			from, to := math.MaxInt, 0
			for _, action := range tag.Next(index, rn) {
				if len(action.Range) > 1 {
					for i := action.Range[0]; i < action.Range[1]; i++ {
						skip[i] = true
					}
					from = min(from, action.Range[0])
					to = max(to, action.Range[1])
				}

				if action.IsNewBlock {
					isNewlineBased = true
				}

				if action.Transformation == nil {
					actions[action.Index] = append(actions[action.Index], action)
					continue
				}

				target := template.HTML(data[action.Range[0]:action.Range[1]])
				transformed, err := ExecuteSchema(action.Transformation, struct{ Children template.HTML }{target})
				if err != nil {
					return err
				}
				actions[action.Range[0]] = append(actions[action.Index], MarkdownTagAction{
					Index:     action.Range[0],
					Insertion: string(transformed),
				})
			}
			if isNewlineBased && from < to {
				for i := from; i < to; i++ {
					paragraphs[i] = true
				}
			}
		}
	}
	return nil
}

func markdownApplyParagraphs(actions [][]MarkdownTagAction, paragraphs []bool, data string) {
	for from := 0; from < len(data); from++ {
		if paragraphs[from] {
			continue
		}
		var to int
		for to = from + 1; to < len(data); to++ {
			if paragraphs[to] || data[to-1:to+1] == "\n\n" {
				break
			}
			paragraphs[to-1] = true
		}
		to -= 1

		if strings.TrimSpace(data[from:to]) == "" {
			from = to
			continue
		}

		actions[from] = slices.Concat(
			[]MarkdownTagAction{{
				Index:     from,
				Insertion: MarkdownTagParagraph[0],
			}},
			actions[from],
		)
		actions[to] = append(actions[to], MarkdownTagAction{
			Index:     to,
			Insertion: MarkdownTagParagraph[1],
		})
		from = to
	}
}
