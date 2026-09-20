package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// quiet stops the exporters, because a test has no collector. The providers still build, and
// the records still reach the JSON destination, which is what these tests read.
//
// The variables are the ones of the OpenTelemetry specification, which is the point: an operator
// changes the export with them, and this package holds no settings of its own for it.
func quiet(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER",
	} {
		t.Setenv(name, "none")
	}
}

// body gives the message of a record, which the data model calls the body.
func body(record map[string]any) string {
	value, ok := record["Body"].(map[string]any)
	if !ok {
		return ""
	}

	text, _ := value["Value"].(string)

	return text
}

// attributeOf gives one attribute of a record. The data model writes them as a list of pairs,
// where each value carries its own type.
func attributeOf(record map[string]any, key string) string {
	pairs, ok := record["Attributes"].([]any)
	if !ok {
		return ""
	}

	for _, one := range pairs {
		pair, ok := one.(map[string]any)
		if !ok || pair["Key"] != key {
			continue
		}

		value, ok := pair["Value"].(map[string]any)
		if !ok {
			return ""
		}

		text, _ := value["Value"].(string)

		return text
	}

	return ""
}

// records reads the JSON log records that a buffer holds.
func records(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()

	var found []map[string]any

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}

		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("the line %q is not JSON: %v", line, err)
		}

		found = append(found, record)
	}

	return found
}

// An operator reads stdout when the collector is the thing that failed, so the records must be
// in both places and not in one.
func TestSetupWritesTheRecordsToStdoutAsJSON(t *testing.T) {
	quiet(t)

	var out bytes.Buffer

	started, err := telemetry.Setup(t.Context(), telemetry.Options{
		ServiceName:  "bk-agent-stack-orchard",
		Version:      "1.2.3",
		ControllerID: "bk-macos-prod",
		Output:       &out,
	})
	if err != nil {
		t.Fatalf("Setup() = %v, want the providers", err)
	}

	t.Cleanup(func() {
		if err := started.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() = %v, want no error", err)
		}
	})

	started.Logger.Info("the controller started", telemetry.LogProfile, "xcode-27")

	found := records(t, &out)
	if len(found) != 1 {
		t.Fatalf("stdout holds %d records, want 1", len(found))
	}

	if body(found[0]) != "the controller started" ||
		attributeOf(found[0], telemetry.LogProfile) != "xcode-27" {
		t.Errorf("the record read as %v, want the message and the profile", found[0])
	}
}

// A record that a person finds in a log is the start of a search. Without the trace, that search
// stops there.
func TestARecordInsideASpanCarriesTheTrace(t *testing.T) {
	quiet(t)

	var out bytes.Buffer

	tel, err := telemetry.Setup(t.Context(), telemetry.Options{Output: &out})
	if err != nil {
		t.Fatalf("Setup() = %v, want the providers", err)
	}

	t.Cleanup(func() {
		if err := tel.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() = %v, want no error", err)
		}
	})

	tel.Logger.Info("before the span")

	ctx, span := tel.Tracer.Start(telemetry.WithJob(t.Context(), sample), "orchard.vm.create")
	tel.Logger.InfoContext(ctx, "the virtual machine exists")
	span.End()

	found := records(t, &out)
	if len(found) != 2 {
		t.Fatalf("stdout holds %d records, want 2", len(found))
	}

	// A record with no span carries the trace of only zeros, which is how the data model says
	// that there is none.
	if found[0]["TraceID"] != strings.Repeat("0", 32) {
		t.Errorf("the record outside a span carries the trace %v, want one of only zeros",
			found[0]["TraceID"])
	}

	// The trace of the record is the trace of the job, which is how a person goes from a log
	// line to the whole life of that job.
	want, err := telemetry.TraceID(sample)
	if err != nil {
		t.Fatalf("TraceID() = %v, want no error", err)
	}

	if found[1]["TraceID"] != want.String() {
		t.Errorf("the record carries the trace %v, want %s", found[1]["TraceID"], want)
	}

	if found[1]["SpanID"] == nil || found[1]["SpanID"] == "" {
		t.Error("the record carries no span, want the span that was active")
	}
}

// A level that the options set must stop the records below it, in both destinations.
func TestTheLoggerObeysTheLevel(t *testing.T) {
	quiet(t)

	var out bytes.Buffer

	started, err := telemetry.Setup(t.Context(), telemetry.Options{
		Output: &out,
		Level:  slog.LevelWarn,
	})
	if err != nil {
		t.Fatalf("Setup() = %v, want the providers", err)
	}

	t.Cleanup(func() { _ = started.Shutdown(context.Background()) })

	started.Logger.Info("a detail")
	started.Logger.Warn("something to read")

	found := records(t, &out)
	if len(found) != 1 || body(found[0]) != "something to read" {
		t.Errorf("stdout holds %v, want only the warning", found)
	}
}

// The design states that this controller has no Prometheus endpoint. A rule that an environment
// variable can break is not a rule, so a variable that asks for another export stops the process
// at its start, and not at the first tick, when jobs already wait.
func TestSetupRefusesAnExportThatIsNotOTLP(t *testing.T) {
	for name, value := range map[string]string{
		"prometheus":          "prometheus",
		"console":             "console",
		"one that is unknown": "an-exporter-that-does-not-exist",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OTEL_METRICS_EXPORTER", value)

			_, err := telemetry.Setup(t.Context(), telemetry.Options{})
			if err == nil {
				t.Fatalf("Setup() with the exporter %q = nil, want an error", value)
			}

			// The message must say what the controller does export, because the operator who
			// reads it set the variable on purpose.
			if !strings.Contains(err.Error(), "otlp") {
				t.Errorf("Setup() = %v, want a message that names OTLP", err)
			}
		})
	}
}

func TestSetupRefusesAProtocolThatOTLPDoesNotHave(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "carrier-pigeon")

	if _, err := telemetry.Setup(t.Context(), telemetry.Options{}); err == nil {
		t.Error("Setup() = nil, want an error for a protocol that OTLP does not have")
	}
}

// The records of the last seconds of a process are the interesting ones, so Shutdown must send
// what the exporters hold. A second call must not fail, because a process that stops twice, on a
// signal and on a defer, is ordinary.
func TestShutdownIsSafeTwice(t *testing.T) {
	quiet(t)

	started, err := telemetry.Setup(t.Context(), telemetry.Options{Output: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("Setup() = %v, want the providers", err)
	}

	for attempt := range 2 {
		if err := started.Shutdown(t.Context()); err != nil {
			t.Errorf("Shutdown() on attempt %d = %v, want no error", attempt+1, err)
		}
	}
}
