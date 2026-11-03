// SPDX-License-Identifier: AGPL-3.0-or-later

package repocheck_test

import (
	"go/constant"
	"go/types"
	"os"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/repocheck"
)

// A constant has the value that the compiler gives it, whether its expression joins constants of
// the package or reads a constant of an import.
func TestLoadGivesTheValueOfEachConstant(t *testing.T) {
	pkg, err := repocheck.Load(t.Context(), "testdata/sample")
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{
		"Joined":    "sample.name",
		"Separator": string(os.PathSeparator),
	} {
		object, ok := pkg.Types.Scope().Lookup(name).(*types.Const)
		if !ok {
			t.Errorf("%s is not a constant of the package", name)

			continue
		}

		if got := constant.StringVal(object.Val()); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestLoadFailsOutsideTheModule(t *testing.T) {
	if _, err := repocheck.Load(t.Context(), t.TempDir()); err == nil {
		t.Fatal("Load gave no error for a directory outside the module")
	}
}
