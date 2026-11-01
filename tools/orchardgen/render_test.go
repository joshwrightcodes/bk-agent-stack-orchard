package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// parse reads a small Go file, so that a test of the generator needs no network.
func parse(t *testing.T, source string) map[string]*declaration {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "upstream.go", source, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing the source of the test: %v", err)
	}

	types := map[string]*declaration{}
	collect(file, types)

	return types
}

// An embedded structure carries its fields on the wire, so the generator must follow it.
func TestFlattenFollowsAnEmbeddedStructure(t *testing.T) {
	types := parse(t, `package v1

type Meta struct {
	Name string `+"`json:\"name,omitempty\"`"+`
}

type VMSpec struct {
	Image string `+"`json:\"image,omitempty\"`"+`
}

type VM struct {
	VMSpec
	Meta
	Status string `+"`json:\"status,omitempty\"`"+`
}
`)

	var got []string
	for _, f := range flatten(types["VM"], types, map[string]bool{}) {
		got = append(got, f.wire)
	}

	want := "image, name, status"
	if strings.Join(got, ", ") != want {
		t.Errorf("flatten() = %v, want %s in that order", got, want)
	}
}

func TestResolveGivesTheTypeThatThisProjectCanCarry(t *testing.T) {
	types := parse(t, `package v1

type Labels map[string]string
type Resources map[string]uint64
type VMStatus string
type HostDir struct {
	Name string `+"`json:\"name\"`"+`
}
`)

	cases := map[string]string{
		"Labels":    "map[string]string",
		"Resources": "map[string]uint64",
		"VMStatus":  "string",
		"string":    "string",
		"[]string":  "[]string",
		"time.Time": "time.Time",
		// A structure that this project does not read keeps its place and loses its content.
		"HostDir": "json.RawMessage",
		// A type that upstream declares nowhere is unknown, so it keeps its place too.
		"Mystery": "json.RawMessage",
	}

	for in, want := range cases {
		if got := resolve(in, types); got != want {
			t.Errorf("resolve(%q) = %q, want %q", in, got, want)
		}
	}
}

// A type that the generator also writes keeps its shape, so that a reader follows one structure
// into another instead of reading raw JSON.
func TestResolveLinksATypeThatTheGeneratorAlsoWrites(t *testing.T) {
	types := parse(t, "package v1\n\ntype VMScript struct{}\n")

	if got := resolve("*VMScript", types); got != "*Script" {
		t.Errorf("resolve(*VMScript) = %q, want *Script", got)
	}
}

// upstreamOfTheTests declares each type that the generator follows, with every field on its allow
// list and one more field that the controller does not read.
func upstreamOfTheTests(extra string) string {
	var b strings.Builder

	b.WriteString("package v1\n\n")

	for _, want := range wanted {
		b.WriteString("type " + want.upstream + " struct {\n")

		for _, name := range want.fields {
			b.WriteString("\t" + name + " string `json:\"" + strings.ToLower(name) + ",omitempty\"`\n")
		}

		b.WriteString(extra + "}\n\n")
	}

	return b.String()
}

const unusedField = "\tNonsense string `json:\"nonsense,omitempty\"`\n"

// The controller reads a few fields of each Orchard type, so the generated file carries those and
// nothing more.
func TestRenderKeepsOnlyTheFieldsTheControllerUses(t *testing.T) {
	types := parse(t, upstreamOfTheTests(unusedField))

	got, err := render("0.58.0", "22bd63c34674a1422896aa208a464f4340cdb0b8", types)
	if err != nil {
		t.Fatalf("render() failed: %v", err)
	}

	if !strings.Contains(string(got), `json:"scriptcontent,omitempty"`) {
		t.Errorf("render() lost the field that the controller reads:\n%s", got)
	}

	if strings.Contains(string(got), "Nonsense") || strings.Contains(string(got), "nonsense") {
		t.Errorf("render() wrote a field that is not on the allow list:\n%s", got)
	}
}

// The file copies names from a licensed source, so a reader must see where they come from and
// under which terms.
func TestRenderWritesTheSourceAndLicenceHeader(t *testing.T) {
	types := parse(t, upstreamOfTheTests(unusedField))

	got, err := render("0.58.0", "22bd63c34674a1422896aa208a464f4340cdb0b8", types)
	if err != nil {
		t.Fatalf("render() failed: %v", err)
	}

	header, _, _ := strings.Cut(string(got), "package orchard")

	for _, want := range []string{
		"github.com/openai/orchard",
		"0.58.0",
		"22bd63c34674",
		"FSL-1.1-ALv2",
		"Copyright 2023-2026 OpenAI",
	} {
		if !strings.Contains(header, want) {
			t.Errorf("the header lacks %q:\n%s", want, header)
		}
	}
}

// A field that upstream renames or removes must fail the gate, not vanish from the file.
func TestRenderFailsWhenAnAllowedFieldIsGone(t *testing.T) {
	types := parse(t, strings.Replace(upstreamOfTheTests(""), "\tStatus string", "\tState string", 1))

	if _, err := render("0.58.0", "", types); err == nil || !strings.Contains(err.Error(), "Status") {
		t.Errorf("render() error = %v, want one that names the field Status", err)
	}
}
