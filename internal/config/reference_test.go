// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
)

// update rewrites the configuration reference: go test ./internal/config -run Reference -update,
// or mise run docs-reference.
var update = flag.Bool("update", false, "rewrite docs/user/configuration.md")

const (
	referencePath = "../../docs/user/configuration.md"
	templatePath  = "testdata/configuration.md.tmpl"
	schemaPath    = "../../deploy/helm/bk-agent-stack-orchard/values.schema.json"
)

// sources are the files that declare the types of the configuration file.
var sources = []string{"config.go", "../profile/profile.go", "../profile/agent.go"}

// The defaults that a profile field takes from elsewhere, and not from its own zero value.
var inherited = map[string]string{
	"profiles[].startupTransport":       string(profile.TransportGuestAgent),
	"profiles[].otel.enabled":           "false",
	"profiles[].timeouts.unplaced":      "`defaults.timeouts.unplaced`",
	"profiles[].timeouts.boot":          "`defaults.timeouts.boot`",
	"profiles[].timeouts.acquire":       "`defaults.timeouts.acquire`",
	"profiles[].timeouts.maxVMLifetime": "`defaults.timeouts.maxVMLifetime`",
	"profiles[].agent.source":           "`defaults.agent.source`",
	"profiles[].agent.url":              "`defaults.agent.url`",
	"profiles[].agent.sha256":           "`defaults.agent.sha256`",
	"defaults.agent.source":             string(profile.DefaultAgent.Source),
	"defaults.agent.url":                profile.DefaultAgent.URL,
	"defaults.agent.sha256":             profile.DefaultAgent.SHA256,
}

// key is one row of the reference.
type key struct {
	Path, Type, Default, Text string
}

// fieldTexts reads the first paragraph of the doc comment of each struct field, by type and field.
func fieldTexts(t *testing.T) map[string]string {
	t.Helper()

	texts := map[string]string{}
	files := token.NewFileSet()

	for _, source := range sources {
		file, err := parser.ParseFile(files, source, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}

		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok {
				return true
			}
			fields, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range fields.Fields.List {
				for _, name := range field.Names {
					if field.Doc != nil {
						first, _, _ := strings.Cut(field.Doc.Text(), "\n\n")
						texts[spec.Name.Name+"."+name.Name] = strings.Join(strings.Fields(first), " ")
					}
				}
			}

			return false
		})
	}

	return texts
}

// yamlName gives the name of a field in the file, and false for a field that the file never holds.
func yamlName(field reflect.StructField) (string, bool) {
	name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")

	return name, name != "" && name != "-"
}

func typeName(t reflect.Type) string {
	switch {
	case t == reflect.TypeFor[time.Duration]():
		return "duration"
	case t.Kind() == reflect.Pointer:
		return typeName(t.Elem())
	case t.Kind() == reflect.Struct:
		return "object"
	case t.Kind() == reflect.Map:
		return "map of strings"
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Struct:
		return "list of objects"
	case t.Kind() == reflect.Slice:
		return "list of strings"
	case t.Kind() == reflect.Bool:
		return "boolean"
	case t.Kind() == reflect.Uint64:
		return "integer"
	default:
		return "string"
	}
}

// duration writes a default in the largest whole unit, as an operator writes it.
func duration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return d.String()
	}
}

func defaultText(path string, value reflect.Value) string {
	if text, ok := inherited[path]; ok {
		if strings.HasPrefix(text, "`") {
			return text
		}

		return "`" + text + "`"
	}
	if !value.IsValid() {
		return ""
	}

	switch v := value.Interface().(type) {
	case time.Duration:
		if v == 0 {
			return ""
		}

		return "`" + duration(v) + "`"
	case bool:
		return "`" + fmt.Sprint(v) + "`"
	case uint64:
		return "`" + fmt.Sprint(v) + "`"
	case string:
		if v == "" {
			return ""
		}

		return "`" + v + "`"
	}

	return ""
}

