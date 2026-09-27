//go:build integration && load

package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/app"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
	loopcontroller "github.com/joshwrightcodes/bk-agent-stack-orchard/internal/controller"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

// T-080. The controller carries the target scale: 50 synthetic workers of two slots each, and 500
// jobs that arrive in the pattern of the demand model (system design baseline, section 4.2).
//
// The demand model runs fifteen times faster than a workday, so that the test fits the half hour
// of layer L5: a job of fifteen minutes takes one minute, and the arrival rates are fifteen times
// the rates of the model. The controller does not know, because its intervals are seconds and the
// jobs still arrive and end many ticks apart.
//
// The controller is the one that app.Build wires, in this process, with the real clients against
// `orchard dev` and the fake Stacks server. The test runs each tick itself, so that it can measure
// the tick, and reads the instruments without a collector. The test is also the agents: it takes
// each job whose machine runs, and ends it after its duration.
//
// The test is a diagnostic. It fails on a broken invariant or budget, and a nightly workflow
// reports it without blocking a merge.
func TestTheControllerCarriesTheTargetScale(t *testing.T) {
	startOrchardWith(t, orchardBinary(t), loadWorkers)

	stacks := stacksWith(t)
	calls := &counted{next: http.DefaultTransport}

	loop, reader := buildInProcess(t, stacks.URL(), calls)
	plan := demand(jobCount(t))
	started := time.Now()

	run := &loadRun{stacks: stacks, plan: plan, started: started, agents: map[string]*agent{}}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	deadline := started.Add(35 * time.Minute)

	for !run.done() {
		if time.Now().After(deadline) {
			t.Fatalf("the run did not end inside 35 minutes: %s", run.summary())
		}

		at := <-ticker.C

		run.arrive(at)
		run.work(t, at)

		begun := time.Now()
		loop.Tick(t.Context(), at)
		run.ticks = append(run.ticks, time.Since(begun))

		run.check(t, reader, at)

		// A line each minute, so that a run that stops making progress says where it stopped.
		if len(run.ticks)%60 == 0 {
			t.Logf("after %s: %s", at.Sub(started).Round(time.Second), run.summary())
		}
	}

	report(t, run, calls)
}

const (
	loadWorkers = 50
	loadSlots   = 2 * loadWorkers
	loadJobs    = 500

	// compression is how many seconds of the demand model one second of the test is.
	compression = 15
)

// jobCount gives the jobs of the run: 500, or LOAD_JOBS for a shorter run while a developer works
// on the test.
func jobCount(t *testing.T) int {
	t.Helper()

	text := os.Getenv("LOAD_JOBS")
	if text == "" {
		return loadJobs
	}

	count, err := strconv.Atoi(text)
	if err != nil || count < 1 {
		t.Fatalf("LOAD_JOBS is %q, want a number of jobs", text)
	}

	return count
}

// The demand model: D12, D13, D7, and D8 of the system design baseline, section 4.2.
const (
	meanArrivalsEachHour = 70.4
	peakArrivalsEachHour = 140.8
	meanJob              = 15 * time.Minute
	longestJob           = 60 * time.Minute
)

// arrival is one job of the plan: when it arrives after the start, and how long its agent runs.
type arrival struct {
	id       string
	after    time.Duration
	duration time.Duration
}

// demand gives the jobs of the run. The first six minutes arrive at the mean rate, the next eight
// at the peak rate, and the rest at the mean rate again. The arrivals are a Poisson process, and
// the durations are exponential about the mean job and no longer than the longest job. A fixed
// seed gives the same run each night, so that two nights compare.
func demand(count int) []arrival {
	random := rand.New(rand.NewPCG(80, 500)) //nolint:gosec // G404: a repeatable plan, not a secret

	rate := func(after time.Duration) float64 {
		perHour := meanArrivalsEachHour
		if after >= 6*time.Minute && after < 14*time.Minute {
			perHour = peakArrivalsEachHour
		}

		return perHour * compression / 3600
	}

	plan := make([]arrival, 0, count)

	var after time.Duration

	for i := range count {
		after += time.Duration(random.ExpFloat64() / rate(after) * float64(time.Second))

		took := time.Duration(random.ExpFloat64() * float64(meanJob) / compression)
		took = min(max(took, 10*time.Second), longestJob/compression)

		plan = append(plan, arrival{
			id:       fmt.Sprintf("018f3b2c-1c4a-7e5b-9a3d-%012x", i+1),
			after:    after,
			duration: took,
		})
	}

	return plan
}

