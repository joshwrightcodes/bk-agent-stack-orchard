// SPDX-License-Identifier: AGPL-3.0-or-later

package launcher_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/launcher"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/match"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/parallel"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

const (
	controller = "macos-prod"
	first      = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d")
	second     = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7e")
	third      = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7f")
	fourth     = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a80")
	fifth      = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a81")
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

// defaultTimeouts are the timeouts of the design: 2 min, 15 min, and 5 min, which add to 22 min.
var defaultTimeouts = profile.Timeouts{
	Unplaced: 2 * time.Minute, Boot: 15 * time.Minute, Acquire: 5 * time.Minute,
}

// stacks answers as Buildkite and records the calls.
type stacks struct {
	reserved     [][]job.ID
	expiries     []time.Duration
	tokenCalls   [][]job.ID
	lifetimes    []time.Duration
	finished     []string
	exitStatuses []int
	details      []string
	notified     [][]buildkite.Notification

	reservation buildkite.Reservation
	clock       *time.Time
	tokens      buildkite.Tokens
	reserveErr  error
	tokenErr    error
	finishErr   error
	notifyErr   error

	// refuse holds the jobs whose lines Buildkite refuses, as it does for a job that ended.
	refuse map[job.ID]bool

	// failTokenCall holds the calls of Tokens, by number from 0, that fail.
	failTokenCall map[int]error

	// tokenDelay is how long each call of Tokens takes before it answers. A call whose context
	// ends first gives the error of the context.
	tokenDelay time.Duration

	// failCall holds the calls of Notify, by number from 0, that fail as a whole.
	failCall map[int]error

	// bounded holds, for each call of Notify, whether its context had a deadline.
	bounded []bool

	// order is the calls in the sequence that they came.
	order []string
}

func (s *stacks) Reserve(_ context.Context, ids []job.ID, expiry time.Duration) (buildkite.Reservation, error) {
	s.order = append(s.order, "reserve")
	s.reserved = append(s.reserved, ids)
	s.expiries = append(s.expiries, expiry)

	// A call over a network takes time, and a test that reads the clock sees it move.
	if s.clock != nil {
		*s.clock = s.clock.Add(3 * time.Second)
	}

	if s.reserveErr != nil {
		return buildkite.Reservation{}, s.reserveErr
	}

	// The default is that every job of the call is held.
	if len(s.reservation.Reserved) == 0 && len(s.reservation.NotReserved) == 0 {
		return buildkite.Reservation{Reserved: ids}, nil
	}

	return s.reservation, nil
}

func (s *stacks) Tokens(ctx context.Context, ids []job.ID, lifetime time.Duration) (buildkite.Tokens, error) {
	s.order = append(s.order, "tokens")
	s.tokenCalls = append(s.tokenCalls, ids)
	s.lifetimes = append(s.lifetimes, lifetime)

	if s.clock != nil {
		*s.clock = s.clock.Add(3 * time.Second)
	}

	if s.tokenDelay > 0 {
		timer := time.NewTimer(s.tokenDelay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return buildkite.Tokens{}, ctx.Err()
		case <-timer.C:
		}
	}

	if err := s.failTokenCall[len(s.tokenCalls)-1]; err != nil {
		return buildkite.Tokens{}, err
	}

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

func (s *stacks) Finish(_ context.Context, id job.ID, exitStatus int, detail string) error {
	s.finished = append(s.finished, string(id))
	s.exitStatuses = append(s.exitStatuses, exitStatus)
	s.details = append(s.details, detail)

	return s.finishErr
}

func (s *stacks) Notify(ctx context.Context, lines []buildkite.Notification) error {
	_, bounded := ctx.Deadline()
	s.bounded = append(s.bounded, bounded)

	call := len(s.notified)
	s.order = append(s.order, "notify")
	s.notified = append(s.notified, lines)

	if err := s.failCall[call]; err != nil {
		return err
	}

	var refused []int

	for index, line := range lines {
		if s.refuse[line.JobID] {
			refused = append(refused, index)
		}
	}

	if len(refused) > 0 {
		return &buildkite.RefusedError{Indexes: refused, Total: len(lines), Reason: "job is finished"}
	}

	return s.notifyErr
}

// machines answers as Orchard. The launcher asks for several machines at the same time.
type machines struct {
	mu      sync.Mutex
	created []orchard.VM
	err     error
}

func (m *machines) CreateVM(_ context.Context, spec orchard.VM) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.created = append(m.created, spec)

	return m.err
}

// records keeps the log records of a launch, so that a test can say that something is normal
// and not a failure.
type records struct {
	slog.Handler

	mu     sync.Mutex
	errors []string
	infos  []string
}

func (r *records) Handle(_ context.Context, record slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if record.Level >= slog.LevelError {
		r.errors = append(r.errors, record.Message)
	}

	if record.Level == slog.LevelInfo {
		line := record.Message

		record.Attrs(func(attr slog.Attr) bool {
			line += " " + attr.Key + "=" + attr.Value.String()

			return true
		})

		r.infos = append(r.infos, line)
	}

	return nil
}

// infoWith gives the info lines that contain the text.
func (r *records) infoWith(text string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var found []string

	for _, line := range r.infos {
		if strings.Contains(line, text) {
			found = append(found, line)
		}
	}

	return found
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

	return buildTimed(t, api, fleet, log, time.Second)
}

// buildTimed gives a launcher whose calls have the time limit that the test chose.
func buildTimed(t *testing.T, api *stacks, fleet launcher.Machines, log *records,
	limit time.Duration,
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
		DefaultTimeouts:   defaultTimeouts,
		CallTimeout:       limit,
		Metrics:           instruments,
		Logger:            slog.New(log),
		Now:               time.Now,
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

// The agent of a machine carries the literal tags of its job, so the job page shows them.
func TestTheMachineOfAJobCarriesTheTagsOfThatJob(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	waiting := admission.Candidate{
		Job:     buildkite.ScheduledJob{ID: first, Rules: []string{"queue=macos-prod", "xcode=27"}},
		Profile: pilot,
	}

	err := started.Launch(t.Context(), admission.Result{Admit: []admission.Candidate{waiting}},
		inventory.Snapshot{})
	if err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(fleet.created) != 1 {
		t.Fatalf("orchard saw %d machines, want 1", len(fleet.created))
	}

	want := "queue=macos-prod,agent-stack=orchard,profile=" + pilot.Name + ",xcode=27"
	if got := fleet.created[0].StartupScript.Env["BUILDKITE_AGENT_TAGS"]; got != want {
		t.Errorf("BUILDKITE_AGENT_TAGS = %q, want %q", got, want)
	}
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

	// The pilot profile takes the default timeouts: 22 min to start, and the margin of a minute.
	if len(api.tokenCalls) != 1 || api.lifetimes[0] != 23*time.Minute {
		t.Errorf("the token call asked for %v over %v, want the job and 23m",
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

// A batch with matched and unmatched jobs reserves only the matched ones. An unmatched job is not
// ours to hold: another stack of the queue may carry it.
func TestAMixedBatchReservesOnlyTheAdmittedJobs(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	decision := admission.Result{
		Admit: candidates(first),
		Unmatched: []admission.Unmatched{{
			Job:      buildkite.ScheduledJob{ID: second},
			Mismatch: errors.New("no profile carries this job"),
		}},
	}

	if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.reserved) != 1 || !slices.Equal(api.reserved[0], []job.ID{first}) {
		t.Fatalf("the controller reserved %v, want one call with the admitted job only",
			api.reserved)
	}

	if len(api.tokenCalls) != 1 || !slices.Equal(api.tokenCalls[0], []job.ID{first}) {
		t.Errorf("the token call asked for %v, want the admitted job only", api.tokenCalls)
	}

	if len(fleet.created) != 1 || fleet.created[0].Name != "bk-"+controller+"-"+string(first) {
		t.Errorf("orchard saw %d machines, want only the one of the admitted job",
			len(fleet.created))
	}

	// The skipped job has its own line, and the admitted job has its line.
	var lines []buildkite.Notification
	for _, call := range api.notified {
		lines = append(lines, call...)
	}

	if n := countFor(lines, second); n != 1 {
		t.Errorf("the skipped job has %d notifications, want 1", n)
	}
}

// countFor counts the notifications of one job.
func countFor(lines []buildkite.Notification, id job.ID) int {
	n := 0

	for _, line := range lines {
		if line.JobID == id {
			n++
		}
	}

	return n
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
	mu      sync.Mutex
	refuse  string
	created []orchard.VM
}

func (r *refusing) CreateVM(_ context.Context, spec orchard.VM) error {
	if spec.Name == r.refuse {
		return errors.New("orchard refused the machine")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

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

	// The clock is one of the parts: a launcher without one would read the clock of the machine.
	if _, err := launcher.New(launcher.Options{}); err == nil || !strings.Contains(err.Error(), "needs a clock") {
		t.Errorf("New() with nothing = %v, want an error that names the clock", err)
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

// An unmatched job is left alone: no reservation, no finish call, and no slot. The controller
// writes one notification for it, and it does not write a second one at the next tick. The count
// of skipped jobs rises once.
func TestAnUnmatchedJobIsLeftAlone(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	log := &records{Handler: slog.DiscardHandler}
	started, reader := buildMeasured(t, api, fleet, log)

	rules := []string{"queue=macos-prod", "xcode=99"}
	_, err := match.Match(rules, []profile.Profile{pilot})

	var mismatch *match.Mismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("Match() = %v, want a mismatch", err)
	}

	decision := admission.Result{Unmatched: []admission.Unmatched{{
		Job:      buildkite.ScheduledJob{ID: second, Rules: rules},
		Mismatch: err,
	}}}

	for tick := range 2 {
		if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
			t.Fatalf("Launch() at tick %d = %v, want no error", tick, err)
		}
	}

	// Finish is not part of what the launcher uses, so no finish call can happen. The component
	// check of invariant I-8 covers the whole controller.
	if len(api.reserved) != 0 {
		t.Errorf("the controller reserved %v, want no reservation", api.reserved)
	}

	if len(api.tokenCalls) != 0 || len(fleet.created) != 0 {
		t.Errorf("the controller asked for %d tokens and %d machines, want none",
			len(api.tokenCalls), len(fleet.created))
	}

	if len(api.notified) != 1 || len(api.notified[0]) != 1 {
		t.Fatalf("buildkite saw %v, want one call with one notification", api.notified)
	}

	line := api.notified[0][0]
	if line.JobID != second || line.Detail == "" {
		t.Errorf("the notification reads %+v, want the job and a line", line)
	}

	if n := utf8.RuneCountInString(line.Detail); n > buildkite.NotificationLimit {
		t.Errorf("the notification has %d characters, want at most %d", n,
			buildkite.NotificationLimit)
	}

	if !strings.Contains(line.Detail, "xcode=99") {
		t.Errorf("the notification reads %q, want it to name the tags of the job", line.Detail)
	}

	if got := skipped(t, reader); got != 1 {
		t.Errorf("the counter of skipped jobs is %d, want 1", got)
	}

	// The full message goes to the log, once, with the job identifier and every profile.
	full := log.infoWith(string(second))
	if len(full) != 1 || !strings.Contains(full[0], pilot.Name) {
		t.Errorf("the log has %q for the job, want one line that names the profiles", full)
	}
}

// formatted is a reason for a skip that counts how often a part writes it out.
type formatted struct{ count *int }

func (f formatted) Error() string {
	*f.count++

	return "no profile carries this job"
}

// A job that stays in the queue meets admission at each tick. The texts of its skip cost
// allocations, and the queue can hold thousands of such jobs, so the launcher writes them out only
// for a job that has no line yet.
func TestTheTextOfASkipIsWrittenOnlyForAJobWithoutALine(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	var count int

	decision := admission.Result{Unmatched: []admission.Unmatched{{
		Job: buildkite.ScheduledJob{ID: second}, Mismatch: formatted{count: &count},
	}}}

	if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	first := count
	if first == 0 {
		t.Fatal("the launcher wrote no text for a job without a line")
	}

	for tick := range 3 {
		if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
			t.Fatalf("Launch() at tick %d = %v, want no error", tick, err)
		}
	}

	if count != first {
		t.Errorf("the launcher wrote the text %d times more for a job that has its line, want "+
			"none", count-first)
	}
}

// A job whose rules the matcher refuses is left alone in the same way. The profile of each case has
// exactly the tags of the refused rule, so only the refusal keeps the job from it.
func TestAJobWithARefusedRuleIsLeftAlone(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct{ rule, key, value string }{
		"a reserved tag": {"profile=other", "profile", "other"},
		"a comma":        {"xcode=27,gpu=yes", "xcode", "27,gpu=yes"},
		"a padded value": {"xcode= 27", "xcode", " 27"},
		"a wildcard":     {"xcode=2*", "xcode", "2*"},
		"a negation":     {"xcode!=27", "xcode", "27"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			api, fleet := &stacks{}, &machines{}
			started, reader := buildMeasured(t, api, fleet, &records{Handler: slog.DiscardHandler})

			carrier := pilot
			carrier.Match = map[string]string{c.key: c.value}

			decision := admission.Decide(admission.Request{
				Jobs: []buildkite.ScheduledJob{{
					ID: second, Rules: []string{"queue=macos-prod", c.rule},
				}},
				Profiles: []profile.Profile{carrier},
				Now:      time.Now(),
			})

			if len(decision.Admit) != 0 || len(decision.Unmatched) != 1 {
				t.Fatalf("Decide() admitted %d and left %d unmatched, want 0 and 1",
					len(decision.Admit), len(decision.Unmatched))
			}

			for range 2 {
				if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
					t.Fatalf("Launch() = %v, want no error", err)
				}
			}

			if len(api.reserved) != 0 || len(fleet.created) != 0 {
				t.Errorf("the controller reserved %v and made %d machines, want none",
					api.reserved, len(fleet.created))
			}

			if len(api.notified) != 1 || len(api.notified[0]) != 1 ||
				api.notified[0][0].JobID != second {
				t.Errorf("buildkite saw %v, want one notification for the job", api.notified)
			}

			if got := skipped(t, reader); got != 1 {
				t.Errorf("the counter of skipped jobs is %d, want 1", got)
			}
		})
	}
}

// unmatchedJobs gives a decision with n unmatched jobs, named by their number.
func unmatchedJobs(from, n int) admission.Result {
	var result admission.Result

	for i := from; i < from+n; i++ {
		id := job.ID(fmt.Sprintf("018f3b2c-1c4a-7e5b-9a3d-%012x", i))
		result.Unmatched = append(result.Unmatched, admission.Unmatched{
			Job: buildkite.ScheduledJob{ID: id}, Mismatch: errors.New("no profile carries this job"),
		})
	}

	return result
}

// notices gives every job that has a line, in the order of the lines.
func notices(api *stacks) []job.ID {
	var ids []job.ID

	for _, call := range api.notified {
		for _, line := range call {
			ids = append(ids, line.JobID)
		}
	}

	return ids
}

func idOf(number int) job.ID {
	return job.ID(fmt.Sprintf("018f3b2c-1c4a-7e5b-9a3d-%012x", number))
}

// After a complete listing, the set drops a job that left the list, so that a job that comes back
// is told again.
func TestACompleteListingEvictsTheJobsThatLeftTheList(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	one, two := unmatchedJobs(1, 1), unmatchedJobs(2, 1)
	both := admission.Result{Unmatched: append(slices.Clone(one.Unmatched), two.Unmatched...)}

	for _, decision := range []admission.Result{
		both, // both told
		both, // nobody again
		two,  // the first job left
		both, // the first job is back, and is told again
		{},   // both left
		both, // both told again
	} {
		if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
			t.Fatalf("Launch() = %v, want no error", err)
		}
	}

	lines := notices(api)

	if got := count(lines, idOf(1)); got != 3 {
		t.Errorf("the first job has %d notifications, want 3", got)
	}

	if got := count(lines, idOf(2)); got != 2 {
		t.Errorf("the second job has %d notifications, want 2", got)
	}
}

// After a truncated listing, a job that is not listed may only be hidden behind the pages, so the
// set keeps it, and the job gets no second line when it comes back into view.
func TestATruncatedListingEvictsNothing(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	one, two := unmatchedJobs(1, 1), unmatchedJobs(2, 1)
	both := admission.Result{Unmatched: append(slices.Clone(one.Unmatched), two.Unmatched...)}

	truncated := two
	truncated.Truncated = true

	for _, decision := range []admission.Result{
		both,      // both told
		truncated, // the first job is out of view, and stays in the set
		both,      // the first job is back in view: no second line
	} {
		if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
			t.Fatalf("Launch() = %v, want no error", err)
		}
	}

	if got := notices(api); len(got) != 2 {
		t.Errorf("the controller wrote %d lines, want 2: %v", len(got), got)
	}
}

