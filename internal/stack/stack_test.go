// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package stack_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
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
	registerErrs  []error
	deregisterErr error
	pollErr       error
	pageErrs      map[string]error
}

func (f *fake) Register(context.Context) error {
	f.registered++

	// The answers of a sequence come first, one for each call, and then the fixed answer.
	if len(f.registerErrs) > 0 {
		err := f.registerErrs[0]
		f.registerErrs = f.registerErrs[1:]

		return err
	}

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

// waits gives a stack that records each wait of Register and does not sleep.
func waits(t *testing.T, api *fake) (*stack.Stack, *[]time.Duration) {
	t.Helper()

	registration, err := stack.New(api)
	if err != nil {
		t.Fatalf("New() = %v, want the stack", err)
	}

	var waited []time.Duration

	registration.SetSleep(func(_ context.Context, d time.Duration) error {
		waited = append(waited, d)

		return nil
	})

	return registration, &waited
}

// limited is a rate limit of Buildkite that names a wait.
func limited(after time.Duration) error {
	return retry.Again(fmt.Errorf("%w: slow down", buildkite.ErrRateLimited), after)
}

// At start-up nothing else runs, so a registration that Buildkite limits waits for the time that
// Buildkite names and tries again, and does not end the process at once.
func TestRegisterWaitsForTheTimeThatBuildkiteNames(t *testing.T) {
	t.Parallel()

	api := &fake{registerErrs: []error{limited(5 * time.Second), limited(20 * time.Second)}}
	registration, waited := waits(t, api)

	if err := registration.Register(t.Context()); err != nil {
		t.Fatalf("Register() = %v, want the third try to succeed", err)
	}

	if api.registered != 3 || !slices.Equal(*waited, []time.Duration{5 * time.Second, 20 * time.Second}) {
		t.Errorf("Buildkite saw %d registrations after the waits %v, want 3 after 5s and 20s",
			api.registered, *waited)
	}
}

// The waits of a registration have a total. A wait that would pass it ends the registration at
// once, and the process ends and starts again with the backoff of the platform.
func TestRegisterStopsWhenTheWaitsPassTheirTotal(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		answers []error
		waited  []time.Duration
	}{
		"one long wait": {answers: []error{limited(2 * time.Minute)}},
		"two waits":     {answers: []error{limited(40 * time.Second), limited(30 * time.Second)}, waited: []time.Duration{40 * time.Second}},
		"no named wait": {answers: []error{retry.Again(errors.New("answered 503"), 0)}},
		"a refusal":     {answers: []error{errors.New("answered 400")}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			api := &fake{registerErrs: test.answers, registerErr: errors.New("a call too many")}
			registration, waited := waits(t, api)

			err := registration.Register(t.Context())
			if err == nil || strings.Contains(err.Error(), "a call too many") {
				t.Fatalf("Register() = %v, want the answer of the last call", err)
			}

			if api.registered != len(test.answers) || !slices.Equal(*waited, test.waited) {
				t.Errorf("Buildkite saw %d registrations after the waits %v, want %d after %v",
					api.registered, *waited, len(test.answers), test.waited)
			}
		})
	}
}