// agent is what the test knows of one job.
type agent struct {
	arrived  time.Time
	vms      map[string]bool
	endsAt   time.Time
	finished bool
}

// loadRun is the state of the run, and its measurements.
type loadRun struct {
	stacks  *fakestacks.Server
	plan    []arrival
	next    int
	started time.Time
	agents  map[string]*agent

	ticks      []time.Duration
	mostVMs    int
	violations []string
	waits      []time.Duration
	lagged     int
}

// arrive schedules each job whose time has come.
func (r *loadRun) arrive(at time.Time) {
	for r.next < len(r.plan) && r.started.Add(r.plan[r.next].after).Before(at) {
		job := r.plan[r.next]
		r.stacks.Schedule(scheduled(job.id, "xcode=27", 0))
		r.agents[job.id] = &agent{arrived: at, vms: map[string]bool{}}
		r.next++
	}
}

// work is the agents: a job whose machine runs is taken, and a job whose time is over ends.
func (r *loadRun) work(t *testing.T, at time.Time) {
	t.Helper()

	vms := loadMachines(t)
	owned := 0

	for _, vm := range vms {
		id, ok := strings.CutPrefix(vm.Name, "bk-"+controllerID+"-")
		if !ok {
			continue
		}

		owned++

		a := r.agents[id]
		if a == nil {
			continue
		}

		a.vms[vm.UID] = true

		// I-1: one machine for each job. A second UID is a second machine for the job.
		if len(a.vms) > 1 {
			r.violate("I-1: the job %s has the machines %v", id, a.vms)
		}

		if vm.Status == "running" && a.endsAt.IsZero() {
			r.stacks.SetJobState(id, "running")
			a.endsAt = at.Add(r.duration(id))
			r.waits = append(r.waits, at.Sub(a.arrived))
		}
	}

	for id, a := range r.agents {
		if !a.finished && !a.endsAt.IsZero() && at.After(a.endsAt) {
			r.stacks.SetJobState(id, "finished")
			a.finished = true
		}
	}

	// I-2: never more machines than the slots of the fleet.
	if owned > loadSlots {
		r.violate("I-2: %d machines for %d slots", owned, loadSlots)
	}

	r.mostVMs = max(r.mostVMs, owned)
}

func (r *loadRun) duration(id string) time.Duration {
	for _, job := range r.plan {
		if job.id == id {
			return job.duration
		}
	}

	return 0
}

// check reads the instruments. The age of the oldest waiting job is the age of the oldest job
// that the queue still holds, within the tolerance of a few ticks.
func (r *loadRun) check(t *testing.T, reader *sdkmetric.ManualReader, at time.Time) {
	t.Helper()

	if len(r.ticks)%30 != 0 {
		return
	}

	reserved := map[string]bool{}
	for _, id := range r.stacks.Reserved() {
		reserved[id] = true
	}

	var oldest time.Duration

	for id, a := range r.agents {
		if !reserved[id] {
			oldest = max(oldest, at.Sub(a.arrived))
		}
	}

	reported := oldestWaiting(t, reader)
	if math.Abs(reported-oldest.Seconds()) > 5 {
		r.lagged++
	}
}

func (r *loadRun) violate(format string, args ...any) {
	r.violations = append(r.violations, fmt.Sprintf(format, args...))
}

// done reports a run whose jobs all arrived and ended, and whose machines are all gone.
func (r *loadRun) done() bool {
	if r.next < len(r.plan) {
		return false
	}

	for _, a := range r.agents {
		if !a.finished {
			return false
		}
	}

	return r.mostVMs > 0 && ownedNow() == 0
}

