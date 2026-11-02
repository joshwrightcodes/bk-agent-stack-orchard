// SPDX-License-Identifier: AGPL-3.0-or-later

package stack_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/stack"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// fake answers as Buildkite, and records what the package asked for.
type fake struct {
	registered   int
	deregistered int
	polls        int
	sizes        []int
	cursors      []string

	// pages answers a call by its cursor, when a test needs a queue of several pages.
	pages map[string]buildkite.Page

	page          buildkite.Page
	registerErr   error
	deregisterErr error
	pollErr       error
	pageErrs      map[string]error
}

func (f *fake) Register(context.Context) error {
	f.registered++

	return f.registerErr
}

func (f *fake) Deregister(context.Context) error {
	f.deregistered++

	return f.deregisterErr
}

func (f *fake) ScheduledJobs(_ context.Context, cursor string, size int) (buildkite.Page, error) {
	f.polls++
	f.sizes = append(f.sizes, size)
	f.cursors = append(f.cursors, cursor)

	if err, ok := f.pageErrs[cursor]; ok {
		return buildkite.Page{}, err
	}

	if page, ok := f.pages[cursor]; ok {
		return page, f.pollErr
	}

	return f.page, f.pollErr
}

func TestRegisterAnnouncesTheStack(t *testing.T) {
	t.Parallel()

	api := &fake{}

	registration, err := stack.New(api)
	if err != nil {
		t.Fatalf("New() = %v, want the stack", err)
	}

	if err := registration.Register(t.Context()); err != nil {
		t.Fatalf("Register() = %v, want no error", err)
	}

	if api.registered != 1 {
		t.Errorf("Buildkite saw %d registrations, want 1", api.registered)
	}
}

// A process that could not register has nothing to withdraw, and a call that fails at shutdown
// would hide the failure that stopped the process.
func TestDeregisterDoesNothingWithoutARegistration(t *testing.T) {
	t.Parallel()

	api := &fake{registerErr: errors.New("buildkite refused")}

	registration, err := stack.New(api)
	if err != nil {
		t.Fatalf("New() = %v, want the stack", err)
	}

	if err := registration.Register(t.Context()); err == nil {
		t.Fatal("Register() = nil, want the error of Buildkite")
	}

	if err := registration.Deregister(t.Context()); err != nil {
		t.Errorf("Deregister() = %v, want no error", err)
	}

	if api.deregistered != 0 {
		t.Errorf("Buildkite saw %d withdrawals, want none", api.deregistered)
	}
}

// A process stops on a signal and on a defer, so the withdrawal can happen twice.
func TestDeregisterWithdrawsOnceForOneRegistration(t *testing.T) {
	t.Parallel()

	api := &fake{}

	registration, _ := stack.New(api)
	if err := registration.Register(t.Context()); err != nil {
		t.Fatalf("Register() = %v, want no error", err)
	}

	for range 2 {
		if err := registration.Deregister(t.Context()); err != nil {
			t.Errorf("Deregister() = %v, want no error", err)
		}
	}

	if api.deregistered != 1 {
		t.Errorf("Buildkite saw %d withdrawals, want 1", api.deregistered)
	}
}

func TestNewRefusesNoClient(t *testing.T) {
	t.Parallel()

	if _, err := stack.New(nil); err == nil {
		t.Error("New(nil) = nil, want an error")
	}

	if _, err := stack.NewPoller(stack.PollerOptions{Interval: time.Second}); err == nil {
		t.Error("NewPoller() with no client = nil, want an error")
	}

	if _, err := stack.NewPoller(stack.PollerOptions{Stacks: &fake{}, Now: time.Now}); err == nil {
		t.Error("NewPoller() with no interval = nil, want an error")
	}

	if _, err := stack.NewPoller(stack.PollerOptions{Stacks: &fake{}, Interval: time.Second}); err == nil {
		t.Error("NewPoller() with no clock = nil, want an error")
	}
}

// The poll gives the jobs, the pause, and the time of the answer. The loop reads all three.
func TestRefreshKeepsTheAnswerAndItsTime(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	api := &fake{page: buildkite.Page{
		Jobs: []buildkite.ScheduledJob{{
			ID: job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d"), Priority: 3,
		}},
		Paused: true,
	}}

	poller, err := stack.NewPoller(stack.PollerOptions{
		Stacks:   api,
		Interval: time.Second,
		Now:      func() time.Time { return at },
	})
	if err != nil {
		t.Fatalf("NewPoller() = %v, want the poller", err)
	}

	// An empty queue and an unread queue look the same, so the loop must be able to tell them
	// apart.
	if _, read := poller.Latest(); read {
		t.Error("Latest() reported an answer before the first poll")
	}

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	queue, read := poller.Latest()
	if !read {
		t.Fatal("Latest() reported no answer after a poll")
	}

	if len(queue.Jobs) != 1 || queue.Jobs[0].Priority != 3 {
		t.Errorf("the answer held %+v, want the job of the page", queue.Jobs)
	}

	if !queue.Paused {
		t.Error("the answer reported a running queue, want the paused queue of the page")
	}

	if !queue.At.Equal(at) || !poller.At().Equal(at) {
		t.Errorf("the answer is from %s, want %s", queue.At, at)
	}
}

