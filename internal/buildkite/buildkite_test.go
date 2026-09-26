package buildkite_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/buildkite/stacksapi"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

// Two jobs that the tests use. The shape is the shape that Buildkite gives.
const (
	first  = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d")
	second = job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7e")
)

// start gives a client that talks to a fake server. The caller changes the fields of the fake
// before it calls the client.
func start(t *testing.T) (*buildkite.Client, *fakestacks.Server) {
	t.Helper()

	fake := fakestacks.New()
	fake.StackKey = "bk-macos-prod"
	t.Cleanup(fake.Close)

	client, err := buildkite.New(buildkite.Options{
		Token:    secret.String("bkct_a-cluster-agent-token"),
		Endpoint: fake.URL(),
		StackKey: fake.StackKey,
		Queue:    "macos-prod",
	})
	if err != nil {
		t.Fatalf("New() = %v, want a client", err)
	}

	return client, fake
}

func TestNewRefusesSettingsThatCannotWork(t *testing.T) {
	t.Parallel()

	whole := buildkite.Options{
		Token:    secret.String("bkct_a-cluster-agent-token"),
		Endpoint: "https://agent.buildkite.com/v3",
		StackKey: "bk-macos-prod",
		Queue:    "macos-prod",
	}

	for name, change := range map[string]func(*buildkite.Options){
		"no token":       func(o *buildkite.Options) { o.Token = "" },
		"no stack key":   func(o *buildkite.Options) { o.StackKey = "" },
		"no queue":       func(o *buildkite.Options) { o.Queue = "" },
		"no endpoint":    func(o *buildkite.Options) { o.Endpoint = "" },
		"a bad endpoint": func(o *buildkite.Options) { o.Endpoint = "agent.buildkite.com" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			options := whole
			change(&options)

			if _, err := buildkite.New(options); err == nil {
				t.Errorf("New() with %s = nil, want an error", name)
			}
		})
	}
}

func TestRegisterAnnouncesACustomStackForTheQueue(t *testing.T) {
	client, fake := start(t)

	if err := client.Register(t.Context()); err != nil {
		t.Fatalf("Register() = %v, want no error", err)
	}

	if len(fake.RegisterCalls) != 1 {
		t.Fatalf("the server saw %d registrations, want 1", len(fake.RegisterCalls))
	}

	call := fake.RegisterCalls[0]
	if call.Key != "bk-macos-prod" || call.QueueKey != "macos-prod" {
		t.Errorf("the registration named stack %q and queue %q, want bk-macos-prod and macos-prod",
			call.Key, call.QueueKey)
	}

	// The type decides what Buildkite permits. The first-party types are for the stacks of
	// Buildkite, and this controller is a custom stack.
	if call.Type != stacksapi.StackTypeCustom {
		t.Errorf("the registration gave type %q, want custom", call.Type)
	}

	if err := client.Deregister(t.Context()); err != nil {
		t.Fatalf("Deregister() = %v, want no error", err)
	}

	if fake.DeregisterCalls != 1 {
		t.Errorf("the server saw %d deregistrations, want 1", fake.DeregisterCalls)
	}
}

// An idle queue is the usual answer, and it must not look like a failure.
func TestScheduledJobsReadsAnEmptyPage(t *testing.T) {
	client, fake := start(t)

	page, err := client.ScheduledJobs(t.Context(), "", 0)
	if err != nil {
		t.Fatalf("ScheduledJobs() = %v, want no error", err)
	}

	if len(page.Jobs) != 0 {
		t.Errorf("the page held %d jobs, want none", len(page.Jobs))
	}

	if page.Paused {
		t.Error("the page reported a paused queue, want a running queue")
	}

	if page.NextCursor != "" {
		t.Errorf("the page gave the cursor %q, want none", page.NextCursor)
	}

	if len(fake.ScheduledJobsCalls) != 1 || fake.ScheduledJobsCalls[0].QueueKey != "macos-prod" {
		t.Errorf("the call asked for %+v, want the queue macos-prod", fake.ScheduledJobsCalls)
	}
}