func (r *loadRun) summary() string {
	reserved := map[string]bool{}
	for _, id := range r.stacks.Reserved() {
		reserved[id] = true
	}

	var held, running, finished int

	for id, a := range r.agents {
		switch {
		case a.finished:
			finished++
		case !a.endsAt.IsZero():
			running++
		case reserved[id]:
			held++
		}
	}

	return fmt.Sprintf("%d of %d jobs arrived, %d reserved without an agent, %d running, "+
		"%d finished, %d machines in Orchard", r.next, len(r.plan), held, running, finished,
		ownedNow())
}

// report writes the measurements to the log of the test and to the summary of the workflow, and
// fails the test on a broken invariant or budget.
func report(t *testing.T, run *loadRun, calls *counted) {
	t.Helper()

	ticks := slices.Clone(run.ticks)
	slices.Sort(ticks)

	waits := slices.Clone(run.waits)
	slices.Sort(waits)

	busiest := calls.busiest()

	lines := []string{
		"## T-080: the target scale",
		"",
		"| Measure | Value |",
		"|---|---|",
		fmt.Sprintf("| Workers, slots | %d, %d |", loadWorkers, loadSlots),
		fmt.Sprintf("| Jobs | %d |", len(run.plan)),
		fmt.Sprintf("| Ticks | %d |", len(ticks)),
		fmt.Sprintf("| Tick p50, p95, max | %s, %s, %s |", quantile(ticks, 0.5),
			quantile(ticks, 0.95), quantile(ticks, 1)),
		fmt.Sprintf("| Most machines at once | %d |", run.mostVMs),
		fmt.Sprintf("| Wait for a running machine p50, p95 (test time) | %s, %s |",
			quantile(waits, 0.5), quantile(waits, 0.95)),
		fmt.Sprintf("| Busiest second: list, reserve, get-states | %d, %d, %d |",
			busiest["list"], busiest["reserve"], busiest["states"]),
		fmt.Sprintf("| Samples where oldest-waiting lagged the queue by more than 5 s | %d |",
			run.lagged),
		fmt.Sprintf("| Violations | %d |", len(run.violations)),
	}

	text := strings.Join(lines, "\n") + "\n"
	t.Log("\n" + text)

	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		//nolint:gosec // G304: the path is the one that GitHub Actions gives.
		if file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600); err == nil {
			_, _ = file.WriteString(text)
			_ = file.Close()
		}
	}

	if len(run.violations) > 0 {
		t.Errorf("the run broke invariants: %v", run.violations)
	}

	// The limits of the Stacks API (target design, section 3).
	for endpoint, limit := range map[string]int{"list": 10, "reserve": 10, "states": 20} {
		if busiest[endpoint] > limit {
			t.Errorf("%d calls of %s in one second, and the limit is %d", busiest[endpoint],
				endpoint, limit)
		}
	}

	// Open item O-5: the budget is two times the first measured p95 on the runner. Until an owner
	// sets it, the test reports and does not judge the tick.
	if budget, err := time.ParseDuration(os.Getenv("LOAD_TICK_BUDGET")); err == nil &&
		quantile(ticks, 0.95) > budget {
		t.Errorf("the p95 of the tick is %s, and the budget is %s", quantile(ticks, 0.95), budget)
	}
}

func quantile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}

	return sorted[int(math.Ceil(q*float64(len(sorted))))-1].Round(time.Millisecond)
}

// counted is a transport that counts the calls of each endpoint of the Stacks API in each second.
type counted struct {
	next http.RoundTripper

	mu      sync.Mutex
	seconds map[string]map[int64]int
}

func (c *counted) RoundTrip(request *http.Request) (*http.Response, error) {
	endpoint := "other"

	switch path := request.URL.Path; {
	case strings.HasSuffix(path, "/scheduled-jobs"):
		endpoint = "list"
	case strings.HasSuffix(path, "/batch-reserve"):
		endpoint = "reserve"
	case strings.HasSuffix(path, "/get-states"):
		endpoint = "states"
	}

	c.mu.Lock()
	if c.seconds == nil {
		c.seconds = map[string]map[int64]int{}
	}

	if c.seconds[endpoint] == nil {
		c.seconds[endpoint] = map[int64]int{}
	}

	c.seconds[endpoint][time.Now().Unix()]++
	c.mu.Unlock()

	return c.next.RoundTrip(request)
}

