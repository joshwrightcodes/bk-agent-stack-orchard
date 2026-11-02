// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buildkite/stacksapi"
	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

// T-061. More matched jobs than free slots (QS-1). The synthetic worker has room for two machines
// of the profile, so two of three jobs start, the third stays scheduled, and it starts when a
// slot is free.
func TestAFullFleetHoldsAJobBackUntilASlotIsFree(t *testing.T) {
	startOrchard(t, orchardBinary(t))

	ids := []string{
		"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a01",
		"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a02",
		"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a03",
	}

	// The jobs are scheduled a minute apart, so admission takes the two oldest first.
	var jobs []stacksapi.ScheduledJob
	for i, id := range ids {
		jobs = append(jobs, scheduled(id, "xcode=27", time.Duration(len(ids)-i)*time.Minute))
	}

	stacks := stacksWith(t, jobs...)
	process := startController(t, writeConfig(t, stacks.URL()))

	eventually(t, "the two oldest jobs have running machines", func() bool {
		return running(t, ids[0]) && running(t, ids[1])
	})

	// Several ticks pass, and the fleet stays full: the third job is neither reserved nor given a
	// machine, so Buildkite still shows it as scheduled. Admission runs in each tick on the poll
	// and the inventory, which refresh at each tick: one tick for a stale answer, one to act, and
	// one more for margin.
	never(t, 3, "the third job is reserved or gets a machine while the fleet is full", func() bool {
		_, found := machineNamed(t, machineOf(ids[2]))

		return found || slices.Contains(stacks.Reserved(), ids[2])
	})

	// The first job ends, and the controller deletes its machine, which frees a slot.
	stacks.SetJobState(ids[0], "running")
	stacks.SetJobState(ids[0], "finished")

	eventually(t, "the machine of the finished job is gone", func() bool {
		_, found := machineNamed(t, machineOf(ids[0]))

		return !found
	})

	eventually(t, "the third job starts in the free slot", func() bool {
		return running(t, ids[2])
	})

	for _, id := range ids[1:] {
		stacks.SetJobState(id, "running")
		stacks.SetJobState(id, "finished")
	}

	eventually(t, "no machine of this controller remains", func() bool {
		return len(ownedMachines(t)) == 0
	})

	// Each job was reserved once: a second reservation would be a second agent for one job.
	if held := stacks.Reserved(); len(held) != len(ids) {
		t.Errorf("the controller reserved %v, want each of the %d jobs once", held, len(ids))
	}

	stopController(t, process)
}

// T-062. A job whose tags match no profile is skipped: the controller never reserves it or
// finishes it, the job page gets one notification at the next poll, the counter of skipped jobs
// rises by 1, and no machine starts for it (QS-3).
func TestAJobThatNoProfileCarriesIsLeftAlone(t *testing.T) {
	startOrchard(t, orchardBinary(t))

	stacks := stacksWith(t, scheduled(carried, "xcode=99", time.Minute))
	sink := newMetricSink(t)

	started := time.Now()
	process := startControllerWith(t, writeConfig(t, stacks.URL()), sink.environment())

	var line stacksapi.StackNotification

	eventually(t, "the controller writes a notification for the job", func() bool {
		for _, notification := range stacks.Notifications() {
			if notification.JobUUID == carried {
				line = notification

				return true
			}
		}

		return false
	})

	// The poll runs each second. The limit leaves room for the start of the process and a slow
	// runner, and it is still far inside the scheduled-job expiry that QS-3 measures against.
	if took := time.Since(started); took > 15*time.Second {
		t.Errorf("the job got its line %s after the controller started, want it at the next poll",
			took)
	}

	if !strings.Contains(line.Detail, "xcode=99") {
		t.Errorf("the job page reads %q, want it to name the tags of the job", line.Detail)
	}

	eventually(t, "the counter of skipped jobs reads 1", func() bool {
		return sink.skipped() == 1
	})

	// Several polls pass, and the job gets no second line, no reservation, no finish, and no
	// machine, and the counter stays at 1. The job comes back through the poll only, so one tick
	// for a stale answer, one to act, and one more for margin.
	never(t, 3, "the controller acts on the job again", func() bool {
		_, found := machineNamed(t, machineOf(carried))

		lines := 0

		for _, notification := range stacks.Notifications() {
			if notification.JobUUID == carried {
				lines++
			}
		}

		return found || lines > 1 || slices.Contains(stacks.Reserved(), carried) ||
			finished(stacks.Finished(), carried) || sink.skipped() > 1
	})

	stopController(t, process)
}

// metricSink is an OTLP receiver over HTTP that keeps the last value of the skipped counter.
type metricSink struct {
	server *httptest.Server

	mu    sync.Mutex
	value int64
}

func newMetricSink(t *testing.T) *metricSink {
	t.Helper()

	sink := &metricSink{}
	sink.server = httptest.NewServer(http.HandlerFunc(sink.receive))
	t.Cleanup(sink.server.Close)

	return sink
}

// environment points the exporter of the controller at the sink, with a short interval.
func (m *metricSink) environment() map[string]string {
	return map[string]string{
		"OTEL_METRICS_EXPORTER":                  "otlp",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL":    "http/protobuf",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT":    m.server.URL + "/v1/metrics",
		"OTEL_METRIC_EXPORT_INTERVAL":            "500",
		"OTEL_EXPORTER_OTLP_METRICS_COMPRESSION": "none",
	}
}

func (m *metricSink) receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	var request collector.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	for _, resource := range request.GetResourceMetrics() {
		for _, scope := range resource.GetScopeMetrics() {
			for _, metric := range scope.GetMetrics() {
				if metric.GetName() != "bk_agent_stack_orchard.job.skipped" {
					continue
				}

				var total int64
				for _, point := range metric.GetSum().GetDataPoints() {
					total += point.GetAsInt()
				}

				m.mu.Lock()
				m.value = total
				m.mu.Unlock()
			}
		}
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

// skipped gives the last value of the counter that the controller exported.
func (m *metricSink) skipped() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.value
}

// stacksWith gives a fake Stacks server for this controller with the jobs scheduled.
func stacksWith(t *testing.T, jobs ...stacksapi.ScheduledJob) *fakestacks.Server {
	t.Helper()

	stacks := fakestacks.New()
	stacks.StackKey = controllerID
	stacks.ScheduledJobs = stacksapi.ListScheduledJobsResponse{Jobs: jobs}
	t.Cleanup(stacks.Close)

	return stacks
}

// scheduled gives one job of the queue with one more tag, scheduled some time ago.
func scheduled(id, tag string, ago time.Duration) stacksapi.ScheduledJob {
	at := time.Now().Add(-ago)

	return stacksapi.ScheduledJob{
		ID:              id,
		AgentQueryRules: []string{"queue=" + queue, tag},
		ScheduledAt:     at,
		RunnableAt:      at,
	}
}

func machineOf(id string) string {
	return "bk-" + controllerID + "-" + id
}

func running(t *testing.T, id string) bool {
	t.Helper()

	found, ok := machineNamed(t, machineOf(id))

	return ok && found.Status == "running"
}

// never checks a condition for a number of ticks of the controller, and fails the test if it holds
// once. A tick is the poll interval that the configuration sets. A call states how many ticks the
// path to the wrong behaviour needs: the slowest source that the behaviour rests on, plus the tick
// that acts on it.
func never(t *testing.T, ticks int, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(time.Duration(ticks) * pollInterval)

	for time.Now().Before(deadline) {
		if condition() {
			t.Fatalf("it happened: %s", what)
		}

		time.Sleep(pollInterval / 4)
	}
}
