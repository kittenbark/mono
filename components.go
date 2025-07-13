package mono

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
)

func ExecuteSchema(templ *template.Template, data any) (template.HTML, error) {
	buff := bytes.Buffer{}
	if err := templ.Execute(&buff, data); err != nil {
		return "", err
	}
	return template.HTML(buff.String()), nil
}

func Schema(schema string, funcs template.FuncMap) (*template.Template, error) {
	return template.New("mono.Schema").
		Funcs(funcs).
		Parse(schema)
}

func SchemaApply(schema string, funcs template.FuncMap, data any) (template.HTML, error) {
	templ, err := Schema(schema, funcs)
	if err != nil {
		return "", fmt.Errorf("mono.Schema failed to parse schema: %w", err)
	}
	return ExecuteSchema(templ, data)
}

func SchemaFile(filename string, funcs template.FuncMap, data any) (template.HTML, error) {
	schema, err := os.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("mono.SchemaFile failed to read file %s: %w", filename, err)
	}
	return SchemaApply(string(schema), funcs, data)
}