func count(ids []job.ID, id job.ID) int {
	return len(slices.DeleteFunc(slices.Clone(ids), func(other job.ID) bool { return other != id }))
}

// The set has a cap, and over the cap it drops the jobs that it saw least recently. A job that it
// dropped gets a line when it comes back.
func TestTheSetOfNotifiedJobsDropsTheLeastRecentlySeenOverItsCap(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	full := unmatchedJobs(1, launcher.NotifiedLimit)

	more := unmatchedJobs(launcher.NotifiedLimit+1, 5)
	more.Truncated = true

	// The first jobs came first, so they are the least recently seen when the 5 new jobs arrive.
	back := unmatchedJobs(1, 10)
	back.Truncated = true

	for _, decision := range []admission.Result{full, more, back} {
		if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
			t.Fatalf("Launch() = %v, want no error", err)
		}
	}

	lines := notices(api)

	if len(lines) != launcher.NotifiedLimit+5+5 {
		t.Fatalf("the controller wrote %d lines, want %d: the cap, 5 new jobs, and 5 dropped jobs",
			len(lines), launcher.NotifiedLimit+5+5)
	}

	// Jobs 1 to 5 were dropped, and told again. Jobs 6 to 10 were kept, and told once.
	for number := 1; number <= 10; number++ {
		want := 1
		if number <= 5 {
			want = 2
		}

		if got := count(lines, idOf(number)); got != want {
			t.Errorf("job %d has %d lines, want %d", number, got, want)
		}
	}
}