func TestScheduledJobsGivesTheJobsTheCursorAndThePause(t *testing.T) {
	client, fake := start(t)

	fake.ScheduledJobs = stacksapi.ListScheduledJobsResponse{
		Jobs: []stacksapi.ScheduledJob{{
			ID:              string(first),
			Priority:        3,
			AgentQueryRules: []string{"queue=macos-prod", "xcode=27"},
			RunnableAt:      time.Now().Add(-time.Minute),
			Pipeline:        stacksapi.Pipeline{Slug: "ios-app"},
			Build:           stacksapi.Build{Number: 412},
		}},
		ClusterQueue: stacksapi.ClusterQueue{Paused: true},
		PageInfo:     stacksapi.PageInfo{HasNextPage: true, EndCursor: "page-2"},
	}

	page, err := client.ScheduledJobs(t.Context(), "page-1", 50)
	if err != nil {
		t.Fatalf("ScheduledJobs() = %v, want no error", err)
	}

	if len(page.Jobs) != 1 {
		t.Fatalf("the page held %d jobs, want 1", len(page.Jobs))
	}

	got := page.Jobs[0]
	if got.ID != first || got.Priority != 3 || got.Pipeline != "ios-app" || got.Build != 412 {
		t.Errorf("the job read as %+v, want the identity and the priority of the answer", got)
	}

	// The Matcher works from these rules, so they must arrive whole.
	if strings.Join(got.Rules, ",") != "queue=macos-prod,xcode=27" {
		t.Errorf("the job carried the rules %v, want both rules of the answer", got.Rules)
	}

	if !page.Paused {
		t.Error("the page reported a running queue, want the paused queue of the answer")
	}

	if page.NextCursor != "page-2" {
		t.Errorf("the page gave the cursor %q, want page-2", page.NextCursor)
	}

	if call := fake.ScheduledJobsCalls[0]; call.After != "page-1" || call.Limit != "50" {
		t.Errorf("the call asked for %+v, want the cursor page-1 and the limit 50", call)
	}
}

// A page whose identifier is not a job means that the contract moved. Silence would give the
// Matcher a job that no VM can carry, so the client stops instead.
func TestScheduledJobsRefusesAnIdentifierThatIsNotAJob(t *testing.T) {
	client, fake := start(t)

	fake.ScheduledJobs = stacksapi.ListScheduledJobsResponse{
		Jobs: []stacksapi.ScheduledJob{{ID: "not-a-job"}},
	}

	if _, err := client.ScheduledJobs(t.Context(), "", 0); err == nil {
		t.Error("ScheduledJobs() = nil, want an error for an identifier that is not a job")
	}
}

// Another stack can hold a job that this controller saw in the list. A partial answer is normal,
// and the launcher continues with the jobs that it holds.
func TestReserveGivesAPartialResult(t *testing.T) {
	client, fake := start(t)

	fake.ReserveResponse = &stacksapi.BatchReserveJobsResponse{
		Reserved:    []string{string(first)},
		NotReserved: []string{string(second)},
	}

	reservation, err := client.Reserve(t.Context(), []job.ID{first, second}, 300*time.Second)
	if err != nil {
		t.Fatalf("Reserve() = %v, want no error", err)
	}

	if len(reservation.Reserved) != 1 || reservation.Reserved[0] != first {
		t.Errorf("the reservation held %v, want only the first job", reservation.Reserved)
	}

	if len(reservation.NotReserved) != 1 || reservation.NotReserved[0] != second {
		t.Errorf("the reservation refused %v, want only the second job", reservation.NotReserved)
	}

	// The expiry decides how long a job waits when the controller stops after the reservation.
	if len(fake.ReserveExpirySeconds) != 1 || fake.ReserveExpirySeconds[0] != 300 {
		t.Errorf("the call asked for the expiry %v, want 300 seconds", fake.ReserveExpirySeconds)
	}
}

func TestReserveSendsNoCallForNoJobs(t *testing.T) {
	client, fake := start(t)

	reservation, err := client.Reserve(t.Context(), nil, 300*time.Second)
	if err != nil {
		t.Fatalf("Reserve() = %v, want no error", err)
	}

	if len(reservation.Reserved) != 0 || len(fake.ReserveCalls) != 0 {
		t.Errorf("the client sent %d calls for no jobs, want none", len(fake.ReserveCalls))
	}
}

