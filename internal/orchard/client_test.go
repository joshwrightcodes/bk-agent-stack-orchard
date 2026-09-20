package orchard_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// server gives a client that talks to a handler, with the retry wait removed so that a test of
// a retry does not wait for it.
func server(t *testing.T, handler http.HandlerFunc) (*orchard.Client, *httptest.Server) {
	t.Helper()

	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)

	client, err := orchard.New(orchard.Options{
		URL:            s.URL,
		ServiceAccount: "bk-agent-stack-orchard",
		Token:          config.Secret("orchard-token"),
		RetryWait:      time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	return client, s
}

func TestNewRejectsOptionsThatCannotWork(t *testing.T) {
	for name, options := range map[string]orchard.Options{
		"no url":             {ServiceAccount: "a", Token: "b"},
		"no service account": {URL: "https://orchard.internal:6120", Token: "b"},
		"no token":           {URL: "https://orchard.internal:6120", ServiceAccount: "a"},
		"a url that is not a url": {
			URL: "orchard.internal", ServiceAccount: "a", Token: "b",
		},
	} {
		if _, err := orchard.New(options); err == nil {
			t.Errorf("New() with %s = nil, want an error", name)
		}
	}
}

// Orchard authenticates a service account with HTTP basic, and the name and the token are the
// two halves of it.
func TestTheClientAuthenticatesWithTheServiceAccount(t *testing.T) {
	var got string

	client, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		writeJSON(t, w, []orchard.VM{})
	})

	if _, err := client.ListVMs(context.Background()); err != nil {
		t.Fatalf("ListVMs() = %v", err)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("bk-agent-stack-orchard:orchard-token"))
	if got != want {
		t.Errorf("Authorization = %q, want the service account and its token", got)
	}
}

func TestCreateVMSendsTheSpecification(t *testing.T) {
	var body map[string]any

	client, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/vms" {
			t.Errorf("CreateVM sent %s %s, want POST /v1/vms", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})

	spec := vmspec.VM{Name: "bk-macos-prod-018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d", Image: "img", CPU: 8}
	if err := client.CreateVM(context.Background(), spec); err != nil {
		t.Fatalf("CreateVM() = %v", err)
	}

	if body["name"] != spec.Name || body["image"] != "img" {
		t.Errorf("the body lost fields of the specification: %v", body)
	}
}

// A second create of the same name means that the first one arrived. The launcher treats that as
// a successful launch, so the client must not hide the difference behind a plain error.
func TestCreateVMReportsAConflictOnItsOwn(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	err := client.CreateVM(context.Background(), vmspec.VM{Name: "bk-a-b"})
	if !errors.Is(err, orchard.ErrAlreadyExists) {
		t.Errorf("CreateVM() = %v, want ErrAlreadyExists", err)
	}
}

func TestListVMsReadsEveryFieldThatTheControllerUses(t *testing.T) {
	created := time.Now().Add(-90 * time.Second).UTC().Truncate(time.Second)

	client, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/vms" {
			t.Errorf("ListVMs asked for %s, want /v1/vms", r.URL.Path)
		}
		writeJSON(t, w, []map[string]any{{
			"name":          "bk-macos-prod-018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d",
			"status":        "running",
			"statusMessage": "the agent started",
			"worker":        "mac-03",
			"createdAt":     created,
			"scheduled_at":  created.Add(5 * time.Second),
			"started_at":    created.Add(20 * time.Second),
			"resources":     map[string]uint64{"org.cirruslabs.tart-vms": 1},
			"labels":        map[string]string{"pool": "general"},
		}})
	})

	vms, err := client.ListVMs(context.Background())
	if err != nil {
		t.Fatalf("ListVMs() = %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("ListVMs() gave %d machines, want 1", len(vms))
	}

	vm := vms[0]
	if vm.Status != orchard.StatusRunning {
		t.Errorf("Status = %q, want running", vm.Status)
	}
	if vm.Worker != "mac-03" || vm.StatusMessage != "the agent started" {
		t.Errorf("Worker = %q, StatusMessage = %q", vm.Worker, vm.StatusMessage)
	}
	if !vm.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", vm.CreatedAt, created)
	}
	if vm.Resources["org.cirruslabs.tart-vms"] != 1 || vm.Labels["pool"] != "general" {
		t.Errorf("Resources = %v, Labels = %v", vm.Resources, vm.Labels)
	}
}