// Buildkite takes at most 1000 lines in one request. More fresh jobs go in more calls.
func TestTheSkipNoticesGoInCallsOfAtMostOneRequestLimit(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	total := 2*buildkite.NotificationsPerRequest + 10

	if err := started.Launch(t.Context(), unmatchedJobs(1, total), inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	for number, call := range api.notified {
		if len(call) > buildkite.NotificationsPerRequest {
			t.Errorf("call %d has %d lines, want at most %d", number, len(call),
				buildkite.NotificationsPerRequest)
		}
	}

	if got := len(notices(api)); got != total || len(api.notified) != 3 {
		t.Errorf("the controller wrote %d lines in %d calls, want %d lines in 3", got,
			len(api.notified), total)
	}

	// A second tick writes none: every job is in the set.
	if err := started.Launch(t.Context(), unmatchedJobs(1, total), inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if got := len(notices(api)); got != total {
		t.Errorf("the controller wrote %d lines after 2 ticks, want %d", got, total)
	}
}

// Buildkite creates the valid lines of a call even when it refuses some. The set keeps the jobs
// that got a line, and the next tick tries only the refused jobs.
func TestAPartlyRefusedCallIsTriedAgainForTheRefusedJobsOnly(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{refuse: map[job.ID]bool{idOf(2): true}}, &machines{}
	started, reader := buildMeasured(t, api, fleet, &records{Handler: slog.DiscardHandler})

	decision := unmatchedJobs(1, 3)

	for range 2 {
		_ = started.Launch(t.Context(), decision, inventory.Snapshot{})
	}

	lines := notices(api)

	if count(lines, idOf(1)) != 1 || count(lines, idOf(3)) != 1 {
		t.Errorf("the accepted jobs got lines %v, want one line each", lines)
	}

	if got := count(lines, idOf(2)); got != 2 {
		t.Errorf("the refused job got %d lines, want 2 (one for each tick)", got)
	}

	// Only the accepted jobs are counted, and the refused job is counted when Buildkite takes it.
	if got := skipped(t, reader); got != 2 {
		t.Errorf("the counter of skipped jobs is %d, want 2", got)
	}

	// Buildkite takes the job later.
	api.refuse = nil

	_ = started.Launch(t.Context(), decision, inventory.Snapshot{})

	if got := skipped(t, reader); got != 3 {
		t.Errorf("the counter of skipped jobs is %d, want 3", got)
	}
}

// A slow notification call must not delay an admitted job, so the skip notices come after the
// reservation, the tokens, and the machines of the same tick.
func TestTheSkipNoticesComeAfterTheAdmittedJobsStart(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	decision := unmatchedJobs(5, 1)
	decision.Admit = candidates(first)

	if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	want := []string{"reserve", "tokens", "notify", "notify"}
	if !slices.Equal(api.order, want) {
		t.Fatalf("the calls came in the order %v, want %v", api.order, want)
	}

	if len(fleet.created) != 1 {
		t.Errorf("orchard saw %d machines, want 1", len(fleet.created))
	}

	// The first line is "VM requested" of the admitted job, and the last is the skip notice.
	if api.notified[0][0].JobID != first || api.notified[1][0].JobID != idOf(5) {
		t.Errorf("the lines went to %v, want the admitted job and then the skipped job",
			notices(api))
	}
}

// Exactly one request of lines makes one call, and one line more makes a second call with that
// line alone.
func TestTheSkipNoticesSplitAtTheRequestLimit(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		jobs  int
		calls []int
	}{
		"exactly one request": {buildkite.NotificationsPerRequest, []int{buildkite.NotificationsPerRequest}},
		"one line more":       {buildkite.NotificationsPerRequest + 1, []int{buildkite.NotificationsPerRequest, 1}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			api, fleet := &stacks{}, &machines{}
			started := build(t, api, fleet)

			if err := started.Launch(t.Context(), unmatchedJobs(1, c.jobs), inventory.Snapshot{}); err != nil {
				t.Fatalf("Launch() = %v, want no error", err)
			}

			var sizes []int
			for _, call := range api.notified {
				sizes = append(sizes, len(call))
			}

			if !slices.Equal(sizes, c.calls) {
				t.Errorf("the calls had %v lines, want %v", sizes, c.calls)
			}
		})
	}
}

