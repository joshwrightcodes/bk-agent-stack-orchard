// SPDX-License-Identifier: AGPL-3.0-or-later

// Command orchardgen writes the wire types of the Orchard API that this controller uses.
//
// The controller talks to Orchard over HTTP and imports no Orchard package. The shape of that
// conversation still belongs to Orchard, so this generator reads the resource types of a pinned
// Orchard version and writes the Go structures that carry them. `go generate` runs it, and a
// pipeline that runs `go generate ./...` and then `git diff --exit-code` reports a contract that
// moved.
//
// What it writes: the field names, the JSON tags, and a type for each field, and only for the
// fields on the allow list of the `wanted` variable, which are the fields that the controller
// uses. It writes no logic of Orchard, and it reads the source of the pinned version over the
// network without keeping a copy in this repository. It reads only public data, so it needs no
// token and sends none. The file that it writes carries a header
// that names the source, the version, the commit, the licence, and the copyright notice.
//
// A field whose type this generator cannot express becomes json.RawMessage, because the
// controller does not read it and a placeholder keeps the structure complete.
package main

import (
	"bytes"
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
	"strconv"
	"strings"
	"time"
)

// wanted lists the Orchard types that this project follows, the name that it gives each one, and
// the fields that it writes for each one. A field that is not on a list is not copied, because
// the generated file copies only what the controller needs for interoperability. The fields come
// from the code of internal/ that reads or sets them, which includes its tests. A field that the
// controller sets in a create request stays here even if no code reads it back, because the
// server needs it in the request body.
var wanted = []struct {
	upstream string
	local    string
	comment  string
	fields   []string // the Go names of the upstream fields, which are the only ones written
}{
	{"VM", "VM", "VM is one virtual machine of the fleet, as Orchard holds it.", []string{
		"CPU", "Conditions", "CreatedAt", "DiskSize", "Headless", "HostDirs", "Image", "ImagePullPolicy",
		"Labels", "Memory", "Name", "NetSoftnet", "NetSoftnetAllow", "OS", "Password",
		"RandomSerial", "Resources", "RestartPolicy", "ScheduledAt", "StartedAt",
		"StartupScript", "Status", "StatusMessage", "Username", "Worker",
	}},
	{
		"Condition", "Condition", "Condition is one fact about a machine, such as that it is scheduled.",
		[]string{"State", "Type"},
	},
	{
		"VMScript", "Script", "Script is the startup script that the worker runs in the guest.",
		[]string{"Env", "ScriptContent", "Transport"},
	},
	{"Worker", "Worker", "Worker is one Mac host that runs virtual machines.", []string{
		"Arch", "Labels", "LastSeen", "Name", "Resources", "Runtime", "SchedulingPaused",
	}},
	{
		"Event", "Event", "Event is one line that the startup script of a machine wrote.",
		[]string{"Kind", "Payload", "Timestamp"},
	},
}

const (
	upstreamRepo = "openai/orchard"
	generatedBy  = "tools/orchardgen"

	// upstreamCopyright is the notice of the LICENSE file of the source at the pinned commit.
	upstreamCopyright = "Copyright 2023-2026 OpenAI"
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
	"pkg/resource/v1/vm_condition.go",
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
//
// It reads the list of references that git reads before a clone, not the GitHub API. The list
// is public and needs no token, and the API limits an anonymous caller to sixty calls each hour.
func verifyRef(version, ref string) error {
	url := fmt.Sprintf("https://github.com/%s.git/info/refs?service=git-upload-pack", upstreamRepo)

	body, err := get(url)
	if err != nil {
		return fmt.Errorf("reading the tags of %s: %w", upstreamRepo, err)
	}

	commit, err := tagCommit(body, version)
	if err != nil {
		return err
	}

	if commit != ref {
		return fmt.Errorf(
			"the tag %s now points at %s, and this repository pins %s. A tag that moves changes "+
				"the generated file without a word: read the difference, then change the -ref of "+
				"the go:generate directive", version, short(commit), short(ref))
	}

	return nil
}

// tagCommit finds the commit of a tag in the reference list that a git server advertises. The
// list is a sequence of packet lines: four hexadecimal digits give the length of a line, those
// four digits included, and "0000" ends a section. An annotated tag names a tag object, and a
// second line, whose name ends in "^{}", names its commit. That commit is the one to compare.
func tagCommit(advertisement []byte, tag string) (string, error) {
	name := "refs/tags/" + tag

	var direct, peeled string

	for rest := advertisement; len(rest) > 0; {
		if len(rest) < 4 {
			return "", fmt.Errorf("the tags of %s end in a line that is cut short", upstreamRepo)
		}

		length, err := strconv.ParseUint(string(rest[:4]), 16, 16)
		if err != nil {
			return "", fmt.Errorf("the tags of %s hold a line with no length", upstreamRepo)
		}

		if length == 0 {
			rest = rest[4:]

			continue
		}

		if length < 4 || int(length) > len(rest) {
			return "", fmt.Errorf("the tags of %s hold a line that is cut short", upstreamRepo)
		}

		line := string(rest[4:length])
		rest = rest[length:]

		// The first reference carries the capabilities of the server after a NUL byte.
		line, _, _ = strings.Cut(strings.TrimSuffix(line, "\n"), "\x00")

		sha, ref, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}

		switch ref {
		case name:
			direct = sha
		case name + "^{}":
			peeled = sha
		}
	}

	switch {
	case peeled != "":
		return peeled, nil
	case direct != "":
		return direct, nil
	default:
		return "", fmt.Errorf("%s has no tag %s", upstreamRepo, tag)
	}
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

// fetch reads one file of the upstream source at a commit. The raw file server gives a file of a
// public repository to anyone, so this needs no token and does not count against the limit of the
// GitHub API.
func fetch(path, ref string) (string, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", upstreamRepo, ref, path)

	body, err := get(url)
	if err != nil {
		return "", fmt.Errorf("reading %s at %s: %w", path, short(ref), err)
	}

	return string(body), nil
}

// get reads one public document. It sends no credentials, even when the environment holds a
// token: everything that it reads is public, and a token that this tool never reads cannot leak
// through it.
func get(url string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil) //nolint:noctx // a short tool run
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "bk-agent-stack-orchard")

	client := &http.Client{Timeout: 30 * time.Second}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the answer from %s was %s", url, response.Status)
	}

	return io.ReadAll(response.Body)
}
