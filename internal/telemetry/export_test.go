// The export tests are inside the package, because the rule that they prove is the choice of an
// exporter, and that choice is what the package does not let an environment variable change.
package telemetry

import "testing"

// The specification gives each signal its own variable, and a general one behind it. An operator
// who sends the metrics over gRPC and the traces over HTTP depends on that order.
func TestTheProtocolOfASignalComesFromItsOwnVariableFirst(t *testing.T) {
	for name, test := range map[string]struct {
		general string
		signal  string
		want    string
	}{
		"nothing gives the default of the specification": {"", "", protocolHTTP},
		"the general variable":                           {protocolGRPC, "", protocolGRPC},
		"the variable of the signal":                     {"", protocolGRPC, protocolGRPC},
		"the signal wins over the general variable":      {protocolGRPC, protocolHTTP, protocolHTTP},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", test.general)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", test.signal)

			got, err := protocol(traces)
			if err != nil {
				t.Fatalf("protocol() = %v, want no error", err)
			}

			if got != test.want {
				t.Errorf("protocol() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAProtocolThatOTLPDoesNotHaveIsAnError(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")

	if _, err := protocol(metrics); err == nil {
		t.Error("protocol() = nil, want an error for a protocol that these exporters do not have")
	}
}

// Every signal takes the same two answers, and no signal may take a third.
func TestOnlyOTLPAndNoneAreExports(t *testing.T) {
	for _, of := range []signal{traces, metrics, logs} {
		variable := "OTEL_" + string(of) + "_EXPORTER"

		for value, want := range map[string]bool{"": true, exporterOTLP: true, exporterNone: false} {
			t.Setenv(variable, value)

			got, err := wanted(of)
			if err != nil {
				t.Fatalf("wanted(%s) with %s=%q = %v, want no error", of, variable, value, err)
			}

			if got != want {
				t.Errorf("wanted(%s) with %s=%q = %v, want %v", of, variable, value, got, want)
			}
		}

		t.Setenv(variable, "prometheus")

		if _, err := wanted(of); err == nil {
			t.Errorf("wanted(%s) with %s=prometheus = nil, want an error, because the design "+
				"gives this controller no Prometheus endpoint", of, variable)
		}
	}
}
