package launcher_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/launcher"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

const (
	controller = "macos-prod"
	first      = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d")
	second     = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7e")
)

var pilot = profile.Profile{
	Name:             "xcode-27",
	Match:            map[string]string{"xcode": "27"},
	Image:            "ghcr.io/cirruslabs/macos-golden-gate-vanilla:27.0",
	CPU:              8,
	MemoryMiB:        28672,
	DiskSizeGB:       150,
	StartupTransport: profile.TransportGuestAgent,
}

// stacks answers as Buildkite and records the calls.
type stacks struct {
	reserved     [][]job.ID
	expiries     []time.Duration
	tokenCalls   [][]job.ID
	lifetimes    []time.Duration
	finished     []string
	exitStatuses []int
	notified     [][]buildkite.Notification

	reservation buildkite.Reservation
	tokens      buildkite.Tokens
	reserveErr  error
	tokenErr    error
	finishErr   error
	notifyErr   error
}

func (s *stacks) Reserve(_ context.Context, ids []job.ID, expiry time.Duration) (buildkite.Reservation, error) {
	s.reserved = append(s.reserved, ids)
	s.expiries = append(s.expiries, expiry)

	if s.reserveErr != nil {
		return buildkite.Reservation{}, s.reserveErr
	}

	// The default is that every job of the call is held.
	if len(s.reservation.Reserved) == 0 && len(s.reservation.NotReserved) == 0 {
		return buildkite.Reservation{Reserved: ids}, nil
	}

	return s.reservation, nil
}

func (s *stacks) Tokens(_ context.Context, ids []job.ID, lifetime time.Duration) (buildkite.Tokens, error) {
	s.tokenCalls = append(s.tokenCalls, ids)
	s.lifetimes = append(s.lifetimes, lifetime)

	if s.tokenErr != nil {
		return buildkite.Tokens{}, s.tokenErr
	}

	if len(s.tokens.Issued) == 0 && len(s.tokens.NotIssued) == 0 {
		issued := make([]buildkite.Token, 0, len(ids))
		for _, id := range ids {
			issued = append(issued, buildkite.Token{
				JobID: id, Token: secret.String("bkjat_" + string(id)),
			})
		}

		return buildkite.Tokens{Issued: issued}, nil
	}

	return s.tokens, nil
}

func (s *stacks) Finish(_ context.Context, id job.ID, exitStatus int, _ string) error {
	s.finished = append(s.finished, string(id))
	s.exitStatuses = append(s.exitStatuses, exitStatus)

	return s.finishErr
}

func (s *stacks) Notify(_ context.Context, lines []buildkite.Notification) error {
	s.notified = append(s.notified, lines)

	return s.notifyErr
}

// machines answers as Orchard.
type machines struct {
	created []orchard.VM
	err     error
}

func (m *machines) CreateVM(_ context.Context, spec orchard.VM) error {
	m.created = append(m.created, spec)

	return m.err
}

// records keeps the log records of a launch, so that a test can say that something is normal
// and not a failure.
type records struct {
	slog.Handler

	mu     sync.Mutex
	errors []string
}

func (r *records) Handle(_ context.Context, record slog.Record) error {
	if record.Level >= slog.LevelError {
		r.mu.Lock()
		r.errors = append(r.errors, record.Message)
		r.mu.Unlock()
	}

	return nil
}

func (r *records) Enabled(context.Context, slog.Level) bool { return true }

func (r *records) failures() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.errors)
}

func build(t *testing.T, api *stacks, fleet launcher.Machines) *launcher.Launcher {
	t.Helper()

	return buildWithLog(t, api, fleet, &records{Handler: slog.DiscardHandler})
}

func buildWithLog(t *testing.T, api *stacks, fleet launcher.Machines,
	log *records,
) *launcher.Launcher {
	t.Helper()

	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetryMetrics(t, provider)
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	started, err := launcher.New(launcher.Options{
		Stacks:            api,
		Orchard:           fleet,
		ControllerID:      controller,
		Queue:             "macos-prod",
		ReservationExpiry: 300 * time.Second,
		TokenLifetime:     1800 * time.Second,
		Metrics:           instruments,
		Logger:            slog.New(log),
	})
	if err != nil {
		t.Fatalf("New() = %v, want the launcher", err)
	}

	return started
}