// A queue of more jobs than one page holds must not hide the jobs behind the first page: the
// controller can carry jobs that rank low in the list. The poll reads the pages from the head,
// asks for the largest page that the API accepts, and stops after three pages.
func TestTheSourceReadsSeveralPagesOfScheduledJobs(t *testing.T) {
	t.Parallel()

	one := job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a01")
	two := job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a02")
	three := job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a03")
	four := job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a04")

	api := &fake{pages: map[string]buildkite.Page{
		"":       {Jobs: []buildkite.ScheduledJob{{ID: one}}, Paused: true, NextCursor: "page-2"},
		"page-2": {Jobs: []buildkite.ScheduledJob{{ID: two}}, NextCursor: "page-3"},
		"page-3": {Jobs: []buildkite.ScheduledJob{{ID: three}}, NextCursor: "page-4"},
		"page-4": {Jobs: []buildkite.ScheduledJob{{ID: four}}},
	}}

	poller, err := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second, Now: time.Now})
	if err != nil {
		t.Fatalf("NewPoller() = %v, want the poller", err)
	}

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if want := []string{"", "page-2", "page-3"}; !slices.Equal(api.cursors, want) {
		t.Errorf("the poll asked for the cursors %q, want %q: from the head, 3 pages at most",
			api.cursors, want)
	}

	want := []int{stack.PageSize, stack.PageSize, stack.PageSize}
	if !slices.Equal(api.sizes, want) {
		t.Errorf("the poll asked for the sizes %v, want %v: the size does not depend on the slots",
			api.sizes, want)
	}

	queue, _ := poller.Latest()

	var got []job.ID
	for _, scheduled := range queue.Jobs {
		got = append(got, scheduled.ID)
	}

	if want := []job.ID{one, two, three}; !slices.Equal(got, want) {
		t.Errorf("the answer holds %v, want the jobs of the 3 pages in order %v", got, want)
	}

	if !queue.Paused {
		t.Error("the answer reported a running queue, want the pause of the first page")
	}
}

// Jobs that never leave the list, such as jobs that no profile matches, fill the pages. A tick
// that stops at the cap with a next page left says so in a warning and in a counter, so that an
// operator can see that jobs behind the pages may be hidden.
func TestATickThatStopsAtTheCapWarnsAndCounts(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	metrics, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	var logged bytes.Buffer

	api := &fake{pages: map[string]buildkite.Page{
		"":       {NextCursor: "page-2"},
		"page-2": {NextCursor: "page-3"},
		"page-3": {NextCursor: "page-4"},
	}}

	poller, _ := stack.NewPoller(stack.PollerOptions{
		Stacks:   api,
		Interval: time.Second,
		Metrics:  metrics,
		Logger:   slog.New(slog.NewTextHandler(&logged, nil)),
		Now:      time.Now,
	})

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if strings.Count(logged.String(), "level=WARN") != 1 {
		t.Errorf("the poll logged %q, want one warning", logged.String())
	}

	if got := truncations(t, reader); got != 1 {
		t.Errorf("the counter holds %d, want 1", got)
	}

	// A tick that reaches the last page inside the cap says nothing.
	api.pages = map[string]buildkite.Page{"": {NextCursor: "page-2"}, "page-2": {}}
	logged.Reset()

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if logged.Len() != 0 || truncations(t, reader) != 1 {
		t.Errorf("a whole read logged %q and counted %d, want neither", logged.String(),
			truncations(t, reader))
	}
}

// truncations reads the counter of the reads that stopped at the cap.
func truncations(t *testing.T, reader sdkmetric.Reader) int64 {
	t.Helper()

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want no error", err)
	}

	for _, scope := range collected.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			if recorded.Name != "bk_agent_stack_orchard.queue.truncated" {
				continue
			}

			sum, ok := recorded.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("the counter is a %T, want a sum", recorded.Data)
			}

			var total int64
			for _, point := range sum.DataPoints {
				total += point.Value
			}

			return total
		}
	}

	return 0
}

