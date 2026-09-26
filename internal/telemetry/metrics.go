package telemetry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// prefix names this controller in a backend that holds the telemetry of many systems. The
// semantic conventions require a prefix on anything that they do not define, so that a name of
// this project cannot collide with a name that OpenTelemetry gives a meaning later.
const prefix = "bk_agent_stack_orchard."

// The attributes that this project defines. Each one carries the prefix, because "state",
// "reason", and "source" are words that another system in the same backend also uses.
//
// Where a convention exists, this file uses it instead: cicd.worker.state for the state of a
// machine that runs an agent, cicd.system.component for the system that answered, and
// error.type for the cause of a failure.
const (
	attributeProfile      = prefix + "profile"
	attributePool         = prefix + "pool"
	attributeSlotState    = prefix + "slot.state"
	attributeVMPhase      = prefix + "vm.phase"
	attributeDeleteReason = prefix + "vm.delete.reason"
	attributeJobPhase     = prefix + "job.phase"
	attributeStep         = prefix + "tick.step"
	attributeSource       = prefix + "source"
)

// The attributes of the job.match span: the profile that carries the job, or none.
const (
	SpanProfile = prefix + "profile"
	SpanMatched = prefix + "job.matched"
)

// The attributes of the reaper.decide span (target design, section 10.2).
const (
	SpanVMPhase  = prefix + "vm.phase"
	SpanJobState = prefix + "job.state"
	SpanDecision = prefix + "reaper.decision"
	SpanReason   = prefix + "reaper.reason"
	SpanRow      = prefix + "reaper.row"
)

// SlotState is what a slot of the fleet does.
type SlotState string

// The states of a slot. They add up: total is used plus free.
const (
	SlotsTotal SlotState = "total"
	SlotsUsed  SlotState = "used"
	SlotsFree  SlotState = "free"
)

// VMPhase is how far a virtual machine has come.
type VMPhase string

// The phases of a virtual machine. Unplaced means that Orchard holds the machine but no worker
// runs it yet, which is the phase that says the fleet is full.
const (
	VMUnplaced VMPhase = "unplaced"
	VMBooting  VMPhase = "booting"
	VMRunning  VMPhase = "running"
	VMFailed   VMPhase = "failed"
)

// WorkerState is the state of a machine that can run an agent, as the CI/CD conventions define
// it. The worker of those conventions is the thing that does the work of a job, which here is
// the virtual machine and not the Mac host that carries it.
type WorkerState string

// The states of a worker, which are the values that the conventions name.
const (
	WorkerAvailable WorkerState = "available"
	WorkerBusy      WorkerState = "busy"
	WorkerOffline   WorkerState = "offline"
)

// JobPhase is one part of the way from a scheduled job to a running agent.
type JobPhase string

// The phases of a job. The controller observes them at poll intervals, so each duration has a
// tolerance of one job-states interval.
const (
	PhaseScheduledToReserved JobPhase = "scheduled_to_reserved"
	PhaseReservedToPlaced    JobPhase = "reserved_to_placed"
	PhasePlacedToRunning     JobPhase = "placed_to_running"
	PhaseRunningToAccepted   JobPhase = "running_to_accepted"
	PhaseTotal               JobPhase = "total"
)

// Component names one of the two systems that the controller calls. It is the value of
// cicd.system.component.
type Component string

// The systems that the controller calls.
const (
	ComponentBuildkite Component = "buildkite"
	ComponentOrchard   Component = "orchard"
)

// Source is one supply of observed state, whose age decides whether the controller may act.
type Source string

// The sources of observed state.
const (
	SourceScheduledJobs Source = "scheduled_jobs"
	SourceJobStates     Source = "job_states"
	SourceInventory     Source = "inventory"
)

// Slot names one series of the slot count.
type Slot struct {
	Pool  string
	State SlotState
}

// VM names one series of the machine count.
type VM struct {
	Profile string
	Phase   VMPhase
}

// Observation is the state of the fleet at one tick, as the controller observed it.
//
// The counts of the fleet are levels and not events, so the conventions make them up-down
// counters, and a level that a controller computes from observed state belongs in a callback.
// The controller therefore gives the whole observation at each tick, and the callback reports
// it. A series that leaves an observation stops, which a value that a caller sets one at a time
// could not do: a profile that no longer exists would report its last count for ever.
type Observation struct {
	// Waiting is the jobs of each profile that wait for a machine.
	Waiting map[string]int64

	// OldestWaiting is the age of the oldest waiting job of each profile.
	OldestWaiting map[string]time.Duration

	// Slots is the slots of each pool in each state.
	Slots map[Slot]int64

	// VMs is the machines of each profile in each phase.
	VMs map[VM]int64

	// Workers is the machines that can run an agent, in the states of the conventions.
	Workers map[WorkerState]int64

	// Synced is when each source last answered. The callback reports the age of that answer,
	// and not the time itself, so that the alert reads a number that needs no clock of its own.
	Synced map[Source]time.Time
}