func TestDeleteVMAsksForTheName(t *testing.T) {
	var path string

	client, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	if err := client.DeleteVM(context.Background(), "bk-macos-prod-x"); err != nil {
		t.Fatalf("DeleteVM() = %v", err)
	}
	if path != "DELETE /v1/vms/bk-macos-prod-x" {
		t.Errorf("DeleteVM sent %q", path)
	}
}

// A machine that another tick already deleted is not a problem: the controller wanted it gone.
func TestDeleteVMAcceptsAMachineThatIsAlreadyGone(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	if err := client.DeleteVM(context.Background(), "bk-a-b"); err != nil {
		t.Errorf("DeleteVM() of a machine that is gone = %v, want no error", err)
	}
}

func TestListWorkersReadsWhatAdmissionNeeds(t *testing.T) {
	seen := time.Now().UTC().Truncate(time.Second)

	client, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workers" {
			t.Errorf("ListWorkers asked for %s, want /v1/workers", r.URL.Path)
		}
		writeJSON(t, w, []map[string]any{{
			"name":              "mac-03",
			"last_seen":         seen,
			"scheduling_paused": true,
			"resources":         map[string]uint64{"org.cirruslabs.tart-vms": 2},
			"labels":            map[string]string{"pool": "general"},
		}})
	})

	workers, err := client.ListWorkers(context.Background())
	if err != nil {
		t.Fatalf("ListWorkers() = %v", err)
	}
	if len(workers) != 1 {
		t.Fatalf("ListWorkers() gave %d workers, want 1", len(workers))
	}

	worker := workers[0]
	if !worker.SchedulingPaused || worker.Resources["org.cirruslabs.tart-vms"] != 2 {
		t.Errorf("worker = %+v", worker)
	}
	if worker.Offline(3 * time.Minute) {
		t.Error("Offline() = true for a worker that Orchard saw a moment ago")
	}
	if !worker.Offline(time.Nanosecond) {
		t.Error("Offline() = false for a worker that nothing saw inside the timeout")
	}
}

// The acquire timeout puts the last lines of the startup script on the job page, so the client
// reads the events of a machine.
func TestEventsGivesTheLogLinesOfTheStartupScript(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/vms/bk-a-b/events" {
			t.Errorf("Events asked for %s", r.URL.Path)
		}
		writeJSON(t, w, []map[string]any{
			{"kind": "log_line", "timestamp": 1, "payload": "first"},
			{"kind": "log_line", "timestamp": 2, "payload": "second"},
		})
	})

	events, err := client.Events(context.Background(), "bk-a-b")
	if err != nil {
		t.Fatalf("Events() = %v", err)
	}
	if len(events) != 2 || events[1].Payload != "second" {
		t.Errorf("Events() = %+v", events)
	}
}

func TestTheClientRetriesAServerError(t *testing.T) {
	var calls int

	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)

			return
		}
		writeJSON(t, w, []orchard.VM{})
	})

	if _, err := client.ListVMs(context.Background()); err != nil {
		t.Fatalf("ListVMs() = %v, want the retry to succeed", err)
	}
	if calls != 3 {
		t.Errorf("the client made %d calls, want 3", calls)
	}
}

// A request that Orchard refuses is not worth sending again.
func TestTheClientDoesNotRetryARefusal(t *testing.T) {
	var calls int

	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	})

	if _, err := client.ListVMs(context.Background()); err == nil {
		t.Fatal("ListVMs() = nil, want an error")
	}
	if calls != 1 {
		t.Errorf("the client made %d calls, want 1", calls)
	}
}

func TestTheClientStopsWhenTheContextEnds(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.ListVMs(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ListVMs() = %v, want the cancellation", err)
	}
}

// An error must say what failed without carrying the token that authenticated the call.
func TestAnErrorDoesNotCarryTheToken(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"no"}`))
	})

	_, err := client.ListVMs(context.Background())
	if err == nil {
		t.Fatal("ListVMs() = nil, want an error")
	}
	if strings.Contains(err.Error(), "orchard-token") {
		t.Errorf("the error carries the token: %v", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("writing the response: %v", err)
	}
}
