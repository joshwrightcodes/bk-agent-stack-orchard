// SPDX-License-Identifier: AGPL-3.0-or-later

package repocheck_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// These tests hold the dependency and licence rules that read go.mod, the Go files, and the
// modules that the binary links. Two of them are licence rules, not preferences: the project
// publishes an image under the AGPL-3.0, and Orchard carries a licence with a field-of-use
// restriction, so no Orchard code may enter the binary (ADR-0010). The go.mod and tree rules
// are T-122.
//
// The go command parses go.mod and lists the modules, so the tests need no parser of their own
// and no module that the binary does not already require.

// root is the root of the repository, from the directory of this package.
const root = "../.."

// The modules of the Orchard repository, under its old owner and its new one.
var orchardModules = []string{"github.com/cirruslabs/orchard", "github.com/openai/orchard"}

const stacksAPI = "github.com/buildkite/stacksapi"

// minimumStacksAPI is the first version of the Stacks API client that the design accepts.
var minimumStacksAPI = [3]int{1, 2, 0}

// The text is in two parts, so that the reuse tool does not read it as a header of this file.
const spdxLine = "// SPDX-License-" + "Identifier: AGPL-3.0-or-later"

// goMod is the part of the output of go mod edit -json that the rules read.
type goMod struct {
	Module  modulePath
	Go      string
	Require []modulePath
	Exclude []modulePath
	Replace []struct{ Old, New modulePath }
	Tool    []modulePath
}

type modulePath struct {
	Path    string
	Version string
}

// listedPackage is the part of the output of go list that the NOTICE rule reads.
type listedPackage struct {
	ImportPath string
	Module     *modulePath
}

func TestGoModFollowsTheDependencyRules(t *testing.T) {
	mod := parseGoMod(t, filepath.Join(root, "go.mod"))

	for _, problem := range goModErrors(mod, statedGoVersion(t)) {
		t.Error(problem)
	}
}

func TestTheTreeHoldsNoFileOfTheOrchardRepository(t *testing.T) {
	for _, problem := range orchardFileErrors(trackedFiles(t)) {
		t.Error(problem)
	}
}

func TestNoticeNamesEachModuleThatTheBinaryLinks(t *testing.T) {
	notice, err := os.ReadFile(filepath.Join(root, "NOTICE"))
	if err != nil {
		t.Fatal(err)
	}

	mod := parseGoMod(t, filepath.Join(root, "go.mod"))

	modules := linkedModules(listBinaryPackages(t), mod.Module.Path)
	if len(modules) == 0 {
		t.Fatal("go list names no module that the controller binary links, so this test " +
			"cannot check NOTICE")
	}

	for _, problem := range noticeErrors(modules, string(notice)) {
		t.Error(problem)
	}
}

func TestEachGoFileStartsWithTheSPDXLine(t *testing.T) {
	checked := 0

	for _, name := range trackedFiles(t) {
		if !strings.HasSuffix(name, ".go") {
			continue
		}

		text, err := os.ReadFile(filepath.Join(root, name)) //nolint:gosec // a file that git tracks
		if errors.Is(err, os.ErrNotExist) {
			// A tracked file that the working tree has deleted is not code any more.
			continue
		} else if err != nil {
			t.Fatal(err)
		}

		checked++

		for _, problem := range spdxErrors(name, string(text)) {
			t.Error(problem)
		}
	}

	if checked == 0 {
		t.Fatal("git lists no Go file, so this test checks nothing")
	}
}

// statedGoVersion is the Go version that the target design states.
func statedGoVersion(t *testing.T) string {
	t.Helper()

	design, err := os.ReadFile(filepath.Join(root, "docs/architecture/target-design.md"))
	if err != nil {
		t.Fatal(err)
	}

	match := regexp.MustCompile(`(?m)^\* \*\*Go version:\*\* ([0-9.]+)\.$`).FindSubmatch(design)
	if match == nil {
		t.Fatal(`the target design no longer states a Go version as "* **Go version:** N."`)
	}

	return string(match[1])
}