// Metrics holds the instruments of the target design, section 10.1.
//
// No method takes a job identifier or the name of a virtual machine, and no field of an
// Observation holds one. Invariant I-7 is therefore a property of these types: a caller cannot
// put a job in a metric attribute, because nothing here accepts one. A job belongs in a trace
// and in a log record, which hold one job each. A metric series is for all of them, and one
// series for each job would multiply the cost of the backend for no answer that a person needs.
type Metrics struct {
	waiting     metric.Int64ObservableUpDownCounter
	oldest      metric.Float64ObservableGauge
	slots       metric.Int64ObservableUpDownCounter
	vms         metric.Int64ObservableUpDownCounter
	workers     metric.Int64ObservableUpDownCounter
	staleness   metric.Float64ObservableGauge
	unmatched   metric.Int64Counter
	failed      metric.Int64Counter
	deletions   metric.Int64Counter
	errors      metric.Int64Counter
	jobPhase    metric.Float64Histogram
	tick        metric.Float64Histogram
	registered  metric.Registration
	mu          sync.RWMutex
	observation Observation
}

// NewMetrics builds the instruments on a meter and registers the callback that reports the
// levels of the fleet.
func NewMetrics(meter metric.Meter) (*Metrics, error) {
	m := &Metrics{}

	if err := m.observable(meter); err != nil {
		return nil, err
	}

	if err := m.synchronous(meter); err != nil {
		return nil, err
	}

	registered, err := meter.RegisterCallback(m.report,
		m.waiting, m.oldest, m.slots, m.vms, m.workers, m.staleness)
	if err != nil {
		return nil, fmt.Errorf("registering the callback of the fleet levels: %w", err)
	}

	m.registered = registered

	return m, nil
}

// observable builds the instruments whose values the callback reports.
func (m *Metrics) observable(meter metric.Meter) error {
	var err error

	m.waiting, err = meter.Int64ObservableUpDownCounter(prefix+"job.waiting.count",
		metric.WithUnit("{job}"),
		metric.WithDescription("Jobs of the queue that wait for a virtual machine."))
	if err != nil {
		return fmt.Errorf("building the instrument of the waiting jobs: %w", err)
	}

	m.oldest, err = meter.Float64ObservableGauge(prefix+"job.waiting.oldest.age",
		metric.WithUnit("s"),
		metric.WithDescription("Age of the oldest job that waits. The alert of QA-02 reads it."))
	if err != nil {
		return fmt.Errorf("building the instrument of the oldest waiting job: %w", err)
	}

	m.slots, err = meter.Int64ObservableUpDownCounter(prefix+"slot.count",
		metric.WithUnit("{slot}"),
		metric.WithDescription("Virtual machine slots of a pool, by state."))
	if err != nil {
		return fmt.Errorf("building the instrument of the slots: %w", err)
	}

	m.vms, err = meter.Int64ObservableUpDownCounter(prefix+"vm.count",
		metric.WithUnit("{vm}"),
		metric.WithDescription("Virtual machines of this controller, by profile and phase."))
	if err != nil {
		return fmt.Errorf("building the instrument of the machines: %w", err)
	}

	m.workers, err = meter.Int64ObservableUpDownCounter("cicd.worker.count",
		metric.WithUnit("{worker}"),
		metric.WithDescription("Machines that can run an agent, by state."))
	if err != nil {
		return fmt.Errorf("building the instrument of the workers: %w", err)
	}

	m.staleness, err = meter.Float64ObservableGauge(prefix+"source.staleness",
		metric.WithUnit("s"),
		metric.WithDescription("Age of the last answer of a source of observed state. The "+
			"readiness alert reads it."))
	if err != nil {
		return fmt.Errorf("building the instrument of the source age: %w", err)
	}

	return nil
}

// synchronous builds the instruments that a caller records as the event happens.
func (m *Metrics) synchronous(meter metric.Meter) error {
	var err error

	m.unmatched, err = meter.Int64Counter(prefix+"job.unmatched",
		metric.WithUnit("{job}"),
		metric.WithDescription("Jobs of the queue that no profile matches."))
	if err != nil {
		return fmt.Errorf("building the instrument of the unmatched jobs: %w", err)
	}

	m.failed, err = meter.Int64Counter(prefix+"job.failed",
		metric.WithUnit("{job}"),
		metric.WithDescription("Jobs that the controller ended, by cause."))
	if err != nil {
		return fmt.Errorf("building the instrument of the failed jobs: %w", err)
	}

	m.deletions, err = meter.Int64Counter(prefix+"vm.deletions",
		metric.WithUnit("{vm}"),
		metric.WithDescription("Deleted virtual machines, by the reason of the decision table."))
	if err != nil {
		return fmt.Errorf("building the instrument of the deletions: %w", err)
	}

	m.errors, err = meter.Int64Counter("cicd.system.errors",
		metric.WithUnit("{error}"),
		metric.WithDescription("Failures of a system that the controller calls."))
	if err != nil {
		return fmt.Errorf("building the instrument of the system errors: %w", err)
	}

	m.jobPhase, err = meter.Float64Histogram(prefix+"job.phase.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Time of one phase of a job, from scheduled to an agent that "+
			"accepted it."))
	if err != nil {
		return fmt.Errorf("building the instrument of the job phases: %w", err)
	}

	m.tick, err = meter.Float64Histogram(prefix+"tick.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Time of one step of the control loop."))
	if err != nil {
		return fmt.Errorf("building the instrument of the tick: %w", err)
	}

	return nil
}