// The end of the context ends a wait, so a shutdown at start-up does not wait for Buildkite.
func TestTheEndOfTheContextEndsTheWaitOfARegistration(t *testing.T) {
	t.Parallel()

	api := &fake{registerErrs: []error{limited(30 * time.Second)}}

	registration, err := stack.New(api)
	if err != nil {
		t.Fatalf("New() = %v, want the stack", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := registration.Register(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Register() = %v, want the end of the context", err)
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

// pagesOf builds a queue of the pages that the caller gives. The cursor of page n is "page-n", and
// the first page has the empty cursor.
func pagesOf(pages ...[]buildkite.ScheduledJob) map[string]buildkite.Page {
	queue := make(map[string]buildkite.Page, len(pages))

	for index, jobs := range pages {
		page := buildkite.Page{Jobs: jobs}
		if index+1 < len(pages) {
			page.NextCursor = fmt.Sprintf("page-%d", index+2)
		}

		queue[cursorOf(index+1)] = page
	}

	return queue
}

// cursorOf gives the cursor of page n of pagesOf.
func cursorOf(page int) string {
	if page == 1 {
		return ""
	}

	return fmt.Sprintf("page-%d", page)
}

// flood gives a page of jobs that no profile carries, which stay in the list. Each page has its
// own jobs, so that the identifiers do not repeat between pages.
func flood(page int) []buildkite.ScheduledJob {
	return []buildkite.ScheduledJob{{
		ID:       job.ID(fmt.Sprintf("018f3b2c-1c4a-7e5b-9a3d-%012x", page)),
		Rules:    []string{"queue=macos", "flood=yes"},
		Pipeline: "flood",
	}}
}

// has reports whether the last answer holds the job.
func has(poller *stack.Poller, id job.ID) bool {
	queue, _ := poller.Latest()

	return slices.ContainsFunc(queue.Jobs, func(scheduled buildkite.ScheduledJob) bool {
		return scheduled.ID == id
	})
}

// Jobs that never leave the list, more than the pages of one read, must not hide the jobs behind
// them for ever: a pipeline author could then stop the fleet with one upload. The poll finds a
// job behind a flood of any length in a number of reads that grows with the flood, and each read
// makes the same fixed number of calls.
func TestAFloodAtTheHeadDoesNotHideTheJobsBehindIt(t *testing.T) {
	t.Parallel()

	const flooded = 40

	legit := job.ID("018f3b2c-1c4a-7e5b-9a3d-ffffffffffff")

	var pages [][]buildkite.ScheduledJob
	for page := range flooded {
		pages = append(pages, flood(page+1))
	}

	pages = append(pages, []buildkite.ScheduledJob{{ID: legit}})

	api := &fake{pages: pagesOf(pages...)}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second, Now: time.Now})

	// The head takes HeadPages pages, and each read sweeps one more page. The job is on page
	// flooded+1, so the sweep reaches it at the read flooded+1-HeadPages.
	reads := flooded + 1 - stack.HeadPages

	for read := 1; read <= reads; read++ {
		before := api.polls

		if err := poller.Refresh(t.Context()); err != nil {
			t.Fatalf("read %d: Refresh() = %v, want no error", read, err)
		}

		if calls := api.polls - before; calls > stack.MaxPages {
			t.Fatalf("read %d made %d calls, want at most %d whatever the length of the queue",
				read, calls, stack.MaxPages)
		}

		if has(poller, legit) {
			if read != reads {
				t.Errorf("the job behind the flood arrived at read %d, want read %d", read, reads)
			}

			return
		}
	}

	t.Errorf("the job behind %d pages of jobs that stay in the list is hidden after %d reads, "+
		"want the sweep to reach it", flooded, reads)
}

// The sweep moves one page at each read, and it starts again after the head when it reaches the
// end of the list. A read that skips pages between the head and the sweep is not a whole listing,
// also when it reaches the last page.
func TestTheSweepMovesOnePageAtEachReadAndStartsAgainAtTheEnd(t *testing.T) {
	t.Parallel()

	api := &fake{pages: pagesOf(flood(1), flood(2), flood(3), flood(4), flood(5))}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second, Now: time.Now})

	steps := []struct {
		cursors   []string
		truncated bool
	}{
		{[]string{"", "page-2", "page-3"}, true},
		{[]string{"", "page-2", "page-4"}, true},
		{[]string{"", "page-2", "page-5"}, true},
		{[]string{"", "page-2", "page-3"}, true},
	}

	for number, step := range steps {
		api.cursors = nil

		if err := poller.Refresh(t.Context()); err != nil {
			t.Fatalf("read %d: Refresh() = %v, want no error", number+1, err)
		}

		if !slices.Equal(api.cursors, step.cursors) {
			t.Errorf("read %d asked for the cursors %q, want %q", number+1, api.cursors, step.cursors)
		}

		if queue, _ := poller.Latest(); queue.Truncated != step.truncated {
			t.Errorf("read %d: Truncated = %t, want %t", number+1, queue.Truncated, step.truncated)
		}
	}
}

