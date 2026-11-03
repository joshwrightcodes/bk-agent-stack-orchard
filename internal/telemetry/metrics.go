// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"
	"fmt"
	"maps"
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
	attributeHostState    = prefix + "host.state"
	attributeVMPhase      = prefix + "vm.phase"
	attributeDeleteReason = prefix + "vm.delete.reason"
	attributeJobPhase     = prefix + "job.phase"
	attributeStep         = prefix + "tick.step"
	attributeSource       = prefix + "source"
	attributeAdmission    = prefix + "admission.reason"
	attributeOperation    = prefix + "operation"
	attributeWorker       = prefix + "worker.name"
	attributeStartOutcome = prefix + "vm.start.outcome"
)

// The attributes of the job.match span: the profile that carries the job, or none. A job that no
// profile carries has the event "no profile", whose attribute SpanMismatch holds the reason. The
// log record of the skip carries the reason under the same key.
const (
	SpanProfile  = prefix + "profile"
	SpanMatched  = prefix + "job.matched"
	SpanSkipped  = prefix + "job.skipped"
	SpanMismatch = prefix + "match.reason"
)

// The attributes of the spans of the Stacks calls that act on several jobs at once. Each job has
// its own span of the call, with the answer for that job.
const (
	// SpanReserved is on stacks.reserve: Buildkite reserved the job, or extended its reservation.
	SpanReserved = prefix + "job.reserved"

	// SpanExtension is on stacks.reserve: the call extends a reservation of the reaper.
	SpanExtension = prefix + "reservation.extension"

	// SpanTokenIssued is on stacks.issue_token: Buildkite issued a token for the job.
	SpanTokenIssued = prefix + "job.token_issued"

	// SpanRefused is on stacks.notify_skipped: Buildkite refused the line of the job.
	SpanRefused = prefix + "notification.refused"
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

// The states of a slot, for the online hosts of a pool. They add up: total is used, reserved,
// unavailable, and free, while no host carries more machines than it advertises.
const (
	// SlotsTotal is every slot of the online hosts.
	SlotsTotal SlotState = "total"

	// SlotsUsed is a slot that a machine of any owner holds.
	SlotsUsed SlotState = "used"

	// SlotsReserved is an open slot that admission counts as promised: to a machine that Orchard
	// holds and no host runs yet, or to a machine that this process asked for and the inventory
	// does not show yet.
	SlotsReserved SlotState = "reserved"

	// SlotsUnavailable is an open slot of a host that admission does not use: an operator paused
	// it, or no profile can put a machine on it.
	SlotsUnavailable SlotState = "unavailable"

	// SlotsFree is an open slot that admission can give a new machine, if the host also has the
	// cores and the memory of the profile.
	SlotsFree SlotState = "free"
)

// HostState is the state of a Mac host of the fleet.
type HostState string

// The states of a host. A host that stopped answering is offline, whatever else it is.
const (
	HostOnline  HostState = "online"
	HostOffline HostState = "offline"
	HostPaused  HostState = "paused"
)

// Host names one series of the host count.
type Host struct {
	Pool  string
	State HostState
}

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

// The states of a worker that this controller reports, which are values that the conventions
// name. A machine of this controller carries one job, so it is never available: it is busy while
// it runs, and offline when it failed.
const (
	WorkerBusy    WorkerState = "busy"
	WorkerOffline WorkerState = "offline"
)

// JobPhase is one part of the way from a scheduled job to a running agent.
type JobPhase string

// The phases of a job. The controller observes them at poll intervals, so each duration has a
// tolerance of one job-states interval. The last one runs from the first tick that saw the job
// reach a terminal state to the deletion that Orchard accepted, and it is the lag of the reaper.
const (
	PhaseScheduledToReserved JobPhase = "scheduled_to_reserved"
	PhaseReservedToPlaced    JobPhase = "reserved_to_placed"
	PhasePlacedToRunning     JobPhase = "placed_to_running"
	PhaseRunningToAccepted   JobPhase = "running_to_accepted"
	PhaseFinishedToDeleted   JobPhase = "finished_to_deleted"
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

// Operation names one call of the API of Buildkite or of Orchard. It is the value of
// bk_agent_stack_orchard.operation on cicd.system.errors, so that a failure of one call, such as
// the create of a machine, is not lost among the failures of the others.
type Operation string

// The calls of the Stacks API of Buildkite.
const (
	OperationRegister      Operation = "register"
	OperationDeregister    Operation = "deregister"
	OperationScheduledJobs Operation = "scheduled_jobs"
	OperationReserve       Operation = "reserve"
	OperationTokens        Operation = "tokens"
	OperationStates        Operation = "states"
	OperationFinish        Operation = "finish"
	OperationNotify        Operation = "notify"
)

// The calls of the Orchard API.
const (
	OperationCreateVM    Operation = "create_vm"
	OperationListVMs     Operation = "list_vms"
	OperationDeleteVM    Operation = "delete_vm"
	OperationListWorkers Operation = "list_workers"
	OperationEvents      Operation = "events"
)

// Source is one supply of observed state, whose age decides whether the controller may act.
type Source string

// The sources of observed state.
const (
	SourceScheduledJobs Source = "scheduled_jobs"
	SourceJobStates     Source = "job_states"
	SourceInventory     Source = "inventory"
)

// AdmissionBlock says why the gate of admission is closed. It is the value of
// bk_agent_stack_orchard.admission.reason, and empty text when the gate is open.
type AdmissionBlock string

// The reasons that close the gate of admission (target design, section 6.3). A source that has not
// answered and a source whose answer is too old each have one reason. The queue has its own two:
// a queue that the controller has not read, and one that an operator paused.
const (
	BlockQueueUnread         AdmissionBlock = "queue_unread"
	BlockQueuePaused         AdmissionBlock = "queue_paused"
	BlockScheduledJobsStale  AdmissionBlock = "scheduled_jobs_stale"
	BlockInventoryUnanswered AdmissionBlock = "inventory_unanswered"
	BlockInventoryStale      AdmissionBlock = "inventory_stale"
	BlockJobStatesUnanswered AdmissionBlock = "job_states_unanswered"
	BlockJobStatesStale      AdmissionBlock = "job_states_stale"
)

// admissionBlocks is the closed set of reasons, each of which has a series from the start.
func admissionBlocks() []AdmissionBlock {
	return []AdmissionBlock{
		BlockQueueUnread, BlockQueuePaused, BlockScheduledJobsStale, BlockInventoryUnanswered,
		BlockInventoryStale, BlockJobStatesUnanswered, BlockJobStatesStale,
	}
}

// Unanswered gives the reason for a source that has not answered. The queue is read when its
// source answered, so a queue source that has not answered is an unread queue.
func Unanswered(source Source) AdmissionBlock {
	switch source {
	case SourceInventory:
		return BlockInventoryUnanswered
	case SourceJobStates:
		return BlockJobStatesUnanswered
	default:
		return BlockQueueUnread
	}
}

// Stale gives the reason for a source whose last answer is too old.
func Stale(source Source) AdmissionBlock {
	switch source {
	case SourceInventory:
		return BlockInventoryStale
	case SourceJobStates:
		return BlockJobStatesStale
	default:
		return BlockScheduledJobsStale
	}
}

// StartOutcome is how one start of a machine for a reserved job ended. It is the value of
// bk_agent_stack_orchard.vm.start.outcome.
type StartOutcome string

// The outcomes of a start. Each one but the first leaves the job reserved with no machine, and the
// job comes back to the queue when its reservation ends.
const (
	// StartCreated is a machine that Orchard accepted.
	StartCreated StartOutcome = "created"

	// StartCreateFailed is a create that failed, such as one that Orchard refused.
	StartCreateFailed StartOutcome = "create_failed"

	// StartNoToken is a job that got no token, so no create was sent.
	StartNoToken StartOutcome = "no_token"

	// StartNotSent is a create that the step did not send, because Orchard did not answer an
	// earlier create or the step ran out of time.
	StartNotSent StartOutcome = "not_sent"
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
// it. A series that the callback reported once and that leaves an observation reports 0: a series
// that stops keeps its last value in many backends for minutes, so a count that fell to zero
// would look like the count that it had before.
type Observation struct {
	// Waiting is the jobs of each profile that wait for a machine.
	Waiting map[string]int64

	// OldestWaiting is the age of the oldest waiting job of each profile, at the observation.
	// The callback adds the time since the observation, so the age keeps rising while the loop
	// is stuck.
	OldestWaiting map[string]time.Duration

	// Slots is the slots of each pool in each state.
	Slots map[Slot]int64

	// Hosts is the Mac hosts of each pool in each state.
	Hosts map[Host]int64

	// VMs is the machines of each profile in each phase.
	VMs map[VM]int64

	// Workers is the machines that can run an agent, in the states of the conventions.
	Workers map[WorkerState]int64

	// Leftover is the machines of this controller whose job is in a terminal state. A machine
	// of that kind has nothing left to do, so the value is zero except for the short time
	// before the reaper deletes it. It has no attribute, because the count of all of them is
	// the answer.
	Leftover int64

	// Synced is when each source last answered. The callback reports the age of that answer,
	// and not the time itself, so that the alert reads a number that needs no clock of its own.
	// A source that has not answered since the start holds the start of the process.
	Synced map[Source]time.Time

	// Heartbeat is when the loop last made progress, and the start of the process before the
	// loop starts. The callback reports its age, as it does for Synced.
	Heartbeat time.Time

	// Registered reports a stack that Buildkite accepted. It is false while the controller
	// registers, which it tries again while Buildkite does not answer, and after it withdrew.
	Registered bool

	// Blocked says why the gate of admission was closed at the tick, and is empty when it was
	// open.
	Blocked AdmissionBlock
}

// Expect adds a zero for each series that the profiles and the closed sets of values give, where
// the observation has no value. An alert that compares a value never fires on a series that does
// not exist, so these series exist from the first observation, and not from the first job.
//
// A pool is known only from the workers of the fleet, so its series start with the first read of
// the inventory.
func (o *Observation) Expect(profiles []string) {
	if o.Waiting == nil {
		o.Waiting = map[string]int64{}
	}

	if o.VMs == nil {
		o.VMs = map[VM]int64{}
	}

	if o.Workers == nil {
		o.Workers = map[WorkerState]int64{}
	}

	for _, profile := range profiles {
		o.Waiting[profile] += 0

		for _, phase := range []VMPhase{VMUnplaced, VMBooting, VMRunning, VMFailed} {
			o.VMs[VM{Profile: profile, Phase: phase}] += 0
		}
	}

	o.Workers[WorkerBusy] += 0
	o.Workers[WorkerOffline] += 0
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
	hosts       metric.Int64ObservableUpDownCounter
	vms         metric.Int64ObservableUpDownCounter
	workers     metric.Int64ObservableUpDownCounter
	leftover    metric.Int64ObservableGauge
	staleness   metric.Float64ObservableGauge
	heartbeat   metric.Float64ObservableGauge
	registered  metric.Int64ObservableGauge
	blocked     metric.Int64ObservableGauge
	skipped     metric.Int64Counter
	truncated   metric.Int64Counter
	failed      metric.Int64Counter
	deletions   metric.Int64Counter
	starts      metric.Int64Counter
	errors      metric.Int64Counter
	jobPhase    metric.Float64Histogram
	tick        metric.Float64Histogram
	callback    metric.Registration
	mu          sync.RWMutex
	observation Observation

	// observed is when the last observation arrived, in real time. The ages of an observation
	// grow by the time since.
	observed time.Time

	// reported holds each series that the callback reported, so that a series that leaves the
	// observation reports 0. The attributes of a series are bounded by the configuration and the
	// fleet, so the set stays small. Only the callback reads and writes it.
	reported map[metric.Observable]map[attribute.Distinct]attribute.Set
}

// NewMetrics builds the instruments on a meter and registers the callback that reports the
// levels of the fleet.
func NewMetrics(meter metric.Meter) (*Metrics, error) {
	m := &Metrics{reported: map[metric.Observable]map[attribute.Distinct]attribute.Set{}}

	if err := m.observable(meter); err != nil {
		return nil, err
	}

	if err := m.synchronous(meter); err != nil {
		return nil, err
	}

	callback, err := meter.RegisterCallback(m.report,
		m.waiting, m.oldest, m.slots, m.hosts, m.vms, m.workers, m.leftover, m.staleness, m.heartbeat,
		m.registered, m.blocked)
	if err != nil {
		return nil, fmt.Errorf("registering the callback of the fleet levels: %w", err)
	}

	m.callback = callback

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
		metric.WithDescription("Age of the oldest job that waits. The alert that jobs wait too long reads it."))
	if err != nil {
		return fmt.Errorf("building the instrument of the oldest waiting job: %w", err)
	}

	m.slots, err = meter.Int64ObservableUpDownCounter(prefix+"slot.count",
		metric.WithUnit("{slot}"),
		metric.WithDescription("Virtual machine slots of a pool, by state."))
	if err != nil {
		return fmt.Errorf("building the instrument of the slots: %w", err)
	}

	m.hosts, err = meter.Int64ObservableUpDownCounter(prefix+"host.count",
		metric.WithUnit("{host}"),
		metric.WithDescription("Mac hosts of a pool, by state."))
	if err != nil {
		return fmt.Errorf("building the instrument of the hosts: %w", err)
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

	m.leftover, err = meter.Int64ObservableGauge(prefix+"vm.leftover",
		metric.WithUnit("{vm}"),
		metric.WithDescription("Virtual machines of this controller whose job is in a terminal "+
			"state. It stays at zero except for the short time before the reaper deletes them."))
	if err != nil {
		return fmt.Errorf("building the instrument of the leftover machines: %w", err)
	}

	m.staleness, err = meter.Float64ObservableGauge(prefix+"source.staleness",
		metric.WithUnit("s"),
		metric.WithDescription("Age of the last answer of a source of observed state, or the "+
			"age of the process while the source has not answered."))
	if err != nil {
		return fmt.Errorf("building the instrument of the source age: %w", err)
	}

	m.heartbeat, err = meter.Float64ObservableGauge(prefix+"loop.heartbeat.age",
		metric.WithUnit("s"),
		metric.WithDescription("Time since the loop last finished a tick, or the age of the "+
			"process while the loop has not started."))
	if err != nil {
		return fmt.Errorf("building the instrument of the heartbeat: %w", err)
	}

	m.registered, err = meter.Int64ObservableGauge(prefix+"stack.registered",
		metric.WithUnit("1"),
		metric.WithDescription("1 while Buildkite holds the registration of the stack, and 0 "+
			"while the controller registers or after it withdrew."))
	if err != nil {
		return fmt.Errorf("building the instrument of the registration: %w", err)
	}

	m.blocked, err = meter.Int64ObservableGauge(prefix+"admission.blocked",
		metric.WithUnit("1"),
		metric.WithDescription("1 for the reason that closed the gate of admission at the last "+
			"tick, and 0 for each other reason. Every series is 0 while the gate is open."))
	if err != nil {
		return fmt.Errorf("building the instrument of the gate of admission: %w", err)
	}

	return nil
}

// synchronous builds the instruments that a caller records as the event happens.
func (m *Metrics) synchronous(meter metric.Meter) error {
	var err error

	m.skipped, err = meter.Int64Counter(prefix+"job.skipped",
		metric.WithUnit("{job}"),
		metric.WithDescription("Jobs of the queue that no profile matches, which the controller "+
			"leaves for another runner. It counts each job once, when it first notifies it."))
	if err != nil {
		return fmt.Errorf("building the instrument of the skipped jobs: %w", err)
	}

	m.truncated, err = meter.Int64Counter(prefix+"queue.truncated",
		metric.WithUnit("{read}"),
		metric.WithDescription("Reads of the queue that stopped at the page cap with a page left, "+
			"so that jobs behind the pages may be hidden."))
	if err != nil {
		return fmt.Errorf("building the instrument of the truncated reads: %w", err)
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

	m.starts, err = meter.Int64Counter(prefix+"vm.starts",
		metric.WithUnit("{vm}"),
		metric.WithDescription("Starts of a machine for a reserved job, by outcome."))
	if err != nil {
		return fmt.Errorf("building the instrument of the starts: %w", err)
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
			"accepted it, and from the end of the job to the deletion of its machine."))
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
	m.observed = time.Now() //nolint:forbidigo // the real time that the ages grow from
}

// Close stops the callback. The meter provider does this as well when it stops, so a caller
// needs it only to build a second set of instruments on one meter.
func (m *Metrics) Close() error {
	if m.callback == nil {
		return nil
	}

	return m.callback.Unregister()
}

// report gives the levels of the last observation to the collector, and 0 for each series that
// it reported before and that the observation no longer holds.
func (m *Metrics) report(_ context.Context, observer metric.Observer) error {
	m.mu.RLock()
	observation, observed := m.observation, m.observed
	m.mu.RUnlock()

	// The time since the observation, which each age of the observation has grown by.
	var since time.Duration
	if !observed.IsZero() {
		since = time.Since(observed) //nolint:forbidigo // real age
	}

	levels := &levels{
		observer: observer,
		sets:     map[metric.Observable]map[attribute.Distinct]attribute.Set{},
	}

	for profile, jobs := range observation.Waiting {
		levels.int64(m.waiting, jobs, attribute.NewSet(attribute.String(attributeProfile, profile)))
	}

	// A profile with no waiting job has no age, and reads 0.
	for profile := range observation.Waiting {
		if _, aged := observation.OldestWaiting[profile]; !aged {
			levels.float64(m.oldest, 0, attribute.NewSet(attribute.String(attributeProfile, profile)))
		}
	}

	for profile, age := range observation.OldestWaiting {
		levels.float64(m.oldest, (age + since).Seconds(),
			attribute.NewSet(attribute.String(attributeProfile, profile)))
	}

	for slot, count := range observation.Slots {
		levels.int64(m.slots, count, attribute.NewSet(
			attribute.String(attributePool, pool(slot.Pool)),
			attribute.String(attributeSlotState, string(slot.State)),
		))
	}

	for host, count := range observation.Hosts {
		levels.int64(m.hosts, count, attribute.NewSet(
			attribute.String(attributePool, pool(host.Pool)),
			attribute.String(attributeHostState, string(host.State)),
		))
	}

	for vm, count := range observation.VMs {
		levels.int64(m.vms, count, attribute.NewSet(
			attribute.String(attributeProfile, vm.Profile),
			attribute.String(attributeVMPhase, string(vm.Phase)),
		))
	}

	for state, count := range observation.Workers {
		levels.int64(m.workers, count,
			attribute.NewSet(semconv.CICDWorkerStateKey.String(string(state))))
	}

	levels.int64(m.leftover, observation.Leftover, attribute.NewSet())

	// An age is its age when the collector reads it, which is real time, so it keeps rising while
	// the loop is stuck and no new observation arrives.
	for source, at := range observation.Synced {
		levels.float64(m.staleness, time.Since(at).Seconds(), //nolint:forbidigo // real age
			attribute.NewSet(attribute.String(attributeSource, string(source))))
	}

	if !observation.Heartbeat.IsZero() {
		levels.float64(m.heartbeat,
			time.Since(observation.Heartbeat).Seconds(), //nolint:forbidigo // real age
			attribute.NewSet())
	}

	registered := int64(0)
	if observation.Registered {
		registered = 1
	}

	levels.int64(m.registered, registered, attribute.NewSet())

	for _, reason := range admissionBlocks() {
		closed := int64(0)
		if observation.Blocked == reason {
			closed = 1
		}

		levels.int64(m.blocked, closed,
			attribute.NewSet(attribute.String(attributeAdmission, string(reason))))
	}

	m.zero(levels)

	return nil
}

// zero reports 0 for each series that the callback reported before and that this read did not,
// and remembers the series of this read.
func (m *Metrics) zero(levels *levels) {
	for instrument, sets := range m.reported {
		for key, set := range sets {
			if _, now := levels.sets[instrument][key]; now {
				continue
			}

			switch observable := instrument.(type) {
			case metric.Int64Observable:
				levels.observer.ObserveInt64(observable, 0, metric.WithAttributeSet(set))
			case metric.Float64Observable:
				levels.observer.ObserveFloat64(observable, 0, metric.WithAttributeSet(set))
			}
		}
	}

	for instrument, sets := range levels.sets {
		if m.reported[instrument] == nil {
			m.reported[instrument] = map[attribute.Distinct]attribute.Set{}
		}

		maps.Copy(m.reported[instrument], sets)
	}
}

// levels is what one read of the callback reported.
type levels struct {
	observer metric.Observer
	sets     map[metric.Observable]map[attribute.Distinct]attribute.Set
}

func (l *levels) int64(instrument metric.Int64Observable, value int64, set attribute.Set) {
	l.observer.ObserveInt64(instrument, value, metric.WithAttributeSet(set))
	l.remember(instrument, set)
}

func (l *levels) float64(instrument metric.Float64Observable, value float64, set attribute.Set) {
	l.observer.ObserveFloat64(instrument, value, metric.WithAttributeSet(set))
	l.remember(instrument, set)
}

func (l *levels) remember(instrument metric.Observable, set attribute.Set) {
	if l.sets[instrument] == nil {
		l.sets[instrument] = map[attribute.Distinct]attribute.Set{}
	}

	l.sets[instrument][set.Equivalent()] = set
}

// pool gives the pool of a worker. A metric series needs a value for each attribute, and a
// worker with no pool label would otherwise give an empty one.
func pool(name string) string {
	return none(name)
}

// none gives the value of an attribute, and "none" for empty text, which a backend would show as
// a series with a missing value.
func none(value string) string {
	if value == "" {
		return "none"
	}

	return value
}

// Skipped counts one job of the queue that no profile matches. The caller counts a job once.
func (m *Metrics) Skipped(ctx context.Context) {
	m.skipped.Add(ctx, 1)
}

// QueueTruncated counts one read of the queue that stopped at the page cap with a page left.
func (m *Metrics) QueueTruncated(ctx context.Context) {
	m.truncated.Add(ctx, 1)
}

// Failure names where one failure happened: the profile of the machine and the Mac host that
// carried it. A bad image fails the machines of one profile, and a broken host fails the machines
// on it, so the failure counters carry both. Both are bounded: the profiles by the configuration,
// and the hosts by the fleet.
type Failure struct {
	Profile string
	Worker  string
}

func (f Failure) attributes() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(attributeProfile, f.Profile),
		attribute.String(attributeWorker, none(f.Worker)),
	}
}

// Failed counts one job that the controller ended. The cause is the value of error.type, so it
// must be a short and closed set of words and not the message of the job page.
func (m *Metrics) Failed(ctx context.Context, where Failure, cause string) {
	m.failed.Add(ctx, 1, metric.WithAttributes(
		append(where.attributes(), semconv.ErrorTypeKey.String(cause))...))
}

// Deleted counts one deleted virtual machine, with the reason of the decision table.
func (m *Metrics) Deleted(ctx context.Context, where Failure, reason string) {
	m.deletions.Add(ctx, 1, metric.WithAttributes(
		append(where.attributes(), attribute.String(attributeDeleteReason, reason))...))
}

// Started counts one start of a machine for a reserved job of a profile, by its outcome.
func (m *Metrics) Started(ctx context.Context, profile string, outcome StartOutcome) {
	m.starts.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attributeProfile, profile),
		attribute.String(attributeStartOutcome, string(outcome)),
	))
}

// SystemError counts one call to Buildkite or to Orchard that failed, such as a rate limit or a
// status that the controller cannot use, with the call. A retry inside a call is not a failure of
// its own: only the last try of a call that failed counts.
func (m *Metrics) SystemError(ctx context.Context, component Component, operation Operation,
	cause string,
) {
	m.errors.Add(ctx, 1, metric.WithAttributes(
		semconv.CICDSystemComponent(string(component)),
		attribute.String(attributeOperation, string(operation)),
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
