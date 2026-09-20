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
