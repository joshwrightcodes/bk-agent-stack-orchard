// SPDX-License-Identifier: AGPL-3.0-or-later

// Package launcher starts the jobs that admission chose.
//
// The order of the calls is what keeps one job to one machine (target design, section 6.4): the
// reservation comes first, because a job that this controller does not hold may already belong
// to another stack; the token comes next, because a machine with no token would boot and find
// nothing to do; and the machine comes last.
//
// Every step can end for one job and continue for the rest. A job that another stack reserved, a
// job that got no token, and a job whose machine failed to start each drop out, and the jobs
// beside them go on.
package launcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/match"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// NotifiedLimit is the most job identifiers that the launcher remembers as notified. It is the
// most jobs that one read of the queue gives: 3 pages of 1000 (target design, section 6.3). A
// decision cannot name more unmatched jobs than the queue read gave.
const NotifiedLimit = 3000

// requested is the first line that a job page gets from this controller.
const requested = "VM requested"

// Stacks is what the launcher needs of Buildkite.
type Stacks interface {
	Reserve(ctx context.Context, ids []job.ID, expiry time.Duration) (buildkite.Reservation, error)
	Tokens(ctx context.Context, ids []job.ID, lifetime time.Duration) (buildkite.Tokens, error)
	Notify(ctx context.Context, notifications []buildkite.Notification) error
}

// Machines is what the launcher needs of Orchard.
type Machines interface {
	CreateVM(ctx context.Context, spec orchard.VM) error
}

// Options configures the launcher.
type Options struct {
	// Stacks and Orchard are the two systems that a launch talks to.
	Stacks  Stacks
	Orchard Machines

	// ControllerID names the machines, and Queue goes in the environment of the agent.
	ControllerID string
	Queue        string

	// ReservationExpiry is how long a reservation holds a job. It is short, because a controller
	// that stops between the reservation and the machine leaves the job held for this long.
	ReservationExpiry time.Duration

	// TokenLifetime is how long a job acquisition token works.
	TokenLifetime time.Duration

	// Environment reads the variables that carry the image credentials and the telemetry
	// headers of a profile. A nil value gives os.Getenv through the caller.
	Environment func(name string) string

	Metrics *telemetry.Metrics
	Logger  *slog.Logger
	Tracer  trace.Tracer

	// Now is the clock of the part. internal/app gives every part the same one.
	Now func() time.Time
}

// Launcher reserves jobs, gets their tokens, and asks Orchard for their machines.
type Launcher struct {
	stacks       Stacks
	machines     Machines
	controllerID string
	queue        string
	expiry       time.Duration
	lifetime     time.Duration
	environment  func(name string) string
	metrics      *telemetry.Metrics
	logger       *slog.Logger
	tracer       trace.Tracer
	now          func() time.Time

	// held is what this process knows about the jobs that it started: when the reservation of a
	// job ends, and which profile carries it. Neither is in Orchard, and a machine does not
	// carry the name of its profile, so this is the only place that has them. A restart loses
	// them, and the reaper treats an unknown reservation as one to extend at once.
	held *memory

	// notified holds the unmatched jobs that this process wrote a notification for, so that it
	// writes one line for each job and not one at each tick. A restart loses it, and the next
	// tick writes the line once more, which is acceptable. The mutex is for the readers outside
	// the loop.
	notifiedMu sync.Mutex
	notified   map[job.ID]struct{}
}

// New builds the launcher.
func New(options Options) (*Launcher, error) {
	var problems []error

	if options.Now == nil {
		problems = append(problems, errors.New("the launcher needs a clock"))
	}

	if options.Stacks == nil {
		problems = append(problems, errors.New("the launcher needs a Buildkite client"))
	}

	if options.Orchard == nil {
		problems = append(problems, errors.New("the launcher needs an Orchard client"))
	}

	if options.ControllerID == "" {
		problems = append(problems, errors.New("the launcher needs the controller identifier"))
	}

	if options.Queue == "" {
		problems = append(problems, errors.New("the launcher needs the queue"))
	}

	if options.Metrics == nil {
		problems = append(problems, errors.New("the launcher needs the instruments"))
	}

	if options.Logger == nil {
		problems = append(problems, errors.New("the launcher needs a logger"))
	}

	if options.ReservationExpiry <= 0 || options.TokenLifetime <= 0 {
		problems = append(problems, errors.New("the reservation expiry and the token lifetime "+
			"must both be more than zero"))
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	tracer := options.Tracer
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("")
	}

	now := options.Now

	environment := options.Environment
	if environment == nil {
		environment = func(string) string { return "" }
	}

	return &Launcher{
		stacks:       options.Stacks,
		machines:     options.Orchard,
		controllerID: options.ControllerID,
		queue:        options.Queue,
		expiry:       options.ReservationExpiry,
		lifetime:     options.TokenLifetime,
		environment:  environment,
		metrics:      options.Metrics,
		logger:       options.Logger,
		tracer:       tracer,
		now:          now,
		held:         newMemory(),
		notified:     make(map[job.ID]struct{}),
	}, nil
}

