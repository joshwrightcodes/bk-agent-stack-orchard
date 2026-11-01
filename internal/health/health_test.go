// SPDX-License-Identifier: AGPL-3.0-or-later

package health_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/health"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

var now = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

type source struct {
	kind telemetry.Source
	at   time.Time
}

func (s *source) Source() telemetry.Source { return s.kind }
func (s *source) At() time.Time            { return s.at }

// fresh gives the three sources of the loop, each with an answer of the given age.
func fresh(ages ...time.Duration) []*source {
	kinds := []telemetry.Source{
		telemetry.SourceScheduledJobs, telemetry.SourceInventory, telemetry.SourceJobStates,
	}

	sources := make([]*source, 0, len(kinds))
	for i, kind := range kinds {
		var age time.Duration
		if i < len(ages) {
			age = ages[i]
		}

		sources = append(sources, &source{kind: kind, at: now.Add(-age)})
	}

	return sources
}

// probed is a health server and what it wrote.
type probed struct {
	checks *health.Checks
	log    *bytes.Buffer
}

// window is the readiness window of most tests, which the configuration gives as stateFreshness.
const window = 30 * time.Second

func build(t *testing.T, sources []*source, unauthorized *bool) probed {
	t.Helper()

	return buildWithin(t, window, sources, unauthorized)
}

func buildWithin(t *testing.T, within time.Duration, sources []*source, unauthorized *bool) probed {
	t.Helper()

	given := make([]health.Source, 0, len(sources))
	for _, s := range sources {
		given = append(given, s)
	}

	log := &bytes.Buffer{}

	checks, err := health.New(health.Options{
		Sources:      given,
		Window:       within,
		Unauthorized: func() bool { return *unauthorized },
		Now:          func() time.Time { return now },
		Logger:       slog.New(slog.NewTextHandler(log, nil)),
	})
	if err != nil {
		t.Fatalf("New() = %v, want the checks", err)
	}

	return probed{checks: checks, log: log}
}

// status gives what a probe of one service reads after an update.
func (p probed) status(t *testing.T, service string) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()

	p.checks.Update(t.Context())

	answer, err := p.checks.Server().Check(t.Context(),
		&healthpb.HealthCheckRequest{Service: service})
	if err != nil {
		t.Fatalf("Check(%q) = %v, want an answer", service, err)
	}

	return answer.GetStatus()
}

const (
	serving    = healthpb.HealthCheckResponse_SERVING
	notServing = healthpb.HealthCheckResponse_NOT_SERVING
)

func TestTheControllerIsReadyWhileEverySourceIsFresh(t *testing.T) {
	t.Parallel()

	refused := false

	// The window includes its end: an answer exactly 30 s old still counts.
	p := build(t, fresh(time.Second, 5*time.Second, window), &refused)

	for _, service := range []string{health.ServiceReadiness, ""} {
		if got := p.status(t, service); got != serving {
			t.Errorf("the service %q is %s, want SERVING", service, got)
		}
	}
}

// A process that has not updated its checks is not ready, which is every process for its first
// moment: New starts not ready.
func TestAControllerIsNotReadyBeforeTheFirstUpdate(t *testing.T) {
	t.Parallel()

	refused := false
	p := build(t, fresh(), &refused)

	answer, err := p.checks.Server().Check(t.Context(),
		&healthpb.HealthCheckRequest{Service: health.ServiceReadiness})
	if err != nil || answer.GetStatus() != notServing {
		t.Errorf("readiness before the first update = %v, %v, want NOT_SERVING", answer, err)
	}
}

// The window is stateFreshness, which the configuration requires to be longer than the interval
// of each source. A source read every 45 s is ready inside a window of 60 s, however often the
// probe reads.
func TestReadinessUsesStateFreshness(t *testing.T) {
	t.Parallel()

	refused := false
	p := buildWithin(t, 60*time.Second, fresh(time.Second, 5*time.Second, 45*time.Second), &refused)

	for range 3 {
		if got := p.status(t, health.ServiceReadiness); got != serving {
			t.Fatalf("readiness is %s, want SERVING", got)
		}
	}

	old := buildWithin(t, 60*time.Second, fresh(time.Second, 61*time.Second), &refused)
	if got := old.status(t, health.ServiceReadiness); got != notServing {
		t.Errorf("readiness with an answer of 61 s is %s, want NOT_SERVING", got)
	}
}