// walk lists each key under one type. value holds the defaults, and is the zero Value under a list.
func walk(t *testing.T, texts map[string]string, typ reflect.Type, value reflect.Value, prefix string) []key {
	t.Helper()

	var keys []key

	for i := range typ.NumField() {
		field := typ.Field(i)
		name, ok := yamlName(field)
		if !ok {
			continue
		}

		path := prefix + name
		text, documented := texts[typ.Name()+"."+field.Name]
		if !documented || text == "" {
			t.Errorf("%s.%s (%s) has no doc comment, so the reference has no text for it",
				typ.Name(), field.Name, path)
		}

		var fieldValue reflect.Value
		if value.IsValid() {
			fieldValue = value.Field(i)
		}

		keys = append(keys, key{
			Path: path, Type: typeName(field.Type), Default: defaultText(path, fieldValue),
			Text: text,
		})

		inner := field.Type
		if inner.Kind() == reflect.Pointer {
			inner, fieldValue = inner.Elem(), reflect.Value{}
		}

		switch {
		case inner.Kind() == reflect.Struct && inner != reflect.TypeFor[time.Duration]():
			keys = append(keys, walk(t, texts, inner, fieldValue, path+".")...)
		case inner.Kind() == reflect.Slice && inner.Elem().Kind() == reflect.Struct:
			keys = append(keys, walk(t, texts, inner.Elem(), reflect.Value{}, path+"[].")...)
		}
	}

	return keys
}

func references(t *testing.T) []key {
	t.Helper()

	return walk(t, fieldTexts(t), reflect.TypeFor[Config](), reflect.ValueOf(*defaults()), "")
}

// wrap breaks text into lines of 100 characters or fewer, the width of the documents.
func wrap(text string) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && len(line)+1+len(word) > 100 {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}

	return strings.Join(append(lines, line), "\n")
}

// render writes the reference: the prose of the template, with a table for each top-level key.
func render(t *testing.T, keys []key) string {
	t.Helper()

	body, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}

	var tables strings.Builder
	for _, k := range keys {
		top := !strings.Contains(k.Path, ".")
		if top {
			fmt.Fprintf(&tables, "\n### `%s`\n\n%s\n\n", k.Path, wrap(k.Text))
			if k.Type != "object" && k.Type != "list of objects" {
				fmt.Fprintf(&tables, "Type: %s.", k.Type)
				if k.Default != "" {
					fmt.Fprintf(&tables, " Default: %s.", k.Default)
				}
				tables.WriteString("\n")

				continue
			}
			tables.WriteString("| Key | Type | Default | Description |\n|---|---|---|---|\n")

			continue
		}
		fmt.Fprintf(&tables, "| `%s` | %s | %s | %s |\n", k.Path, k.Type, k.Default, k.Text)
	}

	page, err := template.New("reference").Parse(string(body))
	if err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := page.Execute(&out, map[string]any{
		"Keys":                   strings.TrimSpace(tables.String()),
		"EnvBuildkiteAgentToken": EnvBuildkiteAgentToken,
		"EnvOrchardToken":        EnvOrchardToken,
		"EnvOrchardCACert":       EnvOrchardCACert,
	}); err != nil {
		t.Fatal(err)
	}

	return out.String()
}

// The reference is written from the code, so it cannot say what the code does not do. CI fails
// when the page is behind the code.
func TestTheConfigurationReferenceIsCurrent(t *testing.T) {
	want := render(t, references(t))

	if *update {
		if err := os.WriteFile(referencePath, []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}

		return
	}

	got, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s is behind the code: run mise run docs-reference, and commit the result",
			referencePath)
	}
}

// schemaNode resolves a $ref of the chart schema.
func schemaNode(t *testing.T, root, node map[string]any) map[string]any {
	t.Helper()

	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}

	resolved, ok := root["definitions"].(map[string]any)[strings.TrimPrefix(ref, "#/definitions/")].(map[string]any)
	if !ok {
		t.Fatalf("the chart schema has no definition for %s", ref)
	}

	return resolved
}

// The chart refuses a key that its schema does not know, so a key of the controller that the schema
// lacks is one that a Helm user cannot set.
func TestTheChartSchemaKnowsEveryKey(t *testing.T) {
	body, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}

	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}

	config := schemaNode(t, root, root["properties"].(map[string]any)["config"].(map[string]any))

	for _, k := range references(t) {
		node := config
		path := strings.Split(strings.ReplaceAll(k.Path, "[]", ".[]"), ".")

		for _, part := range path {
			if part == "[]" {
				node = schemaNode(t, root, node["items"].(map[string]any))

				continue
			}

			properties, _ := node["properties"].(map[string]any)
			next, ok := properties[part].(map[string]any)
			if !ok {
				t.Errorf("the chart schema has no key %s", k.Path)

				break
			}
			node = schemaNode(t, root, next)
		}
	}
}
