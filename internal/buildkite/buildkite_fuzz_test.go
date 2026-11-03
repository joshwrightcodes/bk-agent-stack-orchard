// SPDX-License-Identifier: AGPL-3.0-or-later

package buildkite_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/test/fakestacks"
)

// A line on a job page can quote text that a guest wrote, such as the status message of a machine.
// Whatever the text, the line that leaves the controller is never empty, it is at most 256
// characters, it is whole characters, and it holds no control character, no format character
// (such as a bidirectional override), and no line or paragraph separator, because a line is one
// line. A line of blanks is never sent.
func FuzzANotificationDetailIsShortAndPlain(f *testing.F) {
	f.Add("the virtual machine stopped")
	f.Add("")
	f.Add(strings.Repeat("ビ", 300))
	f.Add(strings.Repeat("x", 256))
	f.Add(strings.Repeat("x", 257))
	f.Add("failed: \x1b[31mred\x1b[0m\r\nnext line\ttab")
	f.Add("\u0085\u2028\u202e\x00\x7f")
	f.Add("a\u202eb\u200bc\u2029d\ufeffe\u2066f")
	f.Add(" \t\r\n\u2028 ")
	f.Add("e\u0301" + strings.Repeat("\u0301", 400))

	fake := fakestacks.New()
	fake.StackKey = "bk-macos-prod"
	f.Cleanup(fake.Close)

	client, err := buildkite.New(buildkite.Options{
		Token:    secret.String("bkct_a-cluster-agent-token"),
		Endpoint: fake.URL(),
		StackKey: fake.StackKey,
		Queue:    "macos-prod",
	})
	if err != nil {
		f.Fatalf("New() = %v, want a client", err)
	}

	f.Fuzz(func(t *testing.T, detail string) {
		// The status message of a machine comes from JSON, which gives whole characters only.
		if !utf8.ValidString(detail) {
			t.Skip("a status message of Orchard holds whole characters")
		}

		before := len(fake.Notifications())

		err := client.Notify(t.Context(), []buildkite.Notification{{JobID: first, Detail: detail}})
		if err != nil {
			t.Fatalf("Notify() = %v, want no error", err)
		}

		lines := fake.Notifications()[before:]
		if len(lines) != 1 {
			t.Fatalf("the server took %d lines, want 1", len(lines))
		}

		sent := lines[0].Detail

		if strings.TrimSpace(sent) == "" {
			t.Error("the line is blank, and Buildkite refuses an empty detail")
		}

		if utf8.RuneCountInString(sent) > buildkite.NotificationLimit {
			t.Errorf("the line has %d characters, want %d or fewer",
				utf8.RuneCountInString(sent), buildkite.NotificationLimit)
		}

		if !utf8.ValidString(sent) {
			t.Errorf("the line %q is not whole characters", sent)
		}

		for _, r := range sent {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
				t.Fatalf("the line holds the character %U, which a reader cannot see or which "+
					"changes what a reader sees", r)
			}
		}
	})
}

// answers is a server whose next answer the fuzzer chooses.
type answers struct {
	mu     sync.Mutex
	status int
	body   []byte
}

func (a *answers) set(status int, body []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.status, a.body = status, body
}

