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
// beside them go on. An Orchard that does not answer is the exception: the creates that wait are
// not sent, because each one would wait for the same Orchard for its whole time limit.
package launcher

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/match"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/parallel"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// notifyTimeout is the time limit of one call that writes skip notices. The client retries a
// server failure and a rate limit inside the call, and without a limit that could hold the loop
// for a long time.
const notifyTimeout = 10 * time.Second

// NotifiedLimit is the most job identifiers that the launcher remembers as notified. One read of
// the queue gives at most 3000 jobs (3 pages of 1000, target design section 6.3), so the cap holds
// only while the listing stays truncated.
const NotifiedLimit = 10_000

// requested is the first line that a job page gets from this controller.
const requested = "VM requested"

// refused is the line of a job whose create failed. The job keeps its reservation with no machine,
// so Buildkite schedules it again when the reservation ends. The cause is in the log and in the
// trace of the job, and not on the page: an answer of Orchard can repeat the specification.
const refused = "VM request failed. The job returns to the queue when its reservation ends"

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

	// TokenLifetime is the longest that a job acquisition token works (buildkite.jobTokenLifetime).
	// Each token works for the start timeouts of the profile of its job and profile.TokenMargin,
	// inside this limit.
	TokenLifetime time.Duration

	// DefaultTimeouts are the timeouts of a profile that states none (defaults.timeouts). The
	// lifetime of a token reads the timeouts of its profile through them.
	DefaultTimeouts profile.Timeouts

	// CallTimeout is the time limit of one call to either API. The creates of one tick share a
	// time budget of a few of these limits, and a tick reserves only the jobs that the budget can
	// start.
	CallTimeout time.Duration

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
	defaults     profile.Timeouts
	budget       time.Duration
	environment  func(name string) string
	metrics      *telemetry.Metrics
	logger       *slog.Logger
	tracer       trace.Tracer
	now          func() time.Time

	// held is what this process knows about the jobs that it started: when the reservation of a
	// job ends, and which profile carries it. The end of a reservation is in neither Orchard nor
	// Buildkite, so this is the only place that has it. A restart loses both, the reaper treats
	// an unknown reservation as one to extend at once, and it reads the profile from the tags of
	// the machine.
	held *memory

	// notified holds the unmatched jobs that this process wrote a notification for, so that it
	// writes one line for each job and not one at each tick. Each job has the number of its last
	// sighting, which orders the eviction at the cap. A restart loses the set, and the next tick
	// writes the line once more, which is acceptable. Only the loop goroutine reads and writes
	// it.
	notified map[job.ID]uint64

	// seq counts the sightings of notified jobs. A higher number is a later sighting.
	seq uint64
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

	if options.DefaultTimeouts.StartSum() <= 0 {
		problems = append(problems, errors.New("the launcher needs the default timeouts, which "+
			"set the lifetime of a token"))
	}

	if options.CallTimeout <= 0 {
		problems = append(problems, errors.New("the launcher needs the time limit of a call, "+
			"which sets the time budget of its creates"))
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
		defaults:     options.DefaultTimeouts,
		budget:       parallel.Budget(options.CallTimeout),
		environment:  environment,
		metrics:      options.Metrics,
		logger:       options.Logger,
		tracer:       tracer,
		now:          now,
		held:         newMemory(),
		notified:     make(map[job.ID]uint64),
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

// TokenAt gives when this process sent the token call of a job, on the clock of the controller,
// and the zero time for a job that got no token from this process. The reaper counts the unplaced
// timeout of the machine of the job from it, because the token starts to expire at that call. A
// restart loses it, and the reaper then counts from the first listing of the machine.
func (l *Launcher) TokenAt(id job.ID) time.Time {
	return l.held.tokenSent(id)
}

// PerTick gives the most jobs that one tick reserves and starts. Admission admits no more, and
// Launch reserves no more.
//
// The creates of a tick run parallel.Limit at a time inside a budget of parallel.Rounds call
// limits, so this many creates finish inside the budget when each one takes its whole limit. A
// job past it would hold a reservation that the tick might not use, for five minutes, while
// another stack could start it. It waits in the queue for the next tick instead.
func (l *Launcher) PerTick() int {
	return parallel.PerStep
}

// Launch carries out the decision of admission.
func (l *Launcher) Launch(ctx context.Context, decision admission.Result,
	fleet inventory.Snapshot,
) error {
	defer l.held.keep(fleet)

	// An unmatched job is left alone: another stack or agent of the queue may carry it, so the
	// controller neither reserves nor finishes it, and it uses no capacity. A pipeline author
	// reads on the job page that this stack skipped the job. The notices come last, so that a
	// slow call cannot delay an admitted job, and they run for an empty list too, because the
	// set of notified jobs drops the jobs that left the list.
	defer l.skip(ctx, decision)

	// Admission admits no more than PerTick, and this keeps the promise of PerTick for any
	// caller.
	admit := decision.Admit[:min(len(decision.Admit), l.PerTick())]

	if len(admit) == 0 {
		return nil
	}

	reserved, err := l.reserve(ctx, admit)
	if err != nil {
		return err
	}

	wanted := make([]admission.Candidate, 0, len(admit))

	for _, candidate := range admit {
		if reserved[candidate.Job.ID] {
			wanted = append(wanted, candidate)
		}
	}

	if len(wanted) == 0 {
		return nil
	}

	// A token call that fails leaves the tokens of the calls before it, and their jobs still get
	// their machines.
	tokens, err := l.tokens(ctx, wanted)
	l.create(ctx, wanted, tokens)

	return err
}

// Extend holds the reservations of jobs whose machines are still starting, in one call, and
// records when each one now ends.
//
// One call for every job of a tick, because one call for each machine would spend the rate limit
// of the organisation on a busy fleet (target design, section 7.2). A job that the call does not
// extend keeps the end that it had, and the launcher records the refusal. Another stack can hold
// such a job, so the reaper removes its machine by row 6 once a later job state shows it still
// reserved, and it sends no finish.
func (l *Launcher) Extend(ctx context.Context, ids []job.ID) error {
	if len(ids) == 0 {
		return nil
	}

	spans := telemetry.StartJobs(ctx, l.tracer, ids, "stacks.reserve",
		trace.WithAttributes(attribute.Bool(telemetry.SpanExtension, true)))
	defer spans.End()

	// The end counts from the send, for the reason that reserve gives.
	sent := l.now()

	answer, err := l.stacks.Reserve(ctx, ids, l.expiry)
	if err != nil {
		spans.Fail(err)

		return fmt.Errorf("extending %d reservations: %w", len(ids), err)
	}

	until := sent.Add(l.expiry)

	for _, id := range answer.Reserved {
		l.held.reserved(id, until, sent)
		spans.Span(id).SetAttributes(attribute.Bool(telemetry.SpanReserved, true))
	}

	for _, id := range answer.NotReserved {
		l.held.refused(id, sent)
		spans.Span(id).SetAttributes(attribute.Bool(telemetry.SpanReserved, false))
		l.logger.WarnContext(spans.Context(ctx, id), "buildkite did not extend the reservation of "+
			"this job, so the reaper removes its machine without a finish",
			telemetry.LogJob, string(id))
	}

	return nil
}

// RefusedAt gives when this process sent an extension of the reservation of a job that Buildkite
// answered with not_reserved, on the clock of the controller. It gives the zero time when no
// extension was refused since the last reservation that Buildkite granted, and after a restart.
func (l *Launcher) RefusedAt(id job.ID) time.Time {
	return l.held.refusal(id)
}

// reserve holds every admitted job, in one call.
func (l *Launcher) reserve(ctx context.Context, admit []admission.Candidate) (map[job.ID]bool, error) {
	ids := make([]job.ID, 0, len(admit))

	for _, candidate := range admit {
		ids = append(ids, candidate.Job.ID)
	}

	spans := telemetry.StartJobs(ctx, l.tracer, ids, "stacks.reserve")
	defer spans.End()

	// The reservation starts when Buildkite handles the call, which is after the call leaves. An
	// end counted from the send is therefore never later than the true end, so the renewal comes
	// early and never late. An end counted from the answer could be later than the true end, and a
	// renewal on that time could find the reservation already lapsed.
	sent := l.now()

	answer, err := l.stacks.Reserve(ctx, ids, l.expiry)
	if err != nil {
		spans.Fail(err)

		return nil, fmt.Errorf("reserving %d jobs: %w", len(ids), err)
	}

	until := sent.Add(l.expiry)
	taken := make(map[job.ID]bool, len(answer.Reserved))

	for _, id := range answer.Reserved {
		taken[id] = true

		l.held.reserved(id, until, sent)
		spans.Span(id).SetAttributes(attribute.Bool(telemetry.SpanReserved, true))
	}

	// The first phase of a job ends with its reservation. The later phases are what the progress
	// of its machine shows, and they start from these times.
	for _, candidate := range admit {
		if !taken[candidate.Job.ID] {
			continue
		}

		l.held.admitted(candidate.Job.ID, candidate.Job.ScheduledAt, sent)
		l.metrics.JobPhase(ctx, candidate.Profile.Name, telemetry.PhaseScheduledToReserved,
			sent.Sub(candidate.Job.ScheduledAt))
	}

	// A job that another stack holds is normal: every stack of the queue reads the same list.
	for _, id := range answer.NotReserved {
		spans.Span(id).SetAttributes(attribute.Bool(telemetry.SpanReserved, false))
		l.logger.DebugContext(spans.Context(ctx, id), "another stack holds this job",
			telemetry.LogJob, string(id))
	}

	return taken, nil
}

// skip tells each unmatched job that has no line yet, and it keeps the set of notified jobs.
//
// A job enters the set when Buildkite takes its line. After a whole listing, a job that is not
// unmatched in this decision leaves the set, so a job that comes back is told again. After a
// truncated listing nothing leaves, because a job that is not listed may be hidden behind the
// pages. Over NotifiedLimit the jobs seen least recently leave.
func (l *Launcher) skip(ctx context.Context, decision admission.Result) {
	fresh := make([]admission.Unmatched, 0, len(decision.Unmatched))
	listed := make(map[job.ID]bool, len(decision.Unmatched))

	for _, entry := range decision.Unmatched {
		id := entry.Job.ID
		listed[id] = true

		if _, done := l.notified[id]; done {
			l.see(id)

			continue
		}

		fresh = append(fresh, entry)
	}

	var (
		failed int
		first  error
	)

	for start := 0; start < len(fresh); start += buildkite.NotificationsPerRequest {
		end := min(start+buildkite.NotificationsPerRequest, len(fresh))

		// A batch that fails as a whole does not stop the later ones: a refusal that never
		// changes would starve every job behind it. A Buildkite that does not answer does stop
		// them, because each one would wait for it for its whole time limit.
		if err := l.tell(ctx, fresh[start:end]); err != nil {
			failed++
			first = cmp.Or(first, err)

			if retry.Unavailable(err) {
				break
			}
		}
	}

	if failed > 0 {
		l.logger.WarnContext(ctx, "the controller could not write some notifications of the "+
			"jobs that it skips, and it tries again at the next tick",
			"failedCalls", failed, "error", first)
	}

	if !decision.Truncated {
		for id := range l.notified {
			if !listed[id] {
				delete(l.notified, id)
			}
		}
	}

	l.trim()
}

// see marks a job as seen now.
func (l *Launcher) see(id job.ID) {
	l.seq++
	l.notified[id] = l.seq
}

// trim drops the jobs that the set saw least recently, down to NotifiedLimit.
func (l *Launcher) trim() {
	over := len(l.notified) - NotifiedLimit
	if over <= 0 {
		return
	}

	ids := make([]job.ID, 0, len(l.notified))
	for id := range l.notified {
		ids = append(ids, id)
	}

	slices.SortFunc(ids, func(a, b job.ID) int { return cmp.Compare(l.notified[a], l.notified[b]) })

	for _, id := range ids[:over] {
		delete(l.notified, id)
	}
}

// tell writes the notifications of up to one request of fresh jobs, and puts each accepted job in
// the set. A call that fails as a whole puts no job in the set, so its jobs try again at the next
// tick. A call that Buildkite refuses in part keeps the valid lines, so only the refused jobs try
// again. The call has its own time limit, so that a client that retries cannot hold the loop.
func (l *Launcher) tell(ctx context.Context, fresh []admission.Unmatched) error {
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()

	at := l.now()
	lines := make([]buildkite.Notification, 0, len(fresh))
	ids := make([]job.ID, 0, len(fresh))

	for _, entry := range fresh {
		lines = append(lines, buildkite.Notification{
			JobID: entry.Job.ID, Detail: entry.Summary(), At: at,
		})
		ids = append(ids, entry.Job.ID)
	}

	spans := telemetry.StartJobs(ctx, l.tracer, ids, "stacks.notify_skipped")
	defer spans.End()

	refused := map[int]bool{}

	if err := l.stacks.Notify(ctx, lines); err != nil {
		var partial *buildkite.RefusedError

		if !errors.As(err, &partial) {
			spans.Fail(err)

			return err
		}

		l.logger.WarnContext(ctx, "buildkite refused some notifications of the jobs that "+
			"the controller skips", "refused", len(partial.Indexes), "jobs", len(fresh),
			"reason", partial.Reason)

		// A refusal with no index is a refusal of every line.
		if len(partial.Indexes) == 0 {
			return nil
		}

		for _, index := range partial.Indexes {
			refused[index] = true
		}
	}

	for index, entry := range fresh {
		if refused[index] {
			spans.Span(entry.Job.ID).SetAttributes(attribute.Bool(telemetry.SpanRefused, true))

			continue
		}

		l.see(entry.Job.ID)
		l.metrics.Skipped(ctx)
		l.logger.InfoContext(spans.Context(ctx, entry.Job.ID),
			"the controller skips a job that no profile carries",
			telemetry.LogJob, string(entry.Job.ID),
			telemetry.LogPipeline, entry.Job.Pipeline,
			telemetry.SpanMismatch, entry.Reason())
	}

	return nil
}

// lifetimeOf is how long the token of a job of the profile works: the time that the reaper gives
// its machine to start, and profile.TokenMargin, inside the configured limit.
//
// The reservation of the job bounds the token already: Buildkite ends a token when the reservation
// of its job expires, and a finish ends the job. The lifetime is a second bound, for the times
// when the controller holds a reservation longer than the start of the machine needs, such as while
// its snapshot of Orchard is stale, and for a Buildkite that did not end the token with the
// reservation.
//
// The configuration refuses start timeouts whose sum is negative or wraps. A sum that is not
// positive still gives the configured limit, and never a token that has expired.
func (l *Launcher) lifetimeOf(carrier profile.Profile) time.Duration {
	lifetime := carrier.EffectiveTimeouts(l.defaults).StartSum() + profile.TokenMargin
	if lifetime <= 0 {
		return l.lifetime
	}

	return min(lifetime, l.lifetime)
}

// tokens gets one job acquisition token for each reserved job, in one call for each lifetime. A
// call that fails ends the calls, and the tokens of the calls before it stay.
//
// The calls share the time budget of a step, as the creates do, so that the longest tick does not
// grow with the number of profiles (target design, section 4). The calls are quick when Buildkite
// answers, so every lifetime gets its call. When each call takes most of its limit, the budget
// ends the later calls, and their jobs keep reservations that lapse.
func (l *Launcher) tokens(ctx context.Context, wanted []admission.Candidate) (map[job.ID]secret.String, error) {
	ctx, cancel := context.WithTimeout(ctx, l.budget)
	defer cancel()

	var lifetimes []time.Duration

	groups := map[time.Duration][]job.ID{}

	for _, candidate := range wanted {
		lifetime := l.lifetimeOf(candidate.Profile)
		if _, seen := groups[lifetime]; !seen {
			lifetimes = append(lifetimes, lifetime)
		}

		groups[lifetime] = append(groups[lifetime], candidate.Job.ID)
	}

	issued := make(map[job.ID]secret.String, len(wanted))

	for _, lifetime := range lifetimes {
		got, err := l.issue(ctx, groups[lifetime], lifetime, issued)
		if err != nil {
			return issued, err
		}

		l.held.tokened(got.ids, got.sent)
	}

	return issued, nil
}

// sending is the jobs that one token call got tokens for, and when the call left.
type sending struct {
	ids  []job.ID
	sent time.Time
}

// issue makes one token call for jobs of one lifetime, and adds the tokens that it got to issued.
// Each job has a span of the call in its own trace.
func (l *Launcher) issue(ctx context.Context, ids []job.ID, lifetime time.Duration,
	issued map[job.ID]secret.String,
) (sending, error) {
	spans := telemetry.StartJobs(ctx, l.tracer, ids, "stacks.issue_token")
	defer spans.End()

	// A token starts to expire when Buildkite handles the call, which is after the call leaves. A
	// time taken from the send is therefore never later than the issue, so the unplaced timeout
	// that counts from it ends inside the token (target design, section 7.3).
	sent := l.now()

	answer, err := l.stacks.Tokens(ctx, ids, lifetime)
	if err != nil {
		spans.Fail(err)

		return sending{}, fmt.Errorf("getting the tokens of %d jobs: %w", len(ids), err)
	}

	got := sending{ids: make([]job.ID, 0, len(answer.Issued)), sent: sent}

	for _, token := range answer.Issued {
		issued[token.JobID] = token.Token
		got.ids = append(got.ids, token.JobID)
		spans.Span(token.JobID).SetAttributes(attribute.Bool(telemetry.SpanTokenIssued, true))
	}

	// A job with no token gets no machine. Its reservation expires by itself, and admission meets
	// the job again at a later tick.
	for _, id := range answer.NotIssued {
		spans.Span(id).SetAttributes(attribute.Bool(telemetry.SpanTokenIssued, false))
		l.logger.WarnContext(spans.Context(ctx, id), "buildkite issued no token for this job",
			telemetry.LogJob, string(id))
	}

	return got, nil
}

// create asks Orchard for the machine of each job that has a token.
//
// The creates run parallel.Limit at a time, inside the time budget of the step, so that a burst
// of jobs costs the time of a few creates and not the sum of them (target design, section 4). A
// create that finds Orchard not answering stops the creates that wait, because they would wait
// for the same Orchard: their jobs keep their reservations, which lapse, and admission meets
// them again. The outcomes change the memory of the launcher only on the goroutine of the loop,
// after every create has ended.
func (l *Launcher) create(ctx context.Context, wanted []admission.Candidate,
	tokens map[job.ID]secret.String,
) {
	type order struct {
		candidate admission.Candidate
		token     secret.String
	}

	orders := make([]order, 0, len(wanted))

	for _, candidate := range wanted {
		token, has := tokens[candidate.Job.ID]
		if !has {
			l.metrics.Started(ctx, candidate.Profile.Name, telemetry.StartNoToken)

			continue
		}

		orders = append(orders, order{candidate: candidate, token: token})
	}

	step, cancel := context.WithTimeout(ctx, l.budget)
	defer cancel()

	outcomes := parallel.Each(step, parallel.Limit, orders, func(ctx context.Context, o order) error {
		return l.start(ctx, o.candidate, o.token)
	}, retry.Unavailable)

	var (
		lines []buildkite.Notification
		left  int
	)

	for i, outcome := range outcomes {
		id, name := orders[i].candidate.Job.ID, orders[i].candidate.Profile.Name

		switch {
		case !outcome.Done:
			left++

			l.metrics.Started(ctx, name, telemetry.StartNotSent)
		case outcome.Value == nil:
			l.held.started(id, name, l.now())
			l.metrics.Started(ctx, name, telemetry.StartCreated)
			lines = append(lines, buildkite.Notification{JobID: id, Detail: requested, At: l.now()})
		default:
			l.metrics.Started(ctx, name, telemetry.StartCreateFailed)
			lines = append(lines, buildkite.Notification{JobID: id, Detail: refused, At: l.now()})
		}
	}

	if left > 0 {
		l.logger.WarnContext(ctx, "the controller asked for no machine for some reserved jobs, "+
			"because Orchard did not answer or the step ran out of time, and their reservations "+
			"lapse", "jobs", left)
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

// start asks Orchard for the machine of one job, in a span of its own. It runs on a goroutine of
// the step, so it records nothing in the memory of the launcher.
func (l *Launcher) start(ctx context.Context, candidate admission.Candidate,
	token secret.String,
) error {
	ctx, span := telemetry.StartJob(ctx, l.tracer, candidate.Job.ID, "orchard.vm.create")
	defer span.End()

	if err := l.machine(ctx, candidate, token); err != nil {
		telemetry.Fail(span, err)
		l.logger.ErrorContext(ctx, "the controller could not start a machine",
			telemetry.LogJob, string(candidate.Job.ID),
			telemetry.LogProfile, candidate.Profile.Name, "error", err)

		return err
	}

	l.logger.InfoContext(ctx, "the controller asked for a machine",
		telemetry.LogJob, string(candidate.Job.ID),
		telemetry.LogProfile, candidate.Profile.Name,
		telemetry.LogVM, vmspec.Name(l.controllerID, candidate.Job.ID))

	return nil
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
