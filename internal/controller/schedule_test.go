// These tests are inside the package, because the rule that they prove is when the loop reads a
// source, and that decision needs a tick time that a test can choose.
package controller

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/stack"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

var start = time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

type counted struct {
	interval time.Duration
	err      error
	reads    int
}

func (c *counted) Source() telemetry.Source      { return telemetry.SourceInventory }
func (c *counted) Interval() time.Duration       { return c.interval }
func (c *counted) At() time.Time                 { return time.Time{} }
func (c *counted) Refresh(context.Context) error { c.reads++; return c.err }

type nothing struct{}

func (nothing) Register(context.Context) error   { return nil }
func (nothing) Deregister(context.Context) error { return nil }

type unread struct{}

func (unread) Latest() (stack.Queue, bool) { return stack.Queue{}, false }

func loop(t *testing.T, source Source) *Controller {
	t.Helper()

	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	built, err := New(Options{
		Stack:     nothing{},
		Queue:     unread{},
		Sources:   []Source{source},
		Interval:  time.Second,
		Freshness: time.Minute,
		Metrics:   instruments,
		Logger:    slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New() = %v, want the loop", err)
	}

	return built
}

// ticks runs the loop at these offsets from the start.
func ticks(t *testing.T, c *Controller, offsets ...time.Duration) {
	t.Helper()

	for _, offset := range offsets {
		c.Tick(t.Context(), start.Add(offset))
	}
}

// A source whose interval equals the tick interval is read at every tick. The loop measured from
// the answer of the source before, which arrives after its tick, so it read such a source at every
// other tick and the fleet ran at half the rate of the design.
func TestASourceOfTheTickIntervalIsReadAtEveryTick(t *testing.T) {
	t.Parallel()

	source := &counted{interval: time.Second}

	ticks(t, loop(t, source), 0, time.Second, 2*time.Second, 3*time.Second, 4*time.Second)

	if source.reads != 5 {
		t.Errorf("the loop read the source %d times in 5 ticks, want 5", source.reads)
	}
}

// A ticker does not fire at the exact moment. A tick that arrives a little early must still find
// the source due, or the rate falls by half again.
func TestTheJitterOfTheTickerDoesNotSkipARead(t *testing.T) {
	t.Parallel()

	source := &counted{interval: time.Second}

	ticks(t, loop(t, source),
		0, 999*time.Millisecond, 2001*time.Millisecond, 2998*time.Millisecond)

	if source.reads != 4 {
		t.Errorf("the loop read the source %d times in 4 ticks, want 4", source.reads)
	}
}

// A longer interval is read at its own rate, and not at every tick.
func TestALongerIntervalIsReadAtItsOwnRate(t *testing.T) {
	t.Parallel()

	source := &counted{interval: 5 * time.Second}

	offsets := make([]time.Duration, 0, 11)
	for i := range 11 {
		offsets = append(offsets, time.Duration(i)*time.Second)
	}

	ticks(t, loop(t, source), offsets...)

	// At 0 s, 5 s, and 10 s.
	if source.reads != 3 {
		t.Errorf("the loop read the source %d times in 11 ticks, want 3", source.reads)
	}
}

// A source that failed waits for its interval like one that answered. The failure is in the API
// behind it, and a read at every tick would press that API harder.
func TestASourceThatFailedWaitsForItsInterval(t *testing.T) {
	t.Parallel()

	source := &counted{interval: 5 * time.Second, err: errors.New("orchard is not available")}

	ticks(t, loop(t, source), 0, time.Second, 2*time.Second, 3*time.Second, 4*time.Second)

	if source.reads != 1 {
		t.Errorf("the loop read a failing source %d times in its interval, want 1", source.reads)
	}
}
