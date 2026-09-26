package telemetry_test

import (
	"context"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

const sample = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d")

// recorder gives a tracer whose spans a test can read. It uses the tracer provider as it comes,
// because the trace of a job is a property of the context and not of the provider.
func recorder(t *testing.T) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))

	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() = %v, want no error", err)
		}
	})

	return provider, exporter
}

// A person who reads a job page has the UUID and nothing else. The trace must be findable from
// it, which means that the trace identifier is those digits and not a mapping that a person
// cannot compute.
func TestTraceIDIsTheDigitsOfTheJobUUID(t *testing.T) {
	t.Parallel()

	traceID, err := telemetry.TraceID(sample)
	if err != nil {
		t.Fatalf("TraceID() = %v, want no error", err)
	}

	want := strings.ReplaceAll(string(sample), "-", "")
	if traceID.String() != want {
		t.Errorf("TraceID() = %s, want %s", traceID, want)
	}

	// 128 bits, which is what a trace identifier holds and what a UUID holds.
	if len(traceID) != 16 {
		t.Errorf("the trace identifier has %d bytes, want 16", len(traceID))
	}
}

func TestTraceIDRefusesTextThatIsNotAJob(t *testing.T) {
	t.Parallel()

	for name, id := range map[string]job.ID{
		"empty":             "",
		"a virtual machine": "bk-macos-prod-018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d",
		"too short":         "018f3b2c-1c4a-7e5b-9a3d",
		"not hexadecimal":   "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6azz",
		"only zeros":        "00000000-0000-0000-0000-000000000000",
	} {
		if _, err := telemetry.TraceID(id); err == nil {
			t.Errorf("TraceID(%s) = nil, want an error", name)
		}
	}
}

// The controller sees one job across many ticks and across a restart, so no span of this process
// can be the parent of all of them. The identifier of the job carries the trace instead.
func TestASpanOfAJobCarriesTheTraceOfThatJob(t *testing.T) {
	t.Parallel()

	provider, exporter := recorder(t)

	_, span := provider.Tracer("test").Start(telemetry.WithJob(t.Context(), sample),
		"stacks.reserve")
	span.End()

	recorded := exporter.GetSpans()
	if len(recorded) != 1 {
		t.Fatalf("the exporter holds %d spans, want 1", len(recorded))
	}

	want, err := telemetry.TraceID(sample)
	if err != nil {
		t.Fatalf("TraceID() = %v, want no error", err)
	}

	if recorded[0].SpanContext.TraceID() != want {
		t.Errorf("the span is in trace %s, want the trace of the job, %s",
			recorded[0].SpanContext.TraceID(), want)
	}

	// The collector never sees a span that the sampler dropped, and a job trace that stops at a
	// restart is worth nothing.
	if !recorded[0].SpanContext.IsSampled() {
		t.Error("the span is not sampled, want a sampled span")
	}
}

// A tick is a short trace of its own, so the link from the tick to the job is the only way from a
// slow tick to the jobs that it touched (target design, section 10.2).
func TestAJobSpanIsInTheJobTraceAndTheTickLinksToIt(t *testing.T) {
	t.Parallel()

	provider, exporter := recorder(t)
	tracer := provider.Tracer("test")

	ctx, tick := tracer.Start(t.Context(), "tick.reap")
	_, span := telemetry.StartJob(ctx, tracer, sample, "reaper.decide")
	span.End()
	tick.End()

	want, err := telemetry.TraceID(sample)
	if err != nil {
		t.Fatalf("TraceID() = %v, want no error", err)
	}

	recorded := exporter.GetSpans()
	if len(recorded) != 2 {
		t.Fatalf("the exporter holds %d spans, want the job span and the tick", len(recorded))
	}

	job, ticked := recorded[0], recorded[1]

	if job.SpanContext.TraceID() != want {
		t.Errorf("the job span is in trace %s, want the trace of the job, %s",
			job.SpanContext.TraceID(), want)
	}

	if ticked.SpanContext.TraceID() == want {
		t.Error("the tick is in the trace of the job, want a trace of its own")
	}

	if len(ticked.Links) != 1 || ticked.Links[0].SpanContext.SpanID() != job.SpanContext.SpanID() {
		t.Errorf("the tick links to %+v, want the span of the job", ticked.Links)
	}
}

