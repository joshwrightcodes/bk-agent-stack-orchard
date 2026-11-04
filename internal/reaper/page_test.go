// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package reaper_test

import (
	"go/ast"
	"go/constant"
	"go/types"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/reaper"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/repocheck"
)

// The reason of a deletion is the value of the deletion metric, and an operator learns the values
// from the telemetry page. The reasons come from each place where the code sets the reason of a
// decision, and at least one must be found, so a refactor cannot make this test pass by finding
// none.
func TestTheTelemetryPageNamesEachDeletionReason(t *testing.T) {
	page, err := os.ReadFile("../../docs/user/telemetry.md")
	if err != nil {
		t.Fatal(err)
	}

	reasons := deletionReasons(t)
	if len(reasons) == 0 {
		t.Fatal("the test found no reason of a decision in the code. It reads the reasons from " +
			"the code, so the code has changed shape: change the test so that it finds them again")
	}

	for _, reason := range reasons {
		if !strings.Contains(string(page), "`"+reason+"`") {
			t.Errorf("docs/user/telemetry.md does not name `%s`, which the code gives as the "+
				"reason of a deletion. Add it to the page, so that an operator can find it", reason)
		}
	}
}

// deletionReasons gives each constant that the package writes to the Reason field of a Decision,
// in a composite literal or in an assignment.
func deletionReasons(t *testing.T) []string {
	t.Helper()

	pkg, err := repocheck.Load(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}

	field := reasonField(t, pkg.Types)

	var reasons []string

	add := func(value ast.Expr) {
		known := pkg.Info.Types[value].Value
		if known == nil || known.Kind() != constant.String {
			t.Errorf("%s: the reason of a decision is not a string constant, so this test "+
				"cannot check that the telemetry page names it", pkg.Fset.Position(value.Pos()))

			return
		}

		if reason := constant.StringVal(known); reason != "" {
			reasons = append(reasons, reason)
		}
	}

	for _, file := range pkg.Files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.KeyValueExpr:
				if key, ok := node.Key.(*ast.Ident); ok && pkg.Info.Uses[key] == field {
					add(node.Value)
				}
			case *ast.AssignStmt:
				for i, target := range node.Lhs {
					selector, ok := target.(*ast.SelectorExpr)
					if ok && pkg.Info.Uses[selector.Sel] == field && len(node.Rhs) == len(node.Lhs) {
						add(node.Rhs[i])
					}
				}
			}

			return true
		})
	}

	slices.Sort(reasons)

	return slices.Compact(reasons)
}

// reasonField finds the Reason field of Decision in the type-checked package.
func reasonField(t *testing.T, pkg *types.Package) *types.Var {
	t.Helper()

	name := reflect.TypeFor[reaper.Decision]().Name()

	decision, ok := pkg.Scope().Lookup(name).Type().Underlying().(*types.Struct)
	if !ok {
		t.Fatalf("%s is not a struct", name)
	}

	for field := range decision.Fields() {
		if field.Name() == "Reason" {
			return field
		}
	}

	t.Fatalf("%s has no Reason field. Change this test to read the field that holds the "+
		"reason of a deletion", name)

	return nil
}