// Observe takes the state of the fleet at one tick. It replaces the observation before it, so a
// series that the controller no longer sees stops instead of repeating its last value.
func (m *Metrics) Observe(observation Observation) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.observation = observation
}

// Close stops the callback. The meter provider does this as well when it stops, so a caller
// needs it only to build a second set of instruments on one meter.
func (m *Metrics) Close() error {
	if m.registered == nil {
		return nil
	}

	return m.registered.Unregister()
}

// report gives the levels of the last observation to the collector.
func (m *Metrics) report(_ context.Context, observer metric.Observer) error {
	m.mu.RLock()
	observation := m.observation
	m.mu.RUnlock()

	for profile, jobs := range observation.Waiting {
		observer.ObserveInt64(m.waiting, jobs,
			metric.WithAttributes(attribute.String(attributeProfile, profile)))
	}

	for profile, age := range observation.OldestWaiting {
		observer.ObserveFloat64(m.oldest, age.Seconds(),
			metric.WithAttributes(attribute.String(attributeProfile, profile)))
	}

	for slot, count := range observation.Slots {
		observer.ObserveInt64(m.slots, count, metric.WithAttributes(
			attribute.String(attributePool, pool(slot.Pool)),
			attribute.String(attributeSlotState, string(slot.State)),
		))
	}

	for vm, count := range observation.VMs {
		observer.ObserveInt64(m.vms, count, metric.WithAttributes(
			attribute.String(attributeProfile, vm.Profile),
			attribute.String(attributeVMPhase, string(vm.Phase)),
		))
	}

	for state, count := range observation.Workers {
		observer.ObserveInt64(m.workers, count,
			metric.WithAttributes(semconv.CICDWorkerStateKey.String(string(state))))
	}

	// The age of an answer is its age when the collector reads it, which is real time.
	for source, at := range observation.Synced {
		observer.ObserveFloat64(m.staleness, time.Since(at).Seconds(), //nolint:forbidigo // real age
			metric.WithAttributes(attribute.String(attributeSource, string(source))))
	}

	return nil
}

// pool gives the pool of a worker. A metric series needs a value for each attribute, and a
// worker with no pool label would otherwise give an empty one.
func pool(name string) string {
	if name == "" {
		return "none"
	}

	return name
}

// Unmatched counts one job of the queue that no profile matches.
func (m *Metrics) Unmatched(ctx context.Context) {
	m.unmatched.Add(ctx, 1)
}

// Failed counts one job that the controller ended. The cause is the value of error.type, so it
// must be a short and closed set of words and not the message of the job page.
func (m *Metrics) Failed(ctx context.Context, cause string) {
	m.failed.Add(ctx, 1, metric.WithAttributes(semconv.ErrorTypeKey.String(cause)))
}

// Deleted counts one deleted virtual machine, with the reason of the decision table.
func (m *Metrics) Deleted(ctx context.Context, reason string) {
	m.deletions.Add(ctx, 1,
		metric.WithAttributes(attribute.String(attributeDeleteReason, reason)))
}

// SystemError counts one failure of Buildkite or of Orchard, such as a rate limit or a status
// that the controller cannot use.
func (m *Metrics) SystemError(ctx context.Context, component Component, cause string) {
	m.errors.Add(ctx, 1, metric.WithAttributes(
		semconv.CICDSystemComponent(string(component)),
		semconv.ErrorTypeKey.String(cause),
	))
}

// JobPhase records the time of one phase of one job.
func (m *Metrics) JobPhase(ctx context.Context, profile string, phase JobPhase,
	took time.Duration,
) {
	m.jobPhase.Record(ctx, took.Seconds(), metric.WithAttributes(
		attribute.String(attributeProfile, profile),
		attribute.String(attributeJobPhase, string(phase)),
	))
}

// Tick records the time of one step of the control loop.
func (m *Metrics) Tick(ctx context.Context, step string, took time.Duration) {
	m.tick.Record(ctx, took.Seconds(),
		metric.WithAttributes(attribute.String(attributeStep, step)))
}
