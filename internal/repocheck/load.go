// SPDX-License-Identifier: AGPL-3.0-or-later

// Package repocheck reads the Go code of this repository as the compiler does, for the tests that
// keep the documents and the licence files in step with the code.
//
// A regular expression over Go source finds fewer names when the code changes shape, and a check
// that finds fewer names passes. The type checker finds each constant wherever it is declared and
// whatever its expression is, so a test that reads its result fails when a name goes missing from
// a document and not when the code is refactored.
//
// Only tests import this package, so the controller binary does not link it.
package repocheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Package is one package of this module, type-checked without its tests.
type Package struct {
	// Types holds the objects of the package, such as its constants and their values.
	Types *types.Package

	// Info holds the type and the constant value of each expression of the files.
	Info *types.Info

	// Files is the syntax of each file, with its comments.
	Files []*ast.File

	// Fset holds the positions of the files.
	Fset *token.FileSet
}

// listed is the part of the output of go list that Load reads.
type listed struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Export     string
	DepOnly    bool
	Error      *struct{ Err string }
}

// Load type-checks the package in dir without its tests.
//
// The go command builds the export data of each import, and the type checker reads it. A
// constant that takes its value from another package, such as a key of the semantic conventions,
// therefore has its real value, and is not lost because an import did not resolve.
func Load(ctx context.Context, dir string) (*Package, error) {
	command := exec.CommandContext(ctx, "go", "list", "-export", "-deps",
		"-json=ImportPath,Dir,GoFiles,Export,DepOnly,Error", ".")
	command.Dir = dir

	var stderr bytes.Buffer

	command.Stderr = &stderr

	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("go list cannot build the export data of %s: %w: %s", dir, err,
			stderr.String())
	}

	exports := map[string]string{}

	var target *listed

	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var pkg listed
		if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("reading the output of go list: %w", err)
		}

		if pkg.Error != nil {
			return nil, fmt.Errorf("go list cannot load %s: %s", pkg.ImportPath, pkg.Error.Err)
		}

		exports[pkg.ImportPath] = pkg.Export

		if !pkg.DepOnly {
			target = &pkg
		}
	}

	if target == nil {
		return nil, fmt.Errorf("go list names no package in %s", dir)
	}

	return check(target, exports)
}

// check parses the files of the target and type-checks them against the export data.
func check(target *listed, exports map[string]string) (*Package, error) {
	fset := token.NewFileSet()

	files := make([]*ast.File, 0, len(target.GoFiles))

	for _, name := range target.GoFiles {
		file, err := parser.ParseFile(fset, filepath.Join(target.Dir, name), nil,
			parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}

		files = append(files, file)
	}

	lookup := func(path string) (io.ReadCloser, error) {
		export, ok := exports[path]
		if !ok || export == "" {
			return nil, fmt.Errorf("go list gave no export data for %s", path)
		}

		return os.Open(export) //nolint:gosec // a path of the build cache that go list gave
	}

	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
	}

	config := types.Config{Importer: importer.ForCompiler(fset, "gc", lookup)}

	pkg, err := config.Check(target.ImportPath, fset, files, info)
	if err != nil {
		return nil, fmt.Errorf("type-checking %s: %w", target.ImportPath, err)
	}

	return &Package{Types: pkg, Info: info, Files: files, Fset: fset}, nil
}