// A read whose pages follow one another from the head to the last page is a whole listing, and
// the launcher may then forget the jobs that left the list.
func TestAReadOfThePagesInSequenceToTheEndIsWhole(t *testing.T) {
	t.Parallel()

	api := &fake{pages: pagesOf(flood(1), flood(2), flood(3))}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second, Now: time.Now})

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	queue, _ := poller.Latest()
	if queue.Truncated || len(queue.Jobs) != 3 {
		t.Errorf("the answer holds %d jobs and Truncated = %t, want 3 jobs and a whole listing",
			len(queue.Jobs), queue.Truncated)
	}
}

// A sweep page that fails leaves the head pages of the read, which arrived whole, so the read
// keeps them as a truncated answer and says why in the log (ADR-0019). Buildkite may refuse a
// cursor of an earlier read, and a read that kept nothing would then fail at every other tick.
// The next read starts the sweep again after the head, because a cursor that Buildkite refused
// once may never work again.
func TestAFailedSweepKeepsTheHeadAndStartsAgainAfterIt(t *testing.T) {
	t.Parallel()

	api := &fake{pages: pagesOf(flood(1), flood(2), flood(3), flood(4), flood(5))}

	var logged bytes.Buffer

	moment := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	clock := moment

	poller, _ := stack.NewPoller(stack.PollerOptions{
		Stacks: api, Interval: time.Second, Now: func() time.Time { return clock },
		Logger: slog.New(slog.NewTextHandler(&logged, nil)),
	})

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	api.pageErrs = map[string]error{"page-4": errors.New("the cursor is not valid")}
	clock = moment.Add(time.Second)

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v after a failed sweep page, want the head as the answer", err)
	}

	after, _ := poller.Latest()

	head := []job.ID{flood(1)[0].ID, flood(2)[0].ID}
	if !slices.Equal(ids(after), head) || !after.Truncated || !after.At.Equal(clock) {
		t.Errorf("the answer after a failed sweep is %v, Truncated = %t, at %s; want the head %v, "+
			"truncated, at %s", ids(after), after.Truncated, after.At, head, clock)
	}

	if log := logged.String(); !strings.Contains(log, "level=WARN") ||
		!strings.Contains(log, "the cursor is not valid") {
		t.Errorf("the log reads %q, want a warning with the error of the sweep page", log)
	}

	api.cursors = nil

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if want := []string{"", "page-2", "page-3"}; !slices.Equal(api.cursors, want) {
		t.Errorf("the read after a failed sweep asked for %q, want %q", api.cursors, want)
	}
}

// A queue that the head reads to its end ends the sweep, so that the sweep of a later flood
// starts after the head and not at a cursor of an old list.
func TestAShortQueueEndsTheSweep(t *testing.T) {
	t.Parallel()

	long := pagesOf(flood(1), flood(2), flood(3), flood(4), flood(5))
	api := &fake{pages: long}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second, Now: time.Now})

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	api.pages = pagesOf(flood(1), flood(2))

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	api.pages = long
	api.cursors = nil

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if want := []string{"", "page-2", "page-3"}; !slices.Equal(api.cursors, want) {
		t.Errorf("the read of a new flood asked for %q, want %q", api.cursors, want)
	}
}

// ids gives the identifiers of the jobs of an answer, in order.
func ids(queue stack.Queue) []job.ID {
	got := make([]job.ID, 0, len(queue.Jobs))
	for _, scheduled := range queue.Jobs {
		got = append(got, scheduled.ID)
	}

	return got
}

// The warning of a cut read names the pipeline that holds the most jobs of the read, so that an
// operator can find the source of a flood without a second tool.
func TestTheWarningOfACutReadNamesThePipelineWithTheMostJobs(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer

	api := &fake{pages: pagesOf(flood(1), flood(2),
		[]buildkite.ScheduledJob{{ID: job.ID("018f3b2c-1c4a-7e5b-9a3d-ffffffffffff"), Pipeline: "app"}},
		flood(4))}

	poller, _ := stack.NewPoller(stack.PollerOptions{
		Stacks:   api,
		Interval: time.Second,
		Logger:   slog.New(slog.NewTextHandler(&logged, nil)),
		Now:      time.Now,
	})

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if !strings.Contains(logged.String(), "cicd.pipeline.name=flood") ||
		!strings.Contains(logged.String(), "bk_agent_stack_orchard.queue.pipeline_jobs=2") {
		t.Errorf("the warning is %q, want the pipeline flood with its 2 jobs", logged.String())
	}
}