func candidates(ids ...job.ID) []admission.Candidate {
	out := make([]admission.Candidate, 0, len(ids))
	for _, id := range ids {
		out = append(out, admission.Candidate{
			Job: buildkite.ScheduledJob{ID: id}, Profile: pilot,
		})
	}

	return out
}

// The order of the calls is what keeps one job to one machine: hold the job, then get its token,
// then ask for the machine.
func TestALaunchHoldsTheJobThenGetsATokenThenAsksForAMachine(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		inventory.Snapshot{})
	if err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.reserved) != 1 || len(api.reserved[0]) != 1 || api.reserved[0][0] != first {
		t.Fatalf("the reservation asked for %v, want the one job", api.reserved)
	}

	if api.expiries[0] != 300*time.Second {
		t.Errorf("the reservation lasts %s, want 300s", api.expiries[0])
	}

	if len(api.tokenCalls) != 1 || api.lifetimes[0] != 1800*time.Second {
		t.Errorf("the token call asked for %v over %v, want the job and 1800s",
			api.tokenCalls, api.lifetimes)
	}

	if len(fleet.created) != 1 {
		t.Fatalf("orchard saw %d machines, want 1", len(fleet.created))
	}

	machine := fleet.created[0]
	if machine.Name != "bk-"+controller+"-"+string(first) {
		t.Errorf("the machine is called %q, want the name of the job", machine.Name)
	}

	// The job page tells a pipeline author that something is happening.
	if len(api.notified) != 1 || len(api.notified[0]) != 1 {
		t.Fatalf("buildkite saw %v, want one notification", api.notified)
	}

	if api.notified[0][0].JobID != first || api.notified[0][0].Detail == "" {
		t.Errorf("the notification reads %+v, want the job and a line", api.notified[0][0])
	}

	// The launcher now knows what carries the job, which the instruments report.
	if started.Profile(first) != pilot.Name {
		t.Errorf("Profile() = %q, want %q", started.Profile(first), pilot.Name)
	}

	if _, held := started.ReservedUntil(first); !held {
		t.Error("ReservedUntil() reported no reservation for a job that was just held")
	}
}

// The unmatched jobs travel in the same reservation as the admitted ones, because one call costs
// less of the rate limit than two.
func TestTheUnmatchedJobsTravelInTheSameReservation(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	decision := admission.Result{
		Admit: candidates(first),
		Unmatched: []admission.Unmatched{{
			Job:    buildkite.ScheduledJob{ID: second},
			Reason: "no profile has these tags",
		}},
	}

	if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.reserved) != 1 || len(api.reserved[0]) != 2 {
		t.Fatalf("the controller made %v reservations, want one call of two jobs", api.reserved)
	}

	// The job ends with the reason, which a pipeline author reads on the job page.
	if len(api.finished) != 1 || api.finished[0] != string(second) {
		t.Fatalf("the controller ended %v, want the unmatched job", api.finished)
	}

	if api.exitStatuses[0] != -1 {
		t.Errorf("the unmatched job ended with %d, want -1", api.exitStatuses[0])
	}

	// An unmatched job uses no capacity: no token, and no machine.
	if len(api.tokenCalls) != 1 || len(api.tokenCalls[0]) != 1 {
		t.Errorf("the token call asked for %v, want the admitted job only", api.tokenCalls)
	}

	if len(fleet.created) != 1 || fleet.created[0].Name != "bk-"+controller+"-"+string(first) {
		t.Errorf("orchard saw %d machines, want only the one of the admitted job",
			len(fleet.created))
	}
}