// A job in not_issued gets no token, so it gets no VM, and its reservation expires.
func TestTokensReportsTheJobsThatGotNone(t *testing.T) {
	client, fake := start(t)

	fake.JobAcquisitionTokenResponse = &stacksapi.IssueJobAcquisitionTokensResponse{
		//nolint:gosec // G101: the text is what a token looks like, which is the point of the
		// test. It opens nothing, because it goes to a server of this test.
		JobAcquisitionTokens: []stacksapi.JobAcquisitionToken{{
			JobUUID:   string(first),
			Token:     "bkjat_a-job-acquisition-token",
			ExpiresAt: time.Now().Add(30 * time.Minute),
		}},
		NotIssued: []string{string(second)},
	}

	tokens, err := client.Tokens(t.Context(), []job.ID{first, second}, 1800*time.Second)
	if err != nil {
		t.Fatalf("Tokens() = %v, want no error", err)
	}

	if len(tokens.Issued) != 1 || tokens.Issued[0].JobID != first {
		t.Fatalf("the answer issued %+v, want one token for the first job", tokens.Issued)
	}

	if tokens.Issued[0].Token.Reveal() != "bkjat_a-job-acquisition-token" {
		t.Error("the token did not carry the value of the answer")
	}

	if len(tokens.NotIssued) != 1 || tokens.NotIssued[0] != second {
		t.Errorf("the answer refused %v, want only the second job", tokens.NotIssued)
	}

	if len(fake.JobAcquisitionTokenCalls) != 1 ||
		fake.JobAcquisitionTokenCalls[0].TokenLifetimeSeconds != 1800 {
		t.Errorf("the call asked for %+v, want the lifetime 1800 seconds",
			fake.JobAcquisitionTokenCalls)
	}
}

// A token opens a queue of the cluster to whoever holds it. It must not reach a log through a
// print of the structure that carries it.
func TestTokensKeepsTheTokenOutOfAPrint(t *testing.T) {
	client, _ := start(t)

	tokens, err := client.Tokens(t.Context(), []job.ID{first}, 1800*time.Second)
	if err != nil {
		t.Fatalf("Tokens() = %v, want no error", err)
	}

	printed := fmt.Sprintf("%v %+v %#v", tokens, tokens, tokens)
	if strings.Contains(printed, "bkjat_") {
		t.Errorf("a print of the tokens held the token itself: %s", printed)
	}
}

func TestStatesGivesTheStateOfEachJobThatBuildkiteKnows(t *testing.T) {
	client, fake := start(t)

	fake.JobStates = map[string]string{string(first): "running"}

	states, err := client.States(t.Context(), []job.ID{first, second})
	if err != nil {
		t.Fatalf("States() = %v, want no error", err)
	}

	if states[first] != "running" {
		t.Errorf("the first job read as %q, want running", states[first])
	}

	// A job that Buildkite does not name is absent, and the Reaper treats absence as unknown.
	if _, ok := states[second]; ok {
		t.Error("the second job had a state, want no state for a job that Buildkite did not name")
	}
}

func TestNotifyWritesTheLinesOfEachJob(t *testing.T) {
	client, fake := start(t)

	err := client.Notify(t.Context(), []buildkite.Notification{
		{JobID: first, Detail: "VM requested", At: time.Now()},
		{JobID: second, Detail: "placed on mac-03, booting", At: time.Now()},
	})
	if err != nil {
		t.Fatalf("Notify() = %v, want no error", err)
	}

	if len(fake.NotificationCalls) != 1 || len(fake.NotificationCalls[0]) != 2 {
		t.Fatalf("the server saw %+v, want one call of two lines", fake.NotificationCalls)
	}

	if fake.NotificationCalls[0][0].Detail != "VM requested" {
		t.Errorf("the first line read %q, want VM requested", fake.NotificationCalls[0][0].Detail)
	}
}

// The job ended, another stack owns it, or it never existed. The answer does not change, so the
// caller marks the job as ignored, and the client sends no second call.
func TestFinishTreatsA422AsTerminalAndSendsNoSecondCall(t *testing.T) {
	client, fake := start(t)

	fake.FinishJobStatusCode = http.StatusUnprocessableEntity
	fake.FinishJobError = "the job is already finished"

	err := client.Finish(t.Context(), first, 1, "no profile has these tags")
	if !errors.Is(err, buildkite.ErrTerminal) {
		t.Fatalf("Finish() = %v, want ErrTerminal", err)
	}

	if len(fake.FinishJobCalls) != 1 {
		t.Errorf("the server saw %d finish calls, want 1", len(fake.FinishJobCalls))
	}
}

