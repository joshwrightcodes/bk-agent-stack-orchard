// SPDX-License-Identifier: AGPL-3.0-or-later

package fakestacks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/buildkite/stacksapi"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

const (
	someJob      = "018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d"
	reservedFor  = 300
	queueKey     = "macos-prod"
	finishedJob  = "finished"
	scheduledJob = "scheduled"
)

// clock is a time that a test moves by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}
}

// call sends one request to the fake and gives the status of the answer.
func call(t *testing.T, fake *fakestacks.Server, method, path string, body any) (int, []byte) {
	t.Helper()

	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatalf("encoding the body: %v", err)
		}
	}

	request, err := http.NewRequestWithContext(t.Context(), method,
		fake.URL()+"/stacks/"+fake.StackKey+path, &payload)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })

	var answer bytes.Buffer
	if _, err := answer.ReadFrom(response.Body); err != nil {
		t.Fatalf("reading the answer: %v", err)
	}

	return response.StatusCode, answer.Bytes()
}

func reserve(t *testing.T, fake *fakestacks.Server, seconds int) {
	t.Helper()

	status, _ := call(t, fake, http.MethodPut, "/scheduled-jobs/batch-reserve",
		stacksapi.BatchReserveJobsRequest{
			JobUUIDs:                 []string{someJob},
			ReservationExpirySeconds: seconds,
		})
	if status != http.StatusOK {
		t.Fatalf("reserve answered %d, want 200", status)
	}
}

func finish(t *testing.T, fake *fakestacks.Server) int {
	t.Helper()

	status, _ := call(t, fake, http.MethodPost, "/jobs/"+someJob+"/finish",
		stacksapi.FinishJobRequest{ExitStatus: -1, Detail: "the machine failed"})

	return status
}

func stateOf(t *testing.T, fake *fakestacks.Server) string {
	t.Helper()

	status, body := call(t, fake, http.MethodPost, "/jobs/get-states",
		stacksapi.GetJobStatesRequest{JobUUIDs: []string{someJob}})
	if status != http.StatusOK {
		t.Fatalf("get-states answered %d, want 200", status)
	}

	var answer stacksapi.GetJobStatesResponse
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("decoding the states: %v", err)
	}

	return answer.States[someJob]
}

func scheduledIDs(t *testing.T, fake *fakestacks.Server) []string {
	t.Helper()

	status, body := call(t, fake, http.MethodGet, "/scheduled-jobs?queue_key="+queueKey, nil)
	if status != http.StatusOK {
		t.Fatalf("scheduled-jobs answered %d, want 200", status)
	}

	var page stacksapi.ListScheduledJobsResponse
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decoding the page: %v", err)
	}

	ids := make([]string, 0, len(page.Jobs))
	for _, scheduled := range page.Jobs {
		ids = append(ids, scheduled.ID)
	}

	return ids
}

func waiting(t *testing.T, now func() time.Time) *fakestacks.Server {
	t.Helper()

	fake := fakestacks.New(fakestacks.WithClock(now))
	t.Cleanup(fake.Close)
	fake.Schedule(stacksapi.ScheduledJob{ID: someJob, AgentQueryRules: []string{"queue=" + queueKey}})

	return fake
}

func TestFinishOfAReservedJobSucceedsAndFinishesIt(t *testing.T) {
	t.Parallel()

	fake := waiting(t, newClock().Now)
	reserve(t, fake, reservedFor)

	if status := finish(t, fake); status != http.StatusOK {
		t.Fatalf("finish answered %d, want 200", status)
	}

	if got := stateOf(t, fake); got != finishedJob {
		t.Errorf("the job state is %q after the finish, want %q", got, finishedJob)
	}
}

func TestFinishOfAJobThatFinishedAnswers422(t *testing.T) {
	t.Parallel()

	fake := waiting(t, newClock().Now)
	reserve(t, fake, reservedFor)

	if status := finish(t, fake); status != http.StatusOK {
		t.Fatalf("the first finish answered %d, want 200", status)
	}

	if status := finish(t, fake); status != http.StatusUnprocessableEntity {
		t.Errorf("the second finish answered %d, want 422", status)
	}
}

func TestFinishOfAJobThatAnAgentAcquiredAnswers422(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"assigned", "accepted", "running", "canceling", "timing_out"} {
		fake := waiting(t, newClock().Now)
		reserve(t, fake, reservedFor)
		fake.SetJobState(someJob, state)

		if status := finish(t, fake); status != http.StatusUnprocessableEntity {
			t.Errorf("finish of a %s job answered %d, want 422", state, status)
		}

		if got := stateOf(t, fake); got != state {
			t.Errorf("a refused finish changed the state %q to %q", state, got)
		}
	}
}

func TestFinishOfATerminalJobAnswers422(t *testing.T) {
	t.Parallel()

	terminal := []string{"finished", "canceled", "expired", "timed_out", "skipped", "broken"}
	for _, state := range terminal {
		fake := waiting(t, newClock().Now)
		fake.SetJobState(someJob, state)

		if status := finish(t, fake); status != http.StatusUnprocessableEntity {
			t.Errorf("finish of a %s job answered %d, want 422", state, status)
		}
	}
}

func TestFinishOfAJobThatNeverHadAReservationAnswers422(t *testing.T) {
	t.Parallel()

	fake := waiting(t, newClock().Now)

	if status := finish(t, fake); status != http.StatusUnprocessableEntity {
		t.Errorf("finish of a scheduled job answered %d, want 422", status)
	}

	unknown := fakestacks.New()
	t.Cleanup(unknown.Close)

	if status := finish(t, unknown); status != http.StatusUnprocessableEntity {
		t.Errorf("finish of an unknown job answered %d, want 422", status)
	}
}