// Every stack of a queue reads the same list, so another stack can hold a job first. The jobs
// beside it go on.
func TestAJobThatAnotherStackHoldsDropsOut(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	api.reservation = buildkite.Reservation{
		Reserved:    []job.ID{first},
		NotReserved: []job.ID{second},
	}

	started := build(t, api, fleet)

	err := started.Launch(t.Context(), admission.Result{Admit: candidates(first, second)},
		inventory.Snapshot{})
	if err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.tokenCalls[0]) != 1 || api.tokenCalls[0][0] != first {
		t.Errorf("the token call asked for %v, want the job that this stack holds",
			api.tokenCalls[0])
	}

	if len(fleet.created) != 1 {
		t.Errorf("orchard saw %d machines, want 1", len(fleet.created))
	}
}

// A job with no token would boot a machine that finds nothing to do. Its reservation expires by
// itself, and admission meets the job again later.
func TestAJobWithNoTokenGetsNoMachine(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	api.tokens = buildkite.Tokens{
		Issued:    []buildkite.Token{{JobID: first, Token: secret.String("bkjat_one")}},
		NotIssued: []job.ID{second},
	}

	log := &records{Handler: slog.DiscardHandler}
	started := buildWithLog(t, api, fleet, log)

	err := started.Launch(t.Context(), admission.Result{Admit: candidates(first, second)},
		inventory.Snapshot{})
	if err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(fleet.created) != 1 || fleet.created[0].Name != "bk-"+controller+"-"+string(first) {
		t.Errorf("orchard saw %d machines, want only the one with a token", len(fleet.created))
	}

	// Buildkite refuses a token for a job that it no longer wants started, which is an ordinary
	// answer. A controller that tried anyway would report a failure of its own making.
	if failures := log.failures(); len(failures) != 0 {
		t.Errorf("the launch reported the failures %v, want none: a refused token is ordinary",
			failures)
	}
}

// A machine of this name exists, which means that an earlier create arrived. The job has its
// machine, which is what the step wanted.
func TestAMachineThatExistsCountsAsAStartedOne(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{err: orchard.ErrAlreadyExists}
	started := build(t, api, fleet)

	err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		inventory.Snapshot{})
	if err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if started.Profile(first) != pilot.Name {
		t.Error("the launcher forgot a job whose machine already existed")
	}

	if len(api.notified) != 1 {
		t.Errorf("buildkite saw %v, want the notification of a started job", api.notified)
	}
}

// One machine that Orchard refuses must not stop the machines beside it.
func TestAMachineThatOrchardRefusesDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()

	api := &stacks{}
	fleet := &refusing{refuse: "bk-" + controller + "-" + string(first)}
	started := build(t, api, fleet)

	err := started.Launch(t.Context(), admission.Result{Admit: candidates(first, second)},
		inventory.Snapshot{})
	if err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(fleet.created) != 1 || fleet.created[0].Name != "bk-"+controller+"-"+string(second) {
		t.Errorf("orchard kept %+v, want the machine of the second job", fleet.created)
	}

	if started.Profile(first) != "" {
		t.Error("the launcher remembers a job whose machine it could not start")
	}
}

// refusing refuses one machine by name and accepts the rest.
type refusing struct {
	refuse  string
	created []orchard.VM
}

func (r *refusing) CreateVM(_ context.Context, spec orchard.VM) error {
	if spec.Name == r.refuse {
		return errors.New("orchard refused the machine")
	}

	r.created = append(r.created, spec)

	return nil
}

// A reservation that fails stops the tick. Nothing was held, so nothing may start.
func TestAReservationThatFailsStartsNothing(t *testing.T) {
	t.Parallel()

	api := &stacks{reserveErr: errors.New("buildkite is not available")}
	fleet := &machines{}
	started := build(t, api, fleet)

	err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		inventory.Snapshot{})
	if err == nil {
		t.Fatal("Launch() = nil, want the error of Buildkite")
	}

	if len(fleet.created) != 0 || len(api.tokenCalls) != 0 {
		t.Error("the controller went on after a reservation that failed")
	}
}