// parseGoMod has the go command parse a go.mod file.
func parseGoMod(t *testing.T, name string) goMod {
	t.Helper()

	// The go command reads the file in a process of its own, which the test cache does not see.
	// Reading it here as well makes a change of go.mod run the test again.
	if _, err := os.ReadFile(name); err != nil { //nolint:gosec // the go.mod that the test names
		t.Fatal(err)
	}

	output := run(t, ".", "go", "mod", "edit", "-json", name)

	var mod goMod
	if err := json.Unmarshal(output, &mod); err != nil {
		t.Fatalf("reading the output of go mod edit -json: %v", err)
	}

	return mod
}

// listBinaryPackages lists each package that the controller binary links.
func listBinaryPackages(t *testing.T) []listedPackage {
	t.Helper()

	output := run(t, root, "go", "list", "-deps", "-json=ImportPath,Module", "./cmd/...")

	var packages []listedPackage

	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("reading the output of go list: %v", err)
		}

		packages = append(packages, pkg)
	}

	return packages
}

// trackedFiles gives the files that git tracks, as paths from the root with forward slashes.
//
// It also opens each directory that holds one. The test cache sees what a test opens, but not
// what git reads, so a new file would otherwise leave a cached pass in place.
func trackedFiles(t *testing.T) []string {
	t.Helper()

	output := run(t, root, "git", "ls-files", "-z")

	names := strings.Split(strings.TrimRight(string(output), "\x00"), "\x00")
	if len(names) == 0 || names[0] == "" {
		t.Fatal("git lists no file")
	}

	directories := map[string]bool{}
	for _, name := range names {
		directories[path.Dir(name)] = true
	}

	for directory := range directories {
		// A directory that the working tree has deleted holds nothing to check.
		if _, err := os.ReadDir(filepath.Join(root, directory)); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}

	return names
}

// run runs a command in a directory and gives its standard output.
func run(t *testing.T, dir, name string, args ...string) []byte {
	t.Helper()

	//nolint:gosec // G204: the tests run go and git, with arguments that they fix.
	command := exec.CommandContext(t.Context(), name, args...)
	command.Dir = dir

	var stderr bytes.Buffer

	command.Stderr = &stderr

	output, err := command.Output()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, stderr.String())
	}

	return output
}

// goModErrors applies the rules of go.mod: no Orchard module, no replace directive, the Go
// version of the design, and a Stacks API client that is recent enough.
func goModErrors(mod goMod, statedGo string) []string {
	var problems []string

	paths := []string{}
	for _, list := range [][]modulePath{mod.Require, mod.Exclude, mod.Tool} {
		for _, module := range list {
			paths = append(paths, module.Path)
		}
	}

	for _, replace := range mod.Replace {
		paths = append(paths, replace.Old.Path, replace.New.Path)
	}

	for _, orchard := range orchardModules {
		if slices.ContainsFunc(paths, func(p string) bool {
			return p == orchard || strings.HasPrefix(p, orchard+"/")
		}) {
			problems = append(problems, fmt.Sprintf("go.mod names %s. The controller calls the "+
				"Orchard REST API with its own client, because the licences do not combine "+
				"(ADR-0010)", orchard))
		}
	}

	for _, replace := range mod.Replace {
		problems = append(problems, fmt.Sprintf("go.mod has a replace directive: %s => %s. "+
			"Remove it, so that each build takes the module that go.sum names",
			strings.TrimSpace(replace.Old.Path+" "+replace.Old.Version),
			strings.TrimSpace(replace.New.Path+" "+replace.New.Version)))
	}

	switch {
	case mod.Go == "":
		problems = append(problems, "go.mod declares no Go version")
	case mod.Go != statedGo && !strings.HasPrefix(mod.Go, statedGo+"."):
		problems = append(problems, fmt.Sprintf("go.mod declares Go %s, the documents state %s",
			mod.Go, statedGo))
	}

	for _, module := range mod.Require {
		if module.Path != stacksAPI {
			continue
		}

		found, ok := parseVersion(module.Version)
		if !ok {
			problems = append(problems, fmt.Sprintf("stacksapi is %s, which is not a version of "+
				"the form vX.Y.Z", module.Version))
		} else if slices.Compare(found[:], minimumStacksAPI[:]) < 0 {
			problems = append(problems, fmt.Sprintf("stacksapi is %s, the design states "+
				"v%d.%d.%d or later", module.Version, minimumStacksAPI[0], minimumStacksAPI[1],
				minimumStacksAPI[2]))
		}
	}

	return problems
}