func TestAReservationExpiresAfterItsLifetimeWithoutRenewal(t *testing.T) {
	t.Parallel()

	clk := newClock()
	fake := waiting(t, clk.Now)
	reserve(t, fake, reservedFor)

	if got := scheduledIDs(t, fake); len(got) != 0 {
		t.Fatalf("a reserved job is on the scheduled list: %v", got)
	}

	clk.advance(reservedFor*time.Second - time.Second)

	if got := stateOf(t, fake); got != "reserved" {
		t.Fatalf("one second before the end the state is %q, want reserved", got)
	}

	clk.advance(time.Second)

	if got := stateOf(t, fake); got != scheduledJob {
		t.Errorf("at the end of the reservation the state is %q, want %q", got, scheduledJob)
	}

	if got := scheduledIDs(t, fake); len(got) != 1 || got[0] != someJob {
		t.Errorf("the scheduled list is %v after the expiry, want the job", got)
	}

	if status := finish(t, fake); status != http.StatusUnprocessableEntity {
		t.Errorf("finish after the expiry answered %d, want 422", status)
	}
}

func TestASecondReserveCallExtendsAReservation(t *testing.T) {
	t.Parallel()

	clk := newClock()
	fake := waiting(t, clk.Now)
	reserve(t, fake, reservedFor)

	clk.advance(200 * time.Second)
	reserve(t, fake, reservedFor)
	clk.advance(200 * time.Second)

	if got := stateOf(t, fake); got != "reserved" {
		t.Fatalf("400 s after the first call and 200 s after the renewal the state is %q, "+
			"want reserved", got)
	}

	clk.advance(100 * time.Second)

	if got := stateOf(t, fake); got != scheduledJob {
		t.Errorf("300 s after the renewal the state is %q, want %q", got, scheduledJob)
	}
}

func TestAnAgentThatAcquiredAJobKeepsItPastTheReservation(t *testing.T) {
	t.Parallel()

	clk := newClock()
	fake := waiting(t, clk.Now)
	reserve(t, fake, reservedFor)
	fake.SetJobState(someJob, "running")
	clk.advance(time.Hour)

	if got := stateOf(t, fake); got != "running" {
		t.Errorf("a running job has the state %q after the reservation time, want running", got)
	}
}

func TestNotificationsNeedNoReservation(t *testing.T) {
	t.Parallel()

	fake := waiting(t, newClock().Now)

	status, _ := call(t, fake, http.MethodPost, "/notifications", stacksapi.CreateStackNotificationsRequest{
		Notifications: []stacksapi.StackNotification{{JobUUID: someJob, Detail: "no profile has these tags"}},
	})
	if status != http.StatusOK {
		t.Fatalf("notification of a scheduled job answered %d, want 200", status)
	}

	if got := len(fake.Notifications()); got != 1 {
		t.Errorf("the server holds %d notifications, want 1", got)
	}
}

func TestReserveOfAJobThatIsNotScheduledAnswersNotReserved(t *testing.T) {
	t.Parallel()

	for _, state := range []string{
		"assigned", "accepted", "running", "canceling", "timing_out",
		"finished", "canceled", "expired", "timed_out", "skipped", "broken",
	} {
		fake := waiting(t, newClock().Now)
		fake.SetJobState(someJob, state)

		status, body := call(t, fake, http.MethodPut, "/scheduled-jobs/batch-reserve",
			stacksapi.BatchReserveJobsRequest{JobUUIDs: []string{someJob}, ReservationExpirySeconds: reservedFor})
		if status != http.StatusOK {
			t.Fatalf("reserve answered %d, want 200", status)
		}

		var answer stacksapi.BatchReserveJobsResponse
		if err := json.Unmarshal(body, &answer); err != nil {
			t.Fatalf("decoding the answer: %v", err)
		}

		if len(answer.Reserved) != 0 || len(answer.NotReserved) != 1 {
			t.Errorf("reserve of a %s job gave %+v, want it not reserved", state, answer)
		}

		if got := stateOf(t, fake); got != state {
			t.Errorf("a refused reserve changed the state %q to %q", state, got)
		}
	}
}

func TestAReserveCallWithoutALifetimeHoldsTheJobForTheDefault(t *testing.T) {
	t.Parallel()

	clk := newClock()
	fake := waiting(t, clk.Now)
	reserve(t, fake, 0)
	clk.advance(899 * time.Second)

	if got := stateOf(t, fake); got != "reserved" {
		t.Fatalf("899 s after a call with no lifetime the state is %q, want reserved", got)
	}

	clk.advance(time.Second)

	if got := stateOf(t, fake); got != scheduledJob {
		t.Errorf("900 s after the call the state is %q, want %q", got, scheduledJob)
	}
}

func TestAFinishThatTheTestConfiguredToFailChangesNothing(t *testing.T) {
	t.Parallel()

	fake := waiting(t, newClock().Now)
	reserve(t, fake, reservedFor)
	fake.FinishJobStatusCode = http.StatusInternalServerError

	if status := finish(t, fake); status != http.StatusInternalServerError {
		t.Fatalf("finish answered %d, want 500", status)
	}

	if got := stateOf(t, fake); got != "reserved" {
		t.Errorf("a failed finish left the state %q, want reserved", got)
	}
}