// Profile gives the profile of a job that this process started, and empty text for a job that it
// did not.
func (l *Launcher) Profile(id job.ID) string {
	return l.held.profile(id)
}

// Timeline is what this process knows of the start of a job that it admitted.
type Timeline struct {
	Profile     string
	ScheduledAt time.Time
	ReservedAt  time.Time
}

// Timeline gives the start of a job that this process admitted and reserved. A job of the process
// before a restart has none.
func (l *Launcher) Timeline(id job.ID) (Timeline, bool) {
	entry, known := l.held.timeline(id)

	return Timeline{Profile: entry.name, ScheduledAt: entry.scheduledAt, ReservedAt: entry.reservedAt},
		known
}

// Held gives the jobs that this process holds and whose machines a snapshot does not show yet,
// each with the profile of the machine that it asked for, or empty text when it asked for none.
//
// Admission reads it, because the inventory learns of a launch only at its next refresh. Until
// then a job that this process just started still looks like a waiting job with no machine, and
// the room that its machine will take still looks free.
func (l *Launcher) Held(fleet inventory.Snapshot) map[job.ID]string {
	return l.held.heldBeyond(fleet, l.now())
}

// ReservedUntil gives when the reservation of a job ends, for a job that this process reserved.
func (l *Launcher) ReservedUntil(id job.ID) (time.Time, bool) {
	return l.held.reservedUntil(id)
}

// Launch carries out the decision of admission.
func (l *Launcher) Launch(ctx context.Context, decision admission.Result,
	fleet inventory.Snapshot,
) error {
	defer l.held.keep(fleet)

	// An unmatched job is left alone: another stack or agent of the queue may carry it, so the
	// controller neither reserves nor finishes it, and it uses no capacity. A pipeline author
	// reads on the job page that this stack skipped the job. This runs for an empty list too,
	// because the set of notified jobs drops the jobs that left the list.
	l.skip(ctx, decision.Unmatched)

	if len(decision.Admit) == 0 {
		return nil
	}

	reserved, err := l.reserve(ctx, decision)
	if err != nil {
		return err
	}

	wanted := make([]admission.Candidate, 0, len(decision.Admit))

	for _, candidate := range decision.Admit {
		if reserved[candidate.Job.ID] {
			wanted = append(wanted, candidate)
		}
	}

	if len(wanted) == 0 {
		return nil
	}

	tokens, err := l.tokens(ctx, wanted)
	if err != nil {
		return err
	}

	l.create(ctx, wanted, tokens)

	return nil
}

// Extend holds the reservations of jobs whose machines are still starting, in one call, and
// records when each one now ends.
//
// One call for every job of a tick, because one call for each machine would spend the rate limit
// of the organisation on a busy fleet (target design, section 7.2). A job that the call does not
// extend keeps the end that it had: its reservation lapses, Buildkite schedules the job again, and
// the reaper removes its machine by the row for a lapsed reservation.
func (l *Launcher) Extend(ctx context.Context, ids []job.ID) error {
	if len(ids) == 0 {
		return nil
	}

	ctx, span := l.tracer.Start(ctx, "stacks.reserve")
	defer span.End()

	// The end counts from the send, for the reason that reserve gives.
	sent := l.now()

	answer, err := l.stacks.Reserve(ctx, ids, l.expiry)
	if err != nil {
		span.RecordError(err)

		return fmt.Errorf("extending %d reservations: %w", len(ids), err)
	}

	until := sent.Add(l.expiry)

	for _, id := range answer.Reserved {
		l.held.reserved(id, until, sent)
	}

	for _, id := range answer.NotReserved {
		l.logger.WarnContext(ctx, "buildkite did not extend the reservation of this job",
			telemetry.LogJob, string(id))
	}

	return nil
}

