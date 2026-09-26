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
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// unmatchedExitStatus is what the controller gives a job that no profile carries. The design
// states it, and a pipeline reads it as a failure of the job and not of the agent.
const unmatchedExitStatus = -1

// requested is the first line that a job page gets from this controller.
const requested = "VM requested"

// Stacks is what the launcher needs of Buildkite.
type Stacks interface {
	Reserve(ctx context.Context, ids []job.ID, expiry time.Duration) (buildkite.Reservation, error)
	Tokens(ctx context.Context, ids []job.ID, lifetime time.Duration) (buildkite.Tokens, error)
	Finish(ctx context.Context, id job.ID, exitStatus int, detail string) error
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

	// Now is for a test. A nil value gives time.Now.
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
}

// New builds the launcher.
func New(options Options) (*Launcher, error) {
	var problems []error

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
	if now == nil {
		now = time.Now
	}

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
	}, nil
}

// Profile gives the profile of a job that this process started, and empty text for a job that it
// did not.
func (l *Launcher) Profile(id job.ID) string {
	return l.held.profile(id)
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

	if len(decision.Admit) == 0 && len(decision.Unmatched) == 0 {
		return nil
	}

	reserved, err := l.reserve(ctx, decision)
	if err != nil {
		return err
	}

	// An unmatched job ends here. It uses no capacity, and a pipeline author reads the reason on
	// the job page instead of waiting for a machine that never comes.
	l.finishUnmatched(ctx, decision, reserved)

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

// reserve holds every job of the decision, in one call. The unmatched jobs travel with the
// admitted ones, because one call costs less of the rate limit than two.
func (l *Launcher) reserve(ctx context.Context, decision admission.Result) (map[job.ID]bool, error) {
	ctx, span := l.tracer.Start(ctx, "stacks.reserve")
	defer span.End()

	ids := make([]job.ID, 0, len(decision.Admit)+len(decision.Unmatched))

	for _, candidate := range decision.Admit {
		ids = append(ids, candidate.Job.ID)
	}

	for _, unmatched := range decision.Unmatched {
		ids = append(ids, unmatched.Job.ID)
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

	// A job that another stack holds is normal: every stack of the queue reads the same list.
	for _, id := range answer.NotReserved {
		l.logger.DebugContext(ctx, "another stack holds this job",
			telemetry.LogJob, string(id))
	}

	return held, nil
}

// finishUnmatched ends the jobs that no profile carries.
func (l *Launcher) finishUnmatched(ctx context.Context, decision admission.Result,
	reserved map[job.ID]bool,
) {
	for _, unmatched := range decision.Unmatched {
		if !reserved[unmatched.Job.ID] {
			continue
		}

		ctx, span := l.tracer.Start(telemetry.WithJob(ctx, unmatched.Job.ID), "stacks.finish_job")

		err := l.stacks.Finish(ctx, unmatched.Job.ID, unmatchedExitStatus, unmatched.Reason)
		if err != nil {
			span.RecordError(err)
			l.logger.ErrorContext(ctx, "the controller could not end an unmatched job",
				telemetry.LogJob, string(unmatched.Job.ID), "error", err)
			span.End()

			continue
		}

		l.metrics.Failed(ctx, "unmatched")
		l.logger.InfoContext(ctx, "the controller ended a job that no profile carries",
			telemetry.LogJob, string(unmatched.Job.ID), "reason", unmatched.Reason)
		span.End()
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

		jobCtx, span := l.tracer.Start(telemetry.WithJob(ctx, candidate.Job.ID),
			"orchard.vm.create")

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
	spec, err := vmspec.Build(l.request(ctx, candidate, token))
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
) vmspec.Request {
	request := vmspec.Request{
		ControllerID: l.controllerID,
		Queue:        l.queue,
		JobID:        candidate.Job.ID,
		JobToken:     token,
		Profile:      candidate.Profile,
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

	return request
}