// A batch that finds Buildkite not answering stops the batches behind it, because they would wait
// for the same Buildkite, each for its whole time limit. Their jobs try again at the next tick.
func TestABatchThatBuildkiteDoesNotAnswerStopsTheLaterBatches(t *testing.T) {
	t.Parallel()

	api := &stacks{failCall: map[int]error{0: context.DeadlineExceeded}}
	started := build(t, api, &machines{})

	if err := started.Launch(t.Context(), unmatchedJobs(1, 2*buildkite.NotificationsPerRequest+5),
		inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.notified) != 1 {
		t.Errorf("buildkite saw %d calls, want the one that did not answer", len(api.notified))
	}
}

// A batch that fails as a whole must not starve the batches behind it. The jobs of the later batch
// get their lines and enter the set, and the jobs of the failed batch try again at the next tick.
func TestAFailedBatchDoesNotStopTheLaterBatches(t *testing.T) {
	t.Parallel()

	total := buildkite.NotificationsPerRequest + 5
	api, fleet := &stacks{failCall: map[int]error{0: errors.New("buildkite refused the call")}}, &machines{}
	started := build(t, api, fleet)

	decision := unmatchedJobs(1, total)

	if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.notified) != 2 || len(api.notified[1]) != 5 {
		t.Fatalf("buildkite saw %d calls, want the failed batch and the batch of 5 jobs",
			len(api.notified))
	}

	// The next tick sends only the jobs of the failed batch: the 5 later jobs are in the set.
	if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.notified) != 3 || len(api.notified[2]) != buildkite.NotificationsPerRequest {
		t.Fatalf("buildkite saw %d calls, want a third call of %d lines", len(api.notified),
			buildkite.NotificationsPerRequest)
	}

	if got := count(notices(api), idOf(total)); got != 1 {
		t.Errorf("a job of the later batch has %d lines, want 1", got)
	}
}

