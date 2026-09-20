// Command orchardgen writes the wire types of the Orchard API that this controller uses.
//
// The controller talks to Orchard over HTTP and imports no Orchard package. The shape of that
// conversation still belongs to Orchard, so this generator reads the resource types of a pinned
// Orchard version and writes the Go structures that carry them. `go generate` runs it, and a
// pipeline that runs `go generate ./...` and then `git diff --exit-code` reports a contract that
// moved.
//
// What it writes: the field names, the JSON tags, and a type for each field. It writes no logic
// of Orchard, and it reads the source of the pinned version over the network without keeping a
// copy in this repository.
//
// A field whose type this generator cannot express becomes json.RawMessage, because the
// controller does not read it and a placeholder keeps the structure complete.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// wanted lists the Orchard types that this project follows, and the name that it gives each one.
var wanted = []struct {
	upstream string
	local    string
	comment  string
}{
	{"VM", "VM", "VM is one virtual machine of the fleet, as Orchard holds it."},
	{"VMScript", "Script", "Script is the startup script that the worker runs in the guest."},
	{"Worker", "Worker", "Worker is one Mac host that runs virtual machines."},
	{"Event", "Event", "Event is one line that the startup script of a machine wrote."},
}

const (
	upstreamRepo = "openai/orchard"
	generatedBy  = "tools/orchardgen"
)

// sources are the upstream files that declare the types this project follows, and the named
// types that their fields use. A named type that this list misses becomes json.RawMessage, which
// is correct but tells a reader less.
var sources = []string{
	"pkg/resource/v1/v1.go",
	"pkg/resource/v1/worker.go",
	"pkg/resource/v1/labels.go",
	"pkg/resource/v1/resources.go",
	"pkg/resource/v1/image_pull_policy.go",
	"pkg/resource/v1/restart_policy.go",
	"pkg/resource/v1/host_dir.go",
}

func main() {
	ref := flag.String("ref", "", "the Orchard commit to read, which must be the commit of the "+
		"version that the design states")
	version := flag.String("version", "", "a version to follow instead of the one in the design")
	out := flag.String("out", "", "the file to write")
	design := flag.String("design", "docs/architecture/target-design.md",
		"the document that states the minimum Orchard version")
	flag.Parse()

	if *out == "" {
		log.Fatal("orchardgen: -out is necessary")
	}

	resolved := *version
	if resolved == "" {
		var err error
		resolved, err = pinnedVersion(*design)
		if err != nil {
			log.Fatalf("orchardgen: %v", err)
		}
	}

	// A tag can move, and a generator that follows a tag then writes something else without a
	// word. The commit is the pin, and the version is what a person reads, so the two must agree.
	source := resolved
	if *ref != "" {
		if err := verifyRef(resolved, *ref); err != nil {
			log.Fatalf("orchardgen: %v", err)
		}
		source = *ref
	}

	types, err := readUpstream(source)
	if err != nil {
		log.Fatalf("orchardgen: %v", err)
	}

	rendered, err := render(resolved, *ref, types)
	if err != nil {
		log.Fatalf("orchardgen: %v", err)
	}

	if err := os.WriteFile(*out, rendered, 0o600); err != nil {
		log.Fatalf("orchardgen: writing %s: %v", *out, err)
	}

	fmt.Printf("orchardgen: wrote %s from Orchard %s (%s)\n", *out, resolved, short(source))
}

// short gives the readable part of a commit, and leaves a version alone.
func short(ref string) string {
	if len(ref) == 40 {
		return ref[:12]
	}

	return ref
}

// verifyRef confirms that the tag of a version still points at the commit that this repository
// pins. A tag that moved is a supply-chain change, and it stops the generator here instead of
// changing the generated file without a word.
func verifyRef(version, ref string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/commits/%s", upstreamRepo, version)

	body, err := get(url)
	if err != nil {
		return fmt.Errorf("resolving the tag %s: %w", version, err)
	}

	var commit struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &commit); err != nil {
		return fmt.Errorf("reading the commit of %s: %w", version, err)
	}

	if commit.SHA != ref {
		return fmt.Errorf(
			"the tag %s now points at %s, and this repository pins %s. A tag that moves changes "+
				"the generated file without a word: read the difference, then change the -ref of "+
				"the go:generate directive", version, short(commit.SHA), short(ref))
	}

	return nil
}

// pinnedVersion reads the version out of the design, so that one document decides it.
func pinnedVersion(path string) (string, error) {
	body, err := os.ReadFile(path) //nolint:gosec // the path comes from a flag of this tool
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}

	match := regexp.MustCompile(`minimum Orchard version MUST be ([0-9.]+)`).FindSubmatch(body)
	if match == nil {
		return "", fmt.Errorf("%s no longer states a minimum Orchard version", path)
	}

	return string(match[1]), nil
}

// declaration is one structure or one named type of the upstream package.
type declaration struct {
	fields   []field  // for a structure
	embedded []string // structures that it embeds, whose fields travel with it
	alias    ast.Expr // for a named type that is not a structure
}

type field struct {
	name    string
	tag     string // the whole json tag, so that omitempty survives
	wire    string // the name on the wire, for ordering and for a message
	goType  string
	comment string
}

// readUpstream parses the Orchard resource types at a version.
func readUpstream(version string) (map[string]*declaration, error) {
	all := map[string]*declaration{}

	for _, path := range sources {
		source, err := fetch(path, version)
		if err != nil {
			return nil, err
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parsing %s at %s: %w", path, version, err)
		}

		collect(file, all)
	}

	return all, nil
}

func collect(file *ast.File, into map[string]*declaration) {
	for _, decl := range file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.TYPE {
			continue
		}

		for _, spec := range general.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				into[typeSpec.Name.Name] = &declaration{alias: typeSpec.Type}

				continue
			}

			into[typeSpec.Name.Name] = structFields(structure)
		}
	}
}

func structFields(structure *ast.StructType) *declaration {
	found := &declaration{}

	for _, f := range structure.Fields.List {
		// A field with no name embeds another structure, whose fields travel on the wire.
		if len(f.Names) == 0 {
			if name, ok := f.Type.(*ast.Ident); ok {
				found.embedded = append(found.embedded, name.Name)
			}

			continue
		}

		tag := jsonTag(f)
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}

		found.fields = append(found.fields, field{
			name:    f.Names[0].Name,
			tag:     tag,
			wire:    name,
			goType:  expression(f.Type),
			comment: strings.TrimSpace(f.Comment.Text()),
		})
	}

	return found
}

func jsonTag(f *ast.Field) string {
	if f.Tag == nil {
		return ""
	}

	match := regexp.MustCompile(`json:"([^"]*)"`).FindStringSubmatch(f.Tag.Value)
	if match == nil {
		return ""
	}

	return match[1]
}

// expression gives the Go source of a type, which the renderer then resolves.
func expression(e ast.Expr) string {
	var b bytes.Buffer
	if err := format.Node(&b, token.NewFileSet(), e); err != nil {
		return ""
	}

	return b.String()
}

func fetch(path, ref string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s", upstreamRepo, path, ref)

	raw, err := get(url)
	if err != nil {
		return "", fmt.Errorf("reading %s at %s: %w", path, short(ref), err)
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body.Content, "\n", ""))
	if err != nil {
		return "", err
	}

	return string(decoded), nil
}

// get reads one document of the GitHub API. A token raises the limit for an anonymous caller,
// which is sixty calls each hour.
func get(url string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil) //nolint:noctx // a short tool run
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "bk-agent-stack-orchard")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 30 * time.Second}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the answer was %s", response.Status)
	}

	return io.ReadAll(response.Body)
}