// A notification is a line on a job page. It must not stop a launch.
func TestANotificationThatFailsDoesNotStopALaunch(t *testing.T) {
	t.Parallel()

	api := &stacks{notifyErr: errors.New("buildkite refused the notification")}
	fleet := &machines{}
	started := build(t, api, fleet)

	err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		inventory.Snapshot{})
	if err != nil {
		t.Errorf("Launch() = %v, want no error: a job page is not a launch", err)
	}

	if len(fleet.created) != 1 {
		t.Errorf("orchard saw %d machines, want 1", len(fleet.created))
	}
}

func TestALaunchOfNothingCallsNothing(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	if err := started.Launch(t.Context(), admission.Result{}, inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.reserved) != 0 {
		t.Errorf("the controller made %v reservations for no jobs, want none", api.reserved)
	}
}

// Without this, one entry of every job that this controller ever started would stay for the life
// of the process.
func TestTheLauncherForgetsAJobWhoseMachineIsGone(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if started.Profile(first) == "" {
		t.Fatal("the launcher forgot the job that it started")
	}

	// A later tick, whose snapshot holds no machine of that job: the reaper deleted it.
	later := inventory.Snapshot{At: time.Now().Add(time.Minute)}
	if err := started.Launch(t.Context(), admission.Result{}, later); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if started.Profile(first) != "" {
		t.Error("the launcher still remembers a job whose machine is gone")
	}
}

// A snapshot older than the launch says nothing about the machine, because the machine did not
// exist when the snapshot was taken.
func TestTheLauncherKeepsAJobThatASnapshotCouldNotSeeYet(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	older := inventory.Snapshot{At: time.Now().Add(-time.Minute)}
	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		older); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if started.Profile(first) != pilot.Name {
		t.Error("the launcher forgot a job that the snapshot was too old to hold")
	}
}

func TestNewRefusesAnIncompleteLauncher(t *testing.T) {
	t.Parallel()

	if _, err := launcher.New(launcher.Options{}); err == nil {
		t.Error("New() with nothing = nil, want an error")
	}
}

// The token opens a queue of the cluster. It goes into the machine and nowhere else.
func TestTheTokenIsNotInAPrintOfTheSpecification(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	spec := fleet.created[0]
	if printed := spec.GoString(); strings.Contains(printed, "bkjat_") {
		t.Errorf("a print of the specification held the token: %s", printed)
	}
}

// telemetryMetrics builds the instruments that the launcher records into.
func telemetryMetrics(t *testing.T, provider *sdkmetric.MeterProvider) (*telemetry.Metrics, error) {
	t.Helper()

	return telemetry.NewMetrics(provider.Meter("test"))
}

// The inventory learns of a launch only at its next refresh. Until then the launcher is the only
// part that knows the job is taken, and admission asks it.
func TestTheLauncherHoldsAJobUntilTheFleetShowsItsMachine(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	before := inventory.Snapshot{At: time.Now().Add(-time.Minute)}
	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		before); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if held := started.Held(before); held[first] != pilot.Name {
		t.Errorf("Held() = %v, want the job with the profile of its machine", held)
	}

	// A later snapshot shows the machine, and the inventory takes over from here.
	shown := inventory.Snapshot{
		At:    time.Now().Add(time.Minute),
		Owned: []inventory.Owned{{Job: first}},
	}

	if held := started.Held(shown); len(held) != 0 {
		t.Errorf("Held() = %v, want nothing once the fleet shows the machine", held)
	}
}

// A job that Buildkite gave no token has no machine, and its reservation is still this
// process's own until it lapses.
func TestTheLauncherHoldsAReservedJobWithNoMachine(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	api.tokens = buildkite.Tokens{NotIssued: []job.ID{first}}
	started := build(t, api, fleet)

	before := inventory.Snapshot{At: time.Now().Add(-time.Minute)}
	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		before); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	held, has := started.Held(before)[first]
	if !has {
		t.Fatal("Held() does not name a job whose reservation this process holds")
	}

	if held != "" {
		t.Errorf("Held() gives the profile %q, want none: no machine is on its way", held)
	}
}