// Two processes, which is what a restart makes, must put their spans in one trace and must name
// one parent, so that the spans of a job meet in one tree.
func TestTwoProcessesAgreeOnTheTraceAndTheParent(t *testing.T) {
	t.Parallel()

	traces, parents := map[string]bool{}, map[string]bool{}

	for range 2 {
		provider, exporter := recorder(t)

		_, span := provider.Tracer("test").Start(telemetry.WithJob(t.Context(), sample),
			"reaper.decide")
		span.End()

		recorded := exporter.GetSpans()[0]
		traces[recorded.SpanContext.TraceID().String()] = true
		parents[recorded.Parent.SpanID().String()] = true
	}

	if len(traces) != 1 {
		t.Errorf("the two processes wrote %d traces, want 1", len(traces))
	}

	if len(parents) != 1 {
		t.Errorf("the two processes named %d parents, want 1", len(parents))
	}
}

// A span inside a span belongs to its parent, so one job holds one tree and not many roots.
func TestASpanInsideASpanKeepsTheTrace(t *testing.T) {
	t.Parallel()

	provider, exporter := recorder(t)
	tracer := provider.Tracer("test")

	ctx, outer := tracer.Start(telemetry.WithJob(t.Context(), sample), "job.admit")
	_, inner := tracer.Start(ctx, "orchard.vm.create")
	inner.End()
	outer.End()

	recorded := exporter.GetSpans()
	if len(recorded) != 2 {
		t.Fatalf("the exporter holds %d spans, want 2", len(recorded))
	}

	if recorded[0].SpanContext.TraceID() != recorded[1].SpanContext.TraceID() {
		t.Error("the two spans are in two traces, want one trace")
	}

	if recorded[0].Parent.SpanID() != recorded[1].SpanContext.SpanID() {
		t.Error("the inner span is not a child of the outer span")
	}
}

// A tick is not the work of one job, and its spans must not land in the trace of a job. They get
// a trace of their own, and each one is different.
func TestWorkThatIsNotAJobGetsATraceOfItsOwn(t *testing.T) {
	t.Parallel()

	provider, exporter := recorder(t)
	tracer := provider.Tracer("test")

	for _, name := range []string{"tick.poll", "tick.reap"} {
		_, span := tracer.Start(t.Context(), name)
		span.End()
	}

	recorded := exporter.GetSpans()

	first, second := recorded[0].SpanContext.TraceID(), recorded[1].SpanContext.TraceID()
	if !first.IsValid() || !second.IsValid() {
		t.Fatalf("the ticks got the trace identifiers %s and %s, want valid ones", first, second)
	}

	if first == second {
		t.Error("the two ticks are in one trace, want a trace for each of them")
	}
}

// Text that is not a job is a defect of the caller. A span that is lost would hide the defect,
// so the span exists and it carries a trace of its own.
func TestTextThatIsNotAJobStillGivesASpan(t *testing.T) {
	t.Parallel()

	provider, exporter := recorder(t)

	_, span := provider.Tracer("test").Start(telemetry.WithJob(t.Context(), "not-a-job"),
		"job.match")
	span.End()

	if got := exporter.GetSpans()[0].SpanContext.TraceID(); !got.IsValid() {
		t.Errorf("the span got the trace identifier %s, want a valid one", got)
	}
}

// The traceparent goes into the environment of the VM, so that the spans of the agent join the
// trace of the job.
func TestTraceParentCarriesTheTraceOfTheJob(t *testing.T) {
	t.Parallel()

	provider, _ := recorder(t)

	ctx, span := provider.Tracer("test").Start(telemetry.WithJob(t.Context(), sample),
		"orchard.vm.create")
	defer span.End()

	parent := telemetry.TraceParent(ctx)

	want, err := telemetry.TraceID(sample)
	if err != nil {
		t.Fatalf("TraceID() = %v, want no error", err)
	}

	if !strings.Contains(parent, want.String()) {
		t.Errorf("TraceParent() = %q, want the trace of the job, %s", parent, want)
	}

	if !strings.HasPrefix(parent, "00-") {
		t.Errorf("TraceParent() = %q, want the W3C version prefix 00-", parent)
	}
}

// No span means no trace to join, and an empty value means that the caller sends none.
func TestTraceParentIsEmptyWithoutASpan(t *testing.T) {
	t.Parallel()

	if parent := telemetry.TraceParent(t.Context()); parent != "" {
		t.Errorf("TraceParent() = %q, want empty text", parent)
	}
}