func (a *answers) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	status, body := a.status, a.body
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// Buildkite answers every call of the client with JSON, and a proxy or a fault can put anything in
// its place. Whatever the status and the body, each call gives a value or an error and never
// panics, and the value has no more entries than the body has bytes, so the size of a result is
// bounded by the size of the answer. The limit of the body is a separate test with a body over it,
// because a fuzzer cannot reach 16 MiB.
//
// The statuses leave out the ones that the client tries again, so that no try waits, and the calls
// share a short deadline for the case that one does. The fuzzer gives only bodies that are JSON. A
// body that is not JSON is the test of a cut answer below.
func FuzzTheClientSurvivesAnyResponseBody(f *testing.F) {
	const job1 = `"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d"`

	f.Add(uint8(0), []byte(`{"cluster_queue":{"paused":false},"jobs":[{"id":`+job1+
		`,"priority":0,"agent_query_rules":["queue=macos-prod"],"pipeline":{"slug":"app"},`+
		`"build":{"number":7}}],"page_info":{"has_next_page":true,"end_cursor":"abc"}}`))
	f.Add(uint8(0), []byte(`{"reserved":[`+job1+`],"not_reserved":[]}`))
	f.Add(uint8(0), []byte(`{"job_acquisition_tokens":[{"job_uuid":`+job1+
		`,"token":"bkua_x","expires_at":"2026-11-01T10:00:00Z"}],"not_issued":[]}`))
	f.Add(uint8(0), []byte(`{"states":{`+job1+`:"reserved"}}`))
	f.Add(uint8(0), []byte(`{"errors":[{"error":"job is finished","indexes":[0]}]}`))
	f.Add(uint8(0), []byte(`{"reserved":["not-a-job"]}`))
	f.Add(uint8(0), []byte(`null`))
	f.Add(uint8(0), []byte(`[]`))
	f.Add(uint8(0), []byte(`{}`))
	f.Add(uint8(0), []byte(`""`))
	f.Add(uint8(0), []byte(`{"jobs":[null,{"id":null}],"states":null,"reserved":[1,"x"]}`))
	f.Add(uint8(1), []byte(`{"message":"unauthorised"}`))
	f.Add(uint8(2), []byte(`{"message":"gone"}`))
	f.Add(uint8(3), []byte(`{"message":"`+strings.Repeat("x", 100000)+`"}`))
	f.Add(uint8(0), []byte(`{"states":{"`+strings.Repeat("a", 100000)+`":"x"}}`))

	statuses := []int{
		http.StatusOK, http.StatusUnauthorized, http.StatusNotFound, http.StatusBadRequest,
		http.StatusUnprocessableEntity, http.StatusConflict, http.StatusForbidden,
	}

	server := &answers{}
	httpServer := httptest.NewServer(server)
	f.Cleanup(httpServer.Close)

	client, err := buildkite.New(buildkite.Options{
		Token:    secret.String("bkct_a-cluster-agent-token"),
		Endpoint: httpServer.URL,
		StackKey: "bk-macos-prod",
		Queue:    "macos-prod",
	})
	if err != nil {
		f.Fatalf("New() = %v, want a client", err)
	}

	f.Fuzz(func(t *testing.T, status uint8, body []byte) {
		// A fuzzer that grows a body without a bound spends its time on the copy.
		if len(body) > 1<<20 {
			t.Skip("a body of this size is for the test of the limit")
		}

		if !json.Valid(body) {
			t.Skip("a body that is not JSON is retried, and has its own test")
		}

		server.set(statuses[int(status)%len(statuses)], body)

		// A body of the wrong shape is also retried, so one deadline covers all the calls.
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()

		ids := []job.ID{first, second}

		if page, err := client.ScheduledJobs(ctx, "", 0); err == nil && len(page.Jobs) > len(body) {
			t.Errorf("ScheduledJobs() gave %d jobs from %d bytes", len(page.Jobs), len(body))
		}

		if got, err := client.Reserve(ctx, ids, 0); err == nil &&
			len(got.Reserved)+len(got.NotReserved) > len(body) {
			t.Errorf("Reserve() gave more jobs than the body has bytes (%d)", len(body))
		}

		if got, err := client.Tokens(ctx, ids, 0); err == nil &&
			len(got.Issued)+len(got.NotIssued) > len(body) {
			t.Errorf("Tokens() gave more tokens than the body has bytes (%d)", len(body))
		}

		if got, err := client.States(ctx, ids); err == nil && len(got) > len(body) {
			t.Errorf("States() gave %d states from %d bytes", len(got), len(body))
		}

		_ = client.Register(ctx)
		_ = client.Deregister(ctx)
		_ = client.Finish(ctx, first, -1, "failed")
		_ = client.Notify(ctx, []buildkite.Notification{{JobID: first, Detail: "line"}})
	})
}

// A body that is cut or is not JSON gives an error and never a value or a panic. The check is that
// the call ends with an error. Each call has a short deadline, which keeps the test quick if a try
// waits.
func TestACutAnswerGivesAnErrorForEachCall(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"empty":     ``,
		"truncated": `{"jobs":[{"id":"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d","pipeline":`,
		"text":      `<html>bad gateway</html>`,
		"binary":    "\x00\xff\xfe{",
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := &answers{}
			server.set(http.StatusOK, []byte(body))

			httpServer := httptest.NewServer(server)
			t.Cleanup(httpServer.Close)

			client, err := buildkite.New(buildkite.Options{
				Token:    secret.String("bkct_a-cluster-agent-token"),
				Endpoint: httpServer.URL,
				StackKey: "bk-macos-prod",
				Queue:    "macos-prod",
			})
			if err != nil {
				t.Fatalf("New() = %v, want a client", err)
			}

			ids := []job.ID{first}

			// Each call has its own deadline, because a call that waits uses all of its own.
			call := func(name string, run func(ctx context.Context) error) {
				ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
				defer cancel()

				if err := run(ctx); err == nil {
					t.Errorf("%s = no error, want an error", name)
				}
			}

			call("ScheduledJobs()", func(ctx context.Context) error {
				_, err := client.ScheduledJobs(ctx, "", 0)

				return err
			})
			call("Reserve()", func(ctx context.Context) error {
				_, err := client.Reserve(ctx, ids, 0)

				return err
			})
			call("Tokens()", func(ctx context.Context) error {
				_, err := client.Tokens(ctx, ids, 0)

				return err
			})
			call("States()", func(ctx context.Context) error {
				_, err := client.States(ctx, ids)

				return err
			})
		})
	}
}