// reserve holds every admitted job of the decision, in one call.
func (l *Launcher) reserve(ctx context.Context, decision admission.Result) (map[job.ID]bool, error) {
	ctx, span := l.tracer.Start(ctx, "stacks.reserve")
	defer span.End()

	ids := make([]job.ID, 0, len(decision.Admit))

	for _, candidate := range decision.Admit {
		ids = append(ids, candidate.Job.ID)
	}

	// The reservation starts when Buildkite handles the call, which is after the call leaves. An
	// end counted from the send is therefore never later than the true end, so the renewal comes
	// early and never late. An end counted from the answer could be later than the true end, and a
	// renewal on that time could find the reservation already lapsed.
	sent := l.now()

	answer, err := l.stacks.Reserve(ctx, ids, l.expiry)
	if err != nil {
		span.RecordError(err)

		return nil, fmt.Errorf("reserving %d jobs: %w", len(ids), err)
	}

	until := sent.Add(l.expiry)
	held := make(map[job.ID]bool, len(answer.Reserved))

	for _, id := range answer.Reserved {
		held[id] = true

		l.held.reserved(id, until, sent)
	}

	// The first phase of a job ends with its reservation. The later phases are what the progress
	// of its machine shows, and they start from these times.
	for _, candidate := range decision.Admit {
		if !held[candidate.Job.ID] {
			continue
		}

		l.held.admitted(candidate.Job.ID, candidate.Job.ScheduledAt, sent)
		l.metrics.JobPhase(ctx, candidate.Profile.Name, telemetry.PhaseScheduledToReserved,
			sent.Sub(candidate.Job.ScheduledAt))
	}

	// A job that another stack holds is normal: every stack of the queue reads the same list.
	for _, id := range answer.NotReserved {
		l.logger.DebugContext(ctx, "another stack holds this job",
			telemetry.LogJob, string(id))
	}

	return held, nil
}

// Notified gives how many unmatched jobs the launcher remembers as notified.
func (l *Launcher) Notified() int {
	l.notifiedMu.Lock()
	defer l.notifiedMu.Unlock()

	return len(l.notified)
}

// skip tells each unmatched job that has no line yet, in one call, and forgets the jobs that are
// no longer unmatched.
//
// The set after the call holds the jobs of this decision that were notified, now or before. A job
// that left the list, or that a new configuration now matches, is not in it, so a job that comes
// back is told again. A decision that names more jobs than NotifiedLimit gets lines for the first
// jobs only, and one warning: a job that the set cannot hold would get a new line at each tick.
func (l *Launcher) skip(ctx context.Context, unmatched []admission.Unmatched) {
	l.notifiedMu.Lock()
	defer l.notifiedMu.Unlock()

	kept := make(map[job.ID]struct{}, len(unmatched))
	fresh := make([]admission.Unmatched, 0, len(unmatched))
	over := 0

	for _, entry := range unmatched {
		id := entry.Job.ID

		if _, done := l.notified[id]; done {
			kept[id] = struct{}{}

			continue
		}

		fresh = append(fresh, entry)
	}

	// The jobs that were told before are in kept already, so they come first in the bound.
	if room := NotifiedLimit - len(kept); len(fresh) > room {
		over = len(fresh) - max(room, 0)
		fresh = fresh[:max(room, 0)]
	}

	if over > 0 {
		l.logger.WarnContext(ctx, "more unmatched jobs than the controller remembers; "+
			"it leaves the extra jobs without a notification",
			"jobs", over, "limit", NotifiedLimit)
	}

	if len(fresh) > 0 {
		l.tell(ctx, fresh, kept)
	}

	l.notified = kept
}

// tell writes the notifications of the fresh jobs, and adds each one to kept when the call works.
// A call that fails leaves the jobs out, so that the next tick tries again.
func (l *Launcher) tell(ctx context.Context, fresh []admission.Unmatched,
	kept map[job.ID]struct{},
) {
	ctx, span := l.tracer.Start(ctx, "stacks.notify_skipped")
	defer span.End()

	at := l.now()
	lines := make([]buildkite.Notification, 0, len(fresh))

	for _, entry := range fresh {
		lines = append(lines, buildkite.Notification{
			JobID: entry.Job.ID, Detail: entry.Summary, At: at,
		})
	}

	if err := l.stacks.Notify(ctx, lines); err != nil {
		span.RecordError(err)
		l.logger.WarnContext(ctx, "the controller could not write the notifications of "+
			"the jobs that it skips", "jobs", len(fresh), "error", err)

		return
	}

	for _, entry := range fresh {
		kept[entry.Job.ID] = struct{}{}

		l.metrics.Skipped(ctx)
		l.logger.InfoContext(ctx, "the controller skips a job that no profile carries",
			telemetry.LogJob, string(entry.Job.ID),
			telemetry.LogPipeline, entry.Job.Pipeline,
			"reason", entry.Reason)
	}
}