// A source whose calls fail keeps its last answer. The controller then acts on an old world, and
// the platform must stop sending it traffic and an operator must see why.
func TestAnOldSourceMakesTheControllerUnready(t *testing.T) {
	t.Parallel()

	refused := false
	p := build(t, fresh(time.Second, window+time.Second), &refused)

	if got := p.status(t, health.ServiceReadiness); got != notServing {
		t.Fatalf("readiness is %s, want NOT_SERVING", got)
	}

	if log := p.log.String(); !strings.Contains(log, "inventory") || !strings.Contains(log, "31s") {
		t.Errorf("the log reads %q, want the inventory and its age", log)
	}
}

// A process that has not read a source yet is not ready.
func TestASourceThatHasNotAnsweredMakesTheControllerUnready(t *testing.T) {
	t.Parallel()

	refused := false
	sources := fresh()
	sources[2].at = time.Time{}
	p := build(t, sources, &refused)

	if got := p.status(t, health.ServiceReadiness); got != notServing ||
		!strings.Contains(p.log.String(), "job_states") {
		t.Errorf("readiness is %s with the log %q, want NOT_SERVING that names the job states",
			got, p.log.String())
	}
}

// T-063. A revoked token gives HTTP 401 on every call, and only a new token and a restart clear
// it, so the controller says so where an operator reads.
func TestARefusedTokenMakesTheControllerUnready(t *testing.T) {
	t.Parallel()

	refused := true
	p := build(t, fresh(), &refused)

	if got := p.status(t, health.ServiceReadiness); got != notServing ||
		!strings.Contains(p.log.String(), "token") {
		t.Errorf("readiness is %s with the log %q, want NOT_SERVING that names the token",
			got, p.log.String())
	}
}

// A restart cures nothing that a slow API or a refused token causes, so liveness passes while
// the controller is not ready.
func TestLivenessDoesNotDependOnTheAPIs(t *testing.T) {
	t.Parallel()

	refused := true
	p := build(t, fresh(time.Hour), &refused)

	if got := p.status(t, health.ServiceLiveness); got != serving {
		t.Errorf("liveness is %s, want SERVING while the controller is not ready", got)
	}
}

// The age in a reason changes at each update. A record at each update would bury the log, so a
// record follows a change of the reasons and not of their text.
func TestTheLogRecordsAChangeOfReadinessOnce(t *testing.T) {
	t.Parallel()

	refused := false
	sources := fresh()
	p := build(t, sources, &refused)

	p.status(t, health.ServiceReadiness)

	sources[1].at = now.Add(-time.Minute)
	p.status(t, health.ServiceReadiness)

	sources[1].at = now.Add(-2 * time.Minute)
	p.status(t, health.ServiceReadiness)

	sources[1].at = now
	p.status(t, health.ServiceReadiness)

	log := p.log.String()
	if strings.Count(log, "is not ready") != 1 || strings.Count(log, "is ready") != 2 {
		t.Errorf("the log reads %q, want ready, then not ready once, then ready", log)
	}
}

func TestNewRefusesIncompleteChecks(t *testing.T) {
	t.Parallel()

	_, err := health.New(health.Options{})
	if err == nil || !strings.Contains(err.Error(), "clock") ||
		!strings.Contains(err.Error(), "window") || !strings.Contains(err.Error(), "token") ||
		!strings.Contains(err.Error(), "logger") {
		t.Errorf("New() with nothing = %v, want an error that names each missing part", err)
	}
}

// A window of zero would make every answer old, and a negative one would make the check wrong
// in a way that nobody sees, so the checks refuse both.
func TestNewRefusesAWindowThatIsNotPositive(t *testing.T) {
	t.Parallel()

	for _, within := range []time.Duration{0, -time.Second} {
		_, err := health.New(health.Options{
			Sources:      []health.Source{&source{kind: telemetry.SourceInventory}},
			Window:       within,
			Unauthorized: func() bool { return false },
			Now:          func() time.Time { return now },
			Logger:       slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		})
		if err == nil || !strings.Contains(err.Error(), "window") {
			t.Errorf("New() with a window of %s = %v, want an error that names the window",
				within, err)
		}
	}
}