// Each call has a time limit, so that a call that retries cannot hold the loop.
func TestEachSkipNoticeCallHasATimeLimit(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	if err := started.Launch(t.Context(), unmatchedJobs(1, 1), inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.bounded) != 1 || !api.bounded[0] {
		t.Errorf("the calls had deadlines %v, want one call with a deadline", api.bounded)
	}
}

// A notification that fails is written again at the next tick, because the job has no line yet.
func TestAFailedNotificationIsTriedAgain(t *testing.T) {
	t.Parallel()

	api, fleet := &stacks{notifyErr: errors.New("buildkite is not available")}, &machines{}
	started, reader := buildMeasured(t, api, fleet, &records{Handler: slog.DiscardHandler})

	decision := unmatchedJobs(1, 1)

	_ = started.Launch(t.Context(), decision, inventory.Snapshot{})

	if got := skipped(t, reader); got != 0 {
		t.Errorf("the counter of skipped jobs is %d after a failed notification, want 0", got)
	}

	api.notifyErr = nil

	if err := started.Launch(t.Context(), decision, inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if len(api.notified) != 2 {
		t.Errorf("buildkite saw %d notification calls, want 2", len(api.notified))
	}

	if got := skipped(t, reader); got != 1 {
		t.Errorf("the counter of skipped jobs is %d, want 1", got)
	}
}

// buildMeasured gives a launcher whose instruments a reader collects.
func buildMeasured(t *testing.T, api *stacks, fleet launcher.Machines,
	log *records,
) (*launcher.Launcher, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
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
		DefaultTimeouts:   defaultTimeouts,
		CallTimeout:       time.Second,
		Metrics:           instruments,
		Logger:            slog.New(log),
		Now:               time.Now,
	})
	if err != nil {
		t.Fatalf("New() = %v, want the launcher", err)
	}

	return started, reader
}

