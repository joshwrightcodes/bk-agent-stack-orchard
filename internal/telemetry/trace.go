package telemetry

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
)

// WithJob puts the work of this context in the trace of one Buildkite job. A span that starts
// from the context, and every span under it, is then in that trace.
//
// The controller sees one job across many ticks, and across a restart, so no span of this
// process can be the parent of all of them. The identifier of the job does that work: the
// context carries a parent that this process did not write, whose trace is the job and whose
// span comes from the same digits. Every process therefore names the same parent, and the spans
// of a job meet in one trace without one span that outlives them.
//
// Call it once, where the work of a job begins. A second call inside a span replaces that span
// as the parent, and the spans under it would leave the tree.
//
// Text that is not a job identifier leaves the context as it was, so the spans keep a trace of
// their own instead of vanishing.
func WithJob(ctx context.Context, id job.ID) context.Context {
	traceID, err := TraceID(id)
	if err != nil {
		return ctx
	}

	return trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  parentOf(traceID),
		// The sampler reads the parent, so the spans of a job travel to the collector.
		TraceFlags: trace.FlagsSampled,
		// The parent belongs to the job and not to this process, which is what remote states.
		Remote: true,
	}))
}

// StartJob starts a span of one job in the trace of that job, and links the span of the context
// to it. Every span that begins the work of a job starts here.
//
// The span of the context is the tick that does the work. A tick is a short trace of its own, and
// the link is how a person goes from a slow tick to the jobs that it touched (target design,
// section 10.2).
func StartJob(ctx context.Context, tracer trace.Tracer, id job.ID, name string,
	options ...trace.SpanStartOption,
) (context.Context, trace.Span) {
	tick := trace.SpanFromContext(ctx)

	ctx, span := tracer.Start(WithJob(ctx, id), name, options...)

	tick.AddLink(trace.Link{SpanContext: span.SpanContext()})

	return ctx, span
}

// TraceID gives the trace identifier of a job, which is the 128 bits of its Buildkite UUID.
// Because of this, a person who has the job page can find the trace, and every span of the job
// is in that one trace.
func TraceID(id job.ID) (trace.TraceID, error) {
	var traceID trace.TraceID

	digits, err := hex.DecodeString(strings.ReplaceAll(string(id), "-", ""))
	if err != nil || len(digits) != len(traceID) {
		return traceID, fmt.Errorf("%q is not a job identifier, so it has no trace identifier", id)
	}

	copy(traceID[:], digits)

	if !traceID.IsValid() {
		return traceID, fmt.Errorf("the job %q gives a trace identifier of only zeros", id)
	}

	return traceID, nil
}

// parentOf gives the span that the spans of a job name as their parent. It is the first 64 bits
// of the trace, so that it needs no state and every process computes the same one.
//
// No span carries this identifier, because the parent is the job and the job is not a span of
// this controller. A backend therefore shows the trace of a job without its root, which is the
// price of a trace that outlives every process that writes to it.
func parentOf(traceID trace.TraceID) trace.SpanID {
	var spanID trace.SpanID

	copy(spanID[:], traceID[:len(spanID)])

	return spanID
}

// TraceParent gives the W3C traceparent of the active span, which goes into the environment of a
// job virtual machine. The agent inside the VM can then put its own spans in the job trace.
//
// The answer is empty when no span is active, and the caller then sends no traceparent.
func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}

	propagation.TraceContext{}.Inject(ctx, carrier)

	return carrier.Get("traceparent")
}
