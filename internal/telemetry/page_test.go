// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package telemetry_test

import (
	"go/constant"
	"go/types"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/repocheck"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

const telemetryPage = "../../docs/user/telemetry.md"

// dottedName is the shape of an attribute key or a log field key: lower-case words joined by
// full stops, such as bk_agent_stack_orchard.vm.phase or cicd.pipeline.name. Another string
// constant of the package, such as the name of an exporter, has no full stop.
var dottedName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// The telemetry page is how an operator learns what to query, so a name that the code adds and
// the page lacks is a name that nobody can find. Each group of names comes from the code itself,
// and each group must be found, so a refactor that moves the names cannot make this test pass by
// finding none.
func TestTheTelemetryPageNamesEachNameOfTheCode(t *testing.T) {
	page, err := os.ReadFile(telemetryPage)
	if err != nil {
		t.Fatal(err)
	}

	keys, values := constants(t)

	groups := []struct {
		what  string
		names []string
	}{
		{"instrument", instruments(t)},
		{"attribute or log field key", keys},
		{"attribute value", values},
	}

	for _, group := range groups {
		if len(group.names) == 0 {
			t.Errorf("the test found no %s in the code. It reads them from the code, so the "+
				"code has changed shape: change the test so that it finds them again", group.what)
		}

		for _, name := range group.names {
			if !strings.Contains(string(page), "`"+name+"`") {
				t.Errorf("docs/user/telemetry.md does not name `%s`, which the code defines as "+
					"an %s. Add it to the page, so that an operator can find it", name, group.what)
			}
		}
	}
}

// instruments gives the name of each instrument that NewMetrics builds. The SDK applies its
// views when an instrument is built, so a view that records each name and drops the stream sees
// every instrument, of any kind, with no list of them here.
func instruments(t *testing.T) []string {
	t.Helper()

	var names []string

	record := func(instrument metric.Instrument) (metric.Stream, bool) {
		names = append(names, instrument.Name)

		return metric.Stream{}, false
	}

	provider := metric.NewMeterProvider(
		metric.WithReader(metric.NewManualReader()), metric.WithView(record))
	t.Cleanup(func() {
		if err := provider.Shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	})

	if _, err := telemetry.NewMetrics(provider.Meter("test")); err != nil {
		t.Fatal(err)
	}

	slices.Sort(names)

	return slices.Compact(names)
}

// constants gives the string constants of the package that an operator sees: the keys of the
// attributes and of the log fields, and the values of the exported string types, which are the
// values of the attributes.
func constants(t *testing.T) (keys, values []string) {
	t.Helper()

	pkg, err := repocheck.Load(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}

	scope := pkg.Types.Scope()

	for _, name := range scope.Names() {
		object, ok := scope.Lookup(name).(*types.Const)
		if !ok || object.Val().Kind() != constant.String {
			continue
		}

		text := constant.StringVal(object.Val())

		if named, ok := object.Type().(*types.Named); ok {
			// An unexported type, such as the signal of an exporter variable, names no value
			// of an attribute.
			if named.Obj().Pkg() == pkg.Types && named.Obj().Exported() {
				values = append(values, text)
			}

			continue
		}

		if dottedName.MatchString(text) {
			keys = append(keys, text)
		}
	}

	slices.Sort(keys)
	slices.Sort(values)

	return slices.Compact(keys), slices.Compact(values)
}