// skipped reads the counter of skipped jobs.
func skipped(t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	var total int64

	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "bk_agent_stack_orchard.job.skipped" {
				continue
			}

			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("the skipped counter has data of type %T, want a sum", m.Data)
			}

			for _, point := range sum.DataPoints {
				total += point.Value
			}
		}
	}

	return total
}

// buildAt gives a launcher that reads a clock that the test holds.
func buildAt(t *testing.T, api *stacks, fleet launcher.Machines, clock *time.Time) *launcher.Launcher {
	t.Helper()

	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetryMetrics(t, provider)
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	api.clock = clock

	started, err := launcher.New(launcher.Options{
		Stacks:            api,
		Orchard:           fleet,
		ControllerID:      controller,
		Queue:             "macos-prod",
		ReservationExpiry: 300 * time.Second,
		TokenLifetime:     1800 * time.Second,
		DefaultTimeouts:   defaultTimeouts,
		CallTimeout:       time.Second,
		Metrics:           instruments,
		Logger:            slog.New(slog.DiscardHandler),
		Now:               func() time.Time { return *clock },
	})
	if err != nil {
		t.Fatalf("New() = %v, want the launcher", err)
	}

	return started
}

// T-025. The extensions of a pass go to Buildkite in one call, with the expiry of a reservation.
func TestExtendHoldsEveryReservationInOneCall(t *testing.T) {
	t.Parallel()

	clock := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	api := &stacks{}
	started := buildAt(t, api, &machines{}, &clock)

	if err := started.Extend(t.Context(), []job.ID{first, second}); err != nil {
		t.Fatalf("Extend() = %v, want no error", err)
	}

	if len(api.reserved) != 1 || len(api.reserved[0]) != 2 {
		t.Fatalf("the launcher made %v reservations, want one call of two jobs", api.reserved)
	}

	if api.expiries[0] != 300*time.Second {
		t.Errorf("the extension asked for %s, want the expiry of 300s", api.expiries[0])
	}
}

// A reservation starts when Buildkite handles the call, which is after the call leaves. Its end
// therefore counts from the send, so that the renewal comes early and never late: an end counted
// from the answer could lie after the true end, and the renewal could then find the reservation
// already lapsed under a machine that is still starting.
func TestAReservationEndCountsFromTheSend(t *testing.T) {
	t.Parallel()

	sent := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	clock := sent
	started := buildAt(t, &stacks{}, &machines{}, &clock)

	if err := started.Extend(t.Context(), []job.ID{first}); err != nil {
		t.Fatalf("Extend() = %v, want no error", err)
	}

	until, known := started.ReservedUntil(first)
	if !known || !until.Equal(sent.Add(300*time.Second)) {
		t.Errorf("the reservation ends at %s, want %s: 300s from the send and not from the answer",
			until, sent.Add(300*time.Second))
	}
}