// busiest gives the most calls of each endpoint in one second.
func (c *counted) busiest() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()

	most := map[string]int{}

	for endpoint, seconds := range c.seconds {
		for _, count := range seconds {
			most[endpoint] = max(most[endpoint], count)
		}
	}

	return most
}

// buildInProcess builds the controller as the binary does, with the real clients, a transport
// that counts the calls to the Stacks API, and a reader of the instruments.
func buildInProcess(t *testing.T, endpoint string, calls *counted) (
	*loopcontroller.Controller, *sdkmetric.ManualReader,
) {
	t.Helper()

	path := writeConfig(t, endpoint)

	settings, err := config.Load(path, func(name string) string {
		// G101: the values are fakes that no API accepts, which the configuration needs to load.
		return map[string]string{ //nolint:gosec
			"BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN": "bkct_load",
			"BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN":         "load",
		}[name]
	})
	if err != nil {
		t.Fatalf("loading the configuration: %v", err)
	}

	stacks, err := buildkite.New(buildkite.Options{
		Token:      settings.Buildkite.AgentToken,
		Endpoint:   settings.Buildkite.Endpoint,
		StackKey:   settings.ControllerID,
		Queue:      settings.Buildkite.Queue,
		HTTPClient: &http.Client{Transport: calls},
	})
	if err != nil {
		t.Fatalf("building the Stacks client: %v", err)
	}

	machines, err := orchard.New(orchard.Options{
		URL:            settings.Orchard.URL,
		ServiceAccount: settings.Orchard.ServiceAccount,
		Token:          settings.Orchard.Token,
	})
	if err != nil {
		t.Fatalf("building the Orchard client: %v", err)
	}

	// The records of the controller go to a file, and a failed run shows their end.
	logPath := filepath.Join(t.TempDir(), "controller.log")
	logFile := output(t, logPath)

	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("the controller wrote:\n%s", tail(logPath))
		}
	})

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("load"))
	if err != nil {
		t.Fatalf("building the instruments: %v", err)
	}

	loop, err := app.Build(app.Options{
		Config: settings,
		Telemetry: &telemetry.Telemetry{
			Metrics: instruments, Logger: slog.New(slog.NewJSONHandler(logFile, nil)),
			Tracer: tracenoop.NewTracerProvider().Tracer(""),
		},
		Stacks:  stacks,
		Orchard: machines,
		Now:     time.Now,
	})
	if err != nil {
		t.Fatalf("building the controller: %v", err)
	}

	return loop, reader
}

// oldestWaiting reads the age of the oldest waiting job, in seconds, as the instruments report it.
func oldestWaiting(t *testing.T, reader *sdkmetric.ManualReader) float64 {
	t.Helper()

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("reading the instruments: %v", err)
	}

	var oldest float64

	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "bk_agent_stack_orchard.job.waiting.oldest.age" {
				continue
			}

			if gauge, ok := m.Data.(metricdata.Gauge[float64]); ok {
				for _, point := range gauge.DataPoints {
					oldest = max(oldest, point.Value)
				}
			}
		}
	}

	return oldest
}

// loadMachine is what the test reads of a machine.
type loadMachine struct {
	Name   string `json:"name"`
	UID    string `json:"uid"`
	Status string `json:"status"`
}

func loadMachines(t *testing.T) []loadMachine {
	t.Helper()

	var vms []loadMachine
	if err := decode(orchardURL+"/v1/vms", &vms); err != nil {
		t.Fatalf("listing the machines: %v", err)
	}

	return vms
}

func ownedNow() int {
	var vms []loadMachine
	if err := decode(orchardURL+"/v1/vms", &vms); err != nil {
		return -1
	}

	owned := 0

	for _, vm := range vms {
		if strings.HasPrefix(vm.Name, "bk-"+controllerID+"-") {
			owned++
		}
	}

	return owned
}