// A cursor over a list that changes can give a job on two pages. A job must enter the answer
// once, because one batch-reserve call must not carry the same job twice.
func TestTheSourceKeepsAJobThatTwoPagesGiveOnce(t *testing.T) {
	t.Parallel()

	one := job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a01")
	two := job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a02")

	api := &fake{pages: map[string]buildkite.Page{
		"":       {Jobs: []buildkite.ScheduledJob{{ID: one}}, NextCursor: "page-2"},
		"page-2": {Jobs: []buildkite.ScheduledJob{{ID: one}, {ID: two}}},
	}}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second, Now: time.Now})
	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	queue, _ := poller.Latest()

	var got []job.ID
	for _, scheduled := range queue.Jobs {
		got = append(got, scheduled.ID)
	}

	if want := []job.ID{one, two}; !slices.Equal(got, want) {
		t.Errorf("the answer holds %v, want %v: each job once, in the order of the pages", got, want)
	}
}

// The poll stops at the last page.
func TestTheSourceStopsAtTheLastPage(t *testing.T) {
	t.Parallel()

	api := &fake{pages: map[string]buildkite.Page{
		"":       {NextCursor: "page-2"},
		"page-2": {},
	}}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second, Now: time.Now})

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if api.polls != 2 {
		t.Errorf("the poll made %d calls, want 2: the last page has no next page", api.polls)
	}

	if queue, _ := poller.Latest(); queue.Truncated {
		t.Error("the queue is truncated after a read that reached the last page, want a whole one")
	}
}

// The launcher evicts only after a whole listing, so the queue says when the read stopped at the cap.
func TestAReadThatStopsAtTheCapIsMarkedTruncated(t *testing.T) {
	t.Parallel()

	api := &fake{pages: map[string]buildkite.Page{
		"":       {NextCursor: "page-2"},
		"page-2": {NextCursor: "page-3"},
		"page-3": {NextCursor: "page-4"},
	}}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second, Now: time.Now})

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if queue, _ := poller.Latest(); !queue.Truncated {
		t.Error("the queue is whole after a read that stopped at the cap with a page left, want truncated")
	}
}

// A page that fails in the middle of a read must not leave half a queue in place of a whole one.
func TestAFailureOnALaterPageKeepsTheAnswerBeforeIt(t *testing.T) {
	t.Parallel()

	api := &fake{page: buildkite.Page{Jobs: []buildkite.ScheduledJob{{Priority: 1}}}}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second, Now: time.Now})
	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	api.pages = map[string]buildkite.Page{
		"": {Jobs: []buildkite.ScheduledJob{{Priority: 9}}, NextCursor: "page-2"},
	}
	api.pageErrs = map[string]error{"page-2": errors.New("buildkite is not available")}

	if err := poller.Refresh(t.Context()); err == nil {
		t.Fatal("Refresh() = nil, want the error of the failed page")
	}

	if want := []string{"", "", "page-2"}; !slices.Equal(api.cursors, want) {
		t.Fatalf("the polls asked for %q, want %q: the second read must reach page 2",
			api.cursors, want)
	}

	queue, _ := poller.Latest()
	if len(queue.Jobs) != 1 || queue.Jobs[0].Priority != 1 {
		t.Errorf("the answer holds %+v, want the answer before the failure", queue.Jobs)
	}
}

// A failed poll must not replace a good answer with an empty one, because the loop would then
// treat a full queue as an empty one.
func TestAFailedRefreshKeepsTheAnswerBeforeIt(t *testing.T) {
	t.Parallel()

	api := &fake{page: buildkite.Page{Jobs: []buildkite.ScheduledJob{{Priority: 1}}}}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second, Now: time.Now})
	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	api.pollErr = errors.New("buildkite is not available")

	if err := poller.Refresh(t.Context()); err == nil {
		t.Fatal("Refresh() = nil, want the error of Buildkite")
	}

	queue, read := poller.Latest()
	if !read || len(queue.Jobs) != 1 {
		t.Errorf("the answer is now %+v, want the answer that arrived before the failure", queue)
	}
}

// The poller gives its interval, and the control loop decides when it is complete. A source that
// decided for itself would measure from its own answer, which arrives after the tick.
func TestThePollerGivesItsInterval(t *testing.T) {
	t.Parallel()

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: &fake{}, Interval: 3 * time.Second, Now: time.Now})

	if got := poller.Interval(); got != 3*time.Second {
		t.Errorf("Interval() = %s, want 3s", got)
	}
}

func TestThePollerIsTheScheduledJobsSource(t *testing.T) {
	t.Parallel()

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: &fake{}, Interval: time.Second, Now: time.Now})

	if poller.Source() != telemetry.SourceScheduledJobs {
		t.Errorf("Source() = %q, want %q", poller.Source(), telemetry.SourceScheduledJobs)
	}

	// The zero time is how the loop knows that no answer has arrived, and it keeps that source
	// out of the staleness metric.
	if !poller.At().IsZero() {
		t.Errorf("At() = %s before the first poll, want the zero time", poller.At())
	}
}