// The first reservation of a launch follows the same rule as an extension.
func TestTheFirstReservationEndCountsFromTheSend(t *testing.T) {
	t.Parallel()

	sent := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	clock := sent
	started := buildAt(t, &stacks{}, &machines{}, &clock)

	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	until, _ := started.ReservedUntil(first)
	if !until.Equal(sent.Add(300 * time.Second)) {
		t.Errorf("the reservation ends at %s, want %s", until, sent.Add(300*time.Second))
	}
}

// A job that the call does not extend keeps the end that it had. Its reservation lapses, and the
// reaper removes its machine by the row for a lapsed reservation.
func TestAJobThatWasNotExtendedKeepsItsEnd(t *testing.T) {
	t.Parallel()

	sent := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	clock := sent
	api := &stacks{}
	started := buildAt(t, api, &machines{}, &clock)

	if err := started.Launch(t.Context(), admission.Result{Admit: candidates(first)},
		inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	before, _ := started.ReservedUntil(first)

	api.reservation = buildkite.Reservation{NotReserved: []job.ID{first}}
	if err := started.Extend(t.Context(), []job.ID{first}); err != nil {
		t.Fatalf("Extend() = %v, want no error", err)
	}

	if after, _ := started.ReservedUntil(first); !after.Equal(before) {
		t.Errorf("the reservation now ends at %s, want the end it had, %s", after, before)
	}
}

// A token works for as long as the reaper lets the machine of its job start, and a minute more,
// never longer than buildkite.jobTokenLifetime. The jobs of one lifetime share one call.
func TestEachTokenLastsTheStartOfItsProfile(t *testing.T) {
	t.Parallel()

	quick := pilot
	quick.Name = "quick"
	quick.Timeouts = profile.Timeouts{
		Unplaced: time.Minute, Boot: 5 * time.Minute, Acquire: 2 * time.Minute,
	}

	slow := pilot
	slow.Name = "slow"
	slow.Timeouts = profile.Timeouts{
		Unplaced: 5 * time.Minute, Boot: 20 * time.Minute, Acquire: 4*time.Minute + 30*time.Second,
	}

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	admit := []admission.Candidate{
		{Job: buildkite.ScheduledJob{ID: first}, Profile: quick},
		{Job: buildkite.ScheduledJob{ID: second}, Profile: pilot},
		{Job: buildkite.ScheduledJob{ID: third}, Profile: slow},
		{Job: buildkite.ScheduledJob{ID: fourth}, Profile: quick},
	}

	err := started.Launch(t.Context(), admission.Result{Admit: admit}, inventory.Snapshot{})
	if err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	got := map[time.Duration][]job.ID{}
	for index, ids := range api.tokenCalls {
		got[api.lifetimes[index]] = ids
	}

	want := map[time.Duration][]job.ID{
		9 * time.Minute:  {first, fourth},
		23 * time.Minute: {second},
		30 * time.Minute: {third}, // 29.5 min and the margin, cut to the limit
	}

	if len(api.tokenCalls) != len(want) {
		t.Fatalf("the controller made %d token calls %v over %v, want one for each lifetime",
			len(api.tokenCalls), api.tokenCalls, api.lifetimes)
	}

	for lifetime, ids := range want {
		if !slices.Equal(got[lifetime], ids) {
			t.Errorf("the tokens of %s went to %v, want %v", lifetime, got[lifetime], ids)
		}
	}

	if len(fleet.created) != len(admit) {
		t.Errorf("orchard saw %d machines, want %d", len(fleet.created), len(admit))
	}
}

// The unplaced timeout of a machine counts from the token call of its job, which comes before the
// create and before the first listing (target design, section 7.3). Each job keeps the send time
// of the call that got its token, so the jobs of one lifetime share a time, and the jobs of the
// next call have a later one. A job with no token from this process has no time.
func TestEachJobKeepsTheSendTimeOfItsTokenCall(t *testing.T) {
	t.Parallel()

	quick := pilot
	quick.Name = "quick"
	quick.Timeouts = profile.Timeouts{
		Unplaced: time.Minute, Boot: 5 * time.Minute, Acquire: 2 * time.Minute,
	}

	begin := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	clock := begin
	started := buildAt(t, &stacks{}, &machines{}, &clock)

	admit := []admission.Candidate{
		{Job: buildkite.ScheduledJob{ID: first}, Profile: quick},
		{Job: buildkite.ScheduledJob{ID: second}, Profile: pilot},
		{Job: buildkite.ScheduledJob{ID: third}, Profile: quick},
	}

	if err := started.Launch(t.Context(), admission.Result{Admit: admit},
		inventory.Snapshot{}); err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	// Each call of the fake takes 3 s: the reservation, then the call of the quick lifetime, then
	// the call of the default one.
	for id, want := range map[job.ID]time.Time{
		first:  begin.Add(3 * time.Second),
		third:  begin.Add(3 * time.Second),
		second: begin.Add(6 * time.Second),
		fourth: {},
	} {
		if got := started.TokenAt(id); !got.Equal(want) {
			t.Errorf("TokenAt(%s) = %s, want %s, the send time of its token call", id, got, want)
		}
	}
}

// The configuration refuses start timeouts whose sum is negative, but the launcher does not trust
// that alone. A profile whose sum is not positive gets a token of the configured limit, and not
// one that has already expired.
func TestATokenNeverHasALifetimeBelowZero(t *testing.T) {
	t.Parallel()

	broken := pilot
	broken.Name = "broken"
	broken.Timeouts = profile.Timeouts{Boot: -time.Hour}

	api, fleet := &stacks{}, &machines{}
	started := build(t, api, fleet)

	admit := []admission.Candidate{{Job: buildkite.ScheduledJob{ID: first}, Profile: broken}}

	err := started.Launch(t.Context(), admission.Result{Admit: admit}, inventory.Snapshot{})
	if err != nil {
		t.Fatalf("Launch() = %v, want no error", err)
	}

	if !slices.Equal(api.lifetimes, []time.Duration{30 * time.Minute}) {
		t.Errorf("the token calls asked for %v, want one token of the configured limit, 30m",
			api.lifetimes)
	}
}

// A token call that fails ends the token calls of the tick. The jobs whose tokens came in the
// calls before it still get their machines, and the others keep reservations that lapse.
func TestAFailedTokenCallKeepsTheTokensBeforeIt(t *testing.T) {
	t.Parallel()

	quick := pilot
	quick.Name = "quick"
	quick.Timeouts = profile.Timeouts{
		Unplaced: time.Minute, Boot: 5 * time.Minute, Acquire: 2 * time.Minute,
	}

	api := &stacks{failTokenCall: map[int]error{1: errors.New("buildkite is not available")}}
	fleet := &machines{}
	started := build(t, api, fleet)

	admit := []admission.Candidate{
		{Job: buildkite.ScheduledJob{ID: first}, Profile: quick},
		{Job: buildkite.ScheduledJob{ID: second}, Profile: pilot},
	}

	if err := started.Launch(t.Context(), admission.Result{Admit: admit},
		inventory.Snapshot{}); err == nil {
		t.Error("Launch() = nil, want the error of the failed token call")
	}

	if len(fleet.created) != 1 || fleet.created[0].Name != vmspec.Name(controller, first) {
		t.Errorf("orchard saw %d machines, want only the machine of the job with a token",
			len(fleet.created))
	}
}

// The token calls of a tick, one for each lifetime, share the time budget of a step, as the creates
// do (target design, section 4). A tick then spends at most parallel.Rounds call limits on tokens,
// whatever the number of profiles. Here five lifetimes each take four fifths of a call limit: the
// first three calls answer inside the budget, and their jobs get machines, and the budget ends the
// fourth. The three calls leave 300 ms of the budget for a slow machine. The fourth call cannot
// answer inside the budget, because a timer never fires early.
func TestTheTokenCallsOfATickShareTheBudgetOfAStep(t *testing.T) {
	t.Parallel()

	const limit = 500 * time.Millisecond

	ids := []job.ID{first, second, third, fourth, fifth}
	admit := make([]admission.Candidate, 0, len(ids))

	for index, id := range ids {
		carrier := pilot
		carrier.Name = fmt.Sprintf("p%d", index)
		carrier.Timeouts = profile.Timeouts{
			Unplaced: time.Minute, Boot: time.Duration(index+1) * time.Minute, Acquire: time.Minute,
		}
		admit = append(admit, admission.Candidate{Job: buildkite.ScheduledJob{ID: id}, Profile: carrier})
	}

	api := &stacks{tokenDelay: limit * 4 / 5}
	fleet := &machines{}
	started := buildTimed(t, api, fleet, &records{}, limit)

	began := time.Now()
	err := started.Launch(t.Context(), admission.Result{Admit: admit}, inventory.Snapshot{})
	took := time.Since(began)

	if err == nil {
		t.Error("Launch() = nil, want the error of the token call that the budget ended")
	}

	if most := parallel.Budget(limit) + limit; took > most {
		t.Errorf("the token calls took %s, want them inside the budget of a step, %s", took,
			parallel.Budget(limit))
	}

	if len(api.tokenCalls) != parallel.Rounds+1 {
		t.Errorf("the controller made %d token calls, want %d: three inside the budget, and one "+
			"that the budget ended", len(api.tokenCalls), parallel.Rounds+1)
	}

	if len(fleet.created) != parallel.Rounds {
		t.Errorf("orchard saw %d machines, want %d, one for each token call that answered",
			len(fleet.created), parallel.Rounds)
	}
}