// tokens gets one job acquisition token for each reserved job.
func (l *Launcher) tokens(ctx context.Context, wanted []admission.Candidate) (map[job.ID]secret.String, error) {
	ctx, span := l.tracer.Start(ctx, "stacks.issue_token")
	defer span.End()

	ids := make([]job.ID, 0, len(wanted))
	for _, candidate := range wanted {
		ids = append(ids, candidate.Job.ID)
	}

	answer, err := l.stacks.Tokens(ctx, ids, l.lifetime)
	if err != nil {
		span.RecordError(err)

		return nil, fmt.Errorf("getting the tokens of %d jobs: %w", len(ids), err)
	}

	issued := make(map[job.ID]secret.String, len(answer.Issued))
	for _, token := range answer.Issued {
		issued[token.JobID] = token.Token
	}

	// A job with no token gets no machine. Its reservation expires by itself, and admission
	// meets the job again at a later tick.
	for _, id := range answer.NotIssued {
		l.logger.WarnContext(ctx, "buildkite issued no token for this job",
			telemetry.LogJob, string(id))
	}

	return issued, nil
}

// create asks Orchard for the machine of each job that has a token.
func (l *Launcher) create(ctx context.Context, wanted []admission.Candidate,
	tokens map[job.ID]secret.String,
) {
	var lines []buildkite.Notification

	for _, candidate := range wanted {
		token, has := tokens[candidate.Job.ID]
		if !has {
			continue
		}

		jobCtx, span := telemetry.StartJob(ctx, l.tracer, candidate.Job.ID, "orchard.vm.create")

		if err := l.machine(jobCtx, candidate, token); err != nil {
			span.RecordError(err)
			l.logger.ErrorContext(jobCtx, "the controller could not start a machine",
				telemetry.LogJob, string(candidate.Job.ID),
				telemetry.LogProfile, candidate.Profile.Name, "error", err)
			span.End()

			continue
		}

		l.held.started(candidate.Job.ID, candidate.Profile.Name, l.now())
		lines = append(lines, buildkite.Notification{
			JobID: candidate.Job.ID, Detail: requested, At: l.now(),
		})

		l.logger.InfoContext(jobCtx, "the controller asked for a machine",
			telemetry.LogJob, string(candidate.Job.ID),
			telemetry.LogProfile, candidate.Profile.Name,
			telemetry.LogVM, vmspec.Name(l.controllerID, candidate.Job.ID))
		span.End()
	}

	if len(lines) == 0 {
		return
	}

	// A notification is a line on a job page. It must not stop a launch, so a failure here is a
	// log record and nothing more.
	if err := l.stacks.Notify(ctx, lines); err != nil {
		l.logger.WarnContext(ctx, "the controller could not write to the job pages",
			"error", err)
	}
}

// machine builds the specification of one job machine and asks Orchard for it.
func (l *Launcher) machine(ctx context.Context, candidate admission.Candidate,
	token secret.String,
) error {
	request, err := l.request(ctx, candidate, token)
	if err != nil {
		return err
	}

	spec, err := vmspec.Build(request)
	if err != nil {
		return err
	}

	err = l.machines.CreateVM(ctx, spec)

	// A machine of this name exists, which means that an earlier create arrived. The job has its
	// machine, which is what this step wanted.
	if errors.Is(err, orchard.ErrAlreadyExists) {
		return nil
	}

	return err
}

// request gives the specification request of one job.
func (l *Launcher) request(ctx context.Context, candidate admission.Candidate,
	token secret.String,
) (vmspec.Request, error) {
	// The tags of the job are the match map of its profile, because a job reaches a profile only
	// when the two are equal. The agent carries the tags of the job itself.
	tags, err := match.Tags(candidate.Job.Rules)
	if err != nil {
		return vmspec.Request{}, err
	}

	request := vmspec.Request{
		ControllerID: l.controllerID,
		Queue:        l.queue,
		JobID:        candidate.Job.ID,
		JobToken:     token,
		Profile:      candidate.Profile,
		Tags:         tags,
	}

	if credentials := candidate.Profile.VMCredentials; credentials != nil {
		request.ImageCredentials = &vmspec.Credentials{
			Username: l.environment(credentials.UsernameEnv),
			Password: secret.String(l.environment(credentials.PasswordEnv)),
		}
	}

	if candidate.Profile.OTel.Enabled {
		// The traceparent of the machine is the span of its creation, so the spans of the agent
		// join the trace of the job.
		request.TraceParent = telemetry.TraceParent(ctx)
		request.OTelHeaders = secret.String(l.environment(candidate.Profile.OTel.HeadersEnv))
	}

	return request, nil
}