func TestStatesTreatsA404AsTerminal(t *testing.T) {
	client, fake := start(t)

	fake.GetJobStatesStatusCode = http.StatusNotFound
	fake.GetJobStatesError = "stack not found"

	_, err := client.States(t.Context(), []job.ID{first})
	if !errors.Is(err, buildkite.ErrTerminal) {
		t.Fatalf("States() = %v, want ErrTerminal", err)
	}
}

// A token that Buildkite refuses cannot change while the controller runs, so the controller must
// stop taking work instead of failing every job of the queue.
func TestA401EndsTheReadinessOfTheController(t *testing.T) {
	client, fake := start(t)

	fake.ScheduledJobsStatusCode = http.StatusUnauthorized
	fake.ScheduledJobsError = "invalid token"

	if client.Unauthorized() {
		t.Fatal("Unauthorized() = true before any call, want false")
	}

	_, err := client.ScheduledJobs(t.Context(), "", 0)
	if !errors.Is(err, buildkite.ErrUnauthorized) {
		t.Fatalf("ScheduledJobs() = %v, want ErrUnauthorized", err)
	}

	if !client.Unauthorized() {
		t.Error("Unauthorized() = false after a 401, want true, so that the readiness check fails")
	}

	// A refused token is not a temporary failure, so the client must not repeat the call.
	if len(fake.ScheduledJobsCalls) != 1 {
		t.Errorf("the server saw %d calls, want 1 for a 401", len(fake.ScheduledJobsCalls))
	}
}

// retryAfter answers the first call with a rate limit, and passes the rest to the fake server.
type retryAfter struct {
	seconds  string
	attempts int
	at       []time.Time
}

func (r *retryAfter) RoundTrip(request *http.Request) (*http.Response, error) {
	r.attempts++
	r.at = append(r.at, time.Now())

	if r.attempts > 1 {
		return http.DefaultTransport.RoundTrip(request)
	}

	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Header:     http.Header{"Retry-After": []string{r.seconds}},
		Body:       http.NoBody,
		Request:    request,
	}, nil
}

// Buildkite states how long to wait. A controller that ignores it makes the limit worse for
// every stack of the organisation.
func TestARateLimitWaitsForTheTimeThatBuildkiteGives(t *testing.T) {
	fake := fakestacks.New()
	fake.StackKey = "bk-macos-prod"
	t.Cleanup(fake.Close)

	transport := &retryAfter{seconds: "1"}

	client, err := buildkite.New(buildkite.Options{
		Token:      secret.String("bkct_a-cluster-agent-token"),
		Endpoint:   fake.URL(),
		StackKey:   fake.StackKey,
		Queue:      "macos-prod",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("New() = %v, want a client", err)
	}

	if _, err := client.ScheduledJobs(t.Context(), "", 0); err != nil {
		t.Fatalf("ScheduledJobs() = %v, want the answer of the second attempt", err)
	}

	if transport.attempts != 2 {
		t.Fatalf("the client made %d attempts, want 2", transport.attempts)
	}

	if waited := transport.at[1].Sub(transport.at[0]); waited < time.Second {
		t.Errorf("the client waited %s between the attempts, want at least the 1s of Retry-After",
			waited)
	}
}

// A rate limit that outlives the retries of the library reaches the caller as itself, so that a
// caller can tell it from a failure that a second call cannot fix.
func TestARateLimitThatDoesNotEndReachesTheCaller(t *testing.T) {
	client, fake := start(t)

	fake.ScheduledJobsStatusCode = http.StatusTooManyRequests
	fake.ScheduledJobsError = "slow down"
	fake.Headers = http.Header{"Retry-After": []string{"0"}}

	_, err := client.ScheduledJobs(t.Context(), "", 0)
	if !errors.Is(err, buildkite.ErrRateLimited) {
		t.Fatalf("ScheduledJobs() = %v, want ErrRateLimited", err)
	}

	if len(fake.ScheduledJobsCalls) < 2 {
		t.Errorf("the server saw %d calls, want the client to try again after a rate limit",
			len(fake.ScheduledJobsCalls))
	}
}

// A call for another stack must not answer, because a test that passes against the wrong stack
// proves nothing about the controller.
func TestTheServerAnswersOnlyForItsOwnStack(t *testing.T) {
	fake := fakestacks.New()
	fake.StackKey = "bk-macos-prod"
	t.Cleanup(fake.Close)

	client, err := buildkite.New(buildkite.Options{
		Token:    secret.String("bkct_a-cluster-agent-token"),
		Endpoint: fake.URL(),
		StackKey: "another-stack",
		Queue:    "macos-prod",
	})
	if err != nil {
		t.Fatalf("New() = %v, want a client", err)
	}

	if _, err := client.ScheduledJobs(t.Context(), "", 0); err == nil {
		t.Error("ScheduledJobs() = nil, want an error for a stack that the server does not hold")
	}
}

// The controller depends on the interface, not on the client, so that a component test needs no
// HTTP server.
func TestTheClientIsUsableThroughTheInterface(t *testing.T) {
	client, _ := start(t)

	var stacks buildkite.Stacks = client
	if err := stacks.Register(t.Context()); err != nil {
		t.Errorf("Register() through the interface = %v, want no error", err)
	}
}

// recording keeps the body of every request, and passes the request to the fake server.
type recording struct {
	bodies []string
}

func (r *recording) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}

		r.bodies = append(r.bodies, string(body))
		request.Body = io.NopCloser(bytes.NewReader(body))
	}

	return http.DefaultTransport.RoundTrip(request)
}