// parseVersion reads the three numbers of a module version, without a pre-release or build part.
func parseVersion(version string) ([3]int, bool) {
	var numbers [3]int

	core, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), "-")
	core, _, _ = strings.Cut(core, "+")

	parts := strings.Split(core, ".")
	if !strings.HasPrefix(version, "v") || len(parts) != len(numbers) {
		return numbers, false
	}

	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil {
			return numbers, false
		}

		numbers[i] = number
	}

	return numbers, true
}

// orchardFileErrors finds a tracked file that has the name of a file of the Orchard repository.
// The documents may hold such a name, for example the OpenAPI document of Orchard that the
// generator reads.
func orchardFileErrors(names []string) []string {
	var problems []string

	for _, name := range names {
		base := path.Base(name)
		top, _, _ := strings.Cut(name, "/")

		if (base == "orchard.go" || base == "openapi.yaml") && top != "docs" {
			problems = append(problems, fmt.Sprintf("%s looks like a file of the Orchard "+
				"repository, and no Orchard code may enter this repository (ADR-0010)", name))
		}
	}

	return problems
}

// linkedModules gives the sorted, unique module paths of the listed packages, without this
// module. A package of the standard library has no module.
func linkedModules(packages []listedPackage, own string) []string {
	var modules []string

	for _, pkg := range packages {
		if pkg.Module != nil && pkg.Module.Path != "" && pkg.Module.Path != own {
			modules = append(modules, pkg.Module.Path)
		}
	}

	slices.Sort(modules)

	return slices.Compact(modules)
}

// noticeErrors applies the rule of NOTICE: it names each linked module, the Go standard library,
// and the NOTICE text of gRPC.
func noticeErrors(modules []string, notice string) []string {
	var problems []string

	for _, module := range modules {
		if !namesModule(notice, module) {
			problems = append(problems, fmt.Sprintf("NOTICE does not name %s, which the "+
				"controller binary links. Add it under its licence, and copy NOTICE to the "+
				"chart, as docs/development.md says", module))
		}
	}

	if !strings.Contains(notice, "Go standard library") {
		problems = append(problems,
			"NOTICE does not name the Go standard library, which the binary links")
	}

	if slices.Contains(modules, "google.golang.org/grpc") &&
		!strings.Contains(notice, "Copyright 2014 gRPC authors") {
		problems = append(problems,
			"NOTICE lacks the NOTICE text of gRPC, which Apache-2.0 section 4(d) needs")
	}

	return problems
}

// namesModule reports whether the text holds the module path as a path of its own. A longer
// path (go.opentelemetry.io/otel/sdk) does not name a shorter module (go.opentelemetry.io/otel),
// and the other way round, but a full stop that ends a sentence may follow the path.
func namesModule(text, module string) bool {
	for start := 0; ; start++ {
		found := strings.Index(text[start:], module)
		if found < 0 {
			return false
		}

		start += found
		end := start + len(module)

		before := start == 0 || !isPathByte(text[start-1])
		after := end == len(text) || !isWordByte(text[end]) && text[end] != '-' &&
			!startsPath(text[end:])

		if before && after {
			return true
		}
	}
}

// startsPath reports whether the text starts with a separator that a path continues after.
func startsPath(text string) bool {
	return len(text) > 1 && (text[0] == '/' || text[0] == '.') && isWordByte(text[1])
}

func isWordByte(b byte) bool {
	return b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

func isPathByte(b byte) bool {
	return isWordByte(b) || b == '.' || b == '/' || b == '-'
}

// spdxErrors applies the rule of the licence line: it is the first line of a Go file. A
// generator writes the line into a generated file too.
func spdxErrors(name, text string) []string {
	first, _, _ := strings.Cut(text, "\n")
	if strings.TrimSuffix(first, "\r") == spdxLine {
		return nil
	}

	return []string{fmt.Sprintf("%s lacks the line %q as its first line", name, spdxLine)}
}
