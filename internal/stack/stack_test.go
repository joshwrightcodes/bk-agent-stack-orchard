package stack_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

	page          buildkite.Page
	registerErr   error
	deregisterErr error
	pollErr       error
}

func (f *fake) Register(context.Context) error {
	f.registered++

	return f.registerErr
}

func (f *fake) Deregister(context.Context) error {
	f.deregistered++

	return f.deregisterErr
}

func (f *fake) ScheduledJobs(_ context.Context, _ string, size int) (buildkite.Page, error) {
	f.polls++
	f.sizes = append(f.sizes, size)

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

	if _, err := stack.NewPoller(stack.PollerOptions{Stacks: &fake{}}); err == nil {
		t.Error("NewPoller() with no interval = nil, want an error")
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

// The poll reads one page. A queue longer than one page means that the fleet is full, and a
// controller that read every page would spend its rate limit on jobs that it cannot start.
func TestRefreshReadsOnePage(t *testing.T) {
	t.Parallel()

	api := &fake{page: buildkite.Page{NextCursor: "page-2"}}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second})

	if err := poller.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v, want no error", err)
	}

	if api.polls != 1 {
		t.Errorf("the poll made %d calls, want 1 even though the answer has a next page",
			api.polls)
	}
}

// A failed poll must not replace a good answer with an empty one, because the loop would then
// treat a full queue as an empty one.
func TestAFailedRefreshKeepsTheAnswerBeforeIt(t *testing.T) {
	t.Parallel()

	api := &fake{page: buildkite.Page{Jobs: []buildkite.ScheduledJob{{Priority: 1}}}}

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: api, Interval: time.Second})
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

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: &fake{}, Interval: 3 * time.Second})

	if got := poller.Interval(); got != 3*time.Second {
		t.Errorf("Interval() = %s, want 3s", got)
	}
}

func TestThePollerIsTheScheduledJobsSource(t *testing.T) {
	t.Parallel()

	poller, _ := stack.NewPoller(stack.PollerOptions{Stacks: &fake{}, Interval: time.Second})

	if poller.Source() != telemetry.SourceScheduledJobs {
		t.Errorf("Source() = %q, want %q", poller.Source(), telemetry.SourceScheduledJobs)
	}

	// The zero time is how the loop knows that no answer has arrived, and it keeps that source
	// out of the staleness metric.
	if !poller.At().IsZero() {
		t.Errorf("At() = %s before the first poll, want the zero time", poller.At())
	}
}