// finishWith sends one finish through the real client to the fake server, and gives the detail
// that went out on the wire.
func finishWith(t *testing.T, detail string) string {
	t.Helper()

	fake := fakestacks.New()
	fake.StackKey = "bk-macos-prod"
	t.Cleanup(fake.Close)

	wire := &recording{}

	client, err := buildkite.New(buildkite.Options{
		Token:      secret.String("bkct_a-cluster-agent-token"),
		Endpoint:   fake.URL(),
		StackKey:   fake.StackKey,
		Queue:      "macos-prod",
		HTTPClient: &http.Client{Transport: wire},
	})
	if err != nil {
		t.Fatalf("New() = %v, want a client", err)
	}

	if err := client.Finish(t.Context(), first, -1, detail); err != nil {
		t.Fatalf("Finish() = %v, want no error", err)
	}

	if len(wire.bodies) != 1 {
		t.Fatalf("the client sent %d requests, want 1", len(wire.bodies))
	}

	var sent struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(wire.bodies[0]), &sent); err != nil {
		t.Fatalf("the finish body is not JSON: %v", err)
	}

	return sent.Detail
}

// T-007. No finish detail over 4 KB leaves the controller, and the cut never splits a character.
// The library makes the cut and says so at the end of the detail.
func TestFinishSendsADetailOfAtMostFourKilobytes(t *testing.T) {
	t.Parallel()

	// Each character takes three bytes, so a cut at a fixed byte would split one.
	sent := finishWith(t, strings.Repeat("ビ", 2000))

	if len(sent) > 4*1024 || !utf8.ValidString(sent) {
		t.Errorf("the client sent a detail of %d bytes, whole characters %v; want at most 4096 "+
			"bytes of whole characters", len(sent), utf8.ValidString(sent))
	}

	if !strings.HasSuffix(sent, "(Message was cropped because it exceeded the max size)") {
		t.Error("the detail does not say that it was cut, so a reader cannot tell")
	}
}

// DetailBudget copies a limit that the library does not export. A detail of exactly the budget
// leaves unchanged, and one byte more is cut, so a version of the library that moves its limit
// fails here instead of cutting the end of a message that counted on the budget.
func TestTheDetailBudgetIsTheLimitOfTheLibrary(t *testing.T) {
	t.Parallel()

	fits := strings.Repeat("a", buildkite.DetailBudget)
	if sent := finishWith(t, fits); sent != fits {
		t.Errorf("a detail of %d bytes changed on the way out, want it unchanged",
			buildkite.DetailBudget)
	}

	over := fits + "b"
	if sent := finishWith(t, over); sent == over {
		t.Errorf("a detail of %d bytes left unchanged, want it cut: the budget is below the "+
			"limit of the library", buildkite.DetailBudget+1)
	}
}
