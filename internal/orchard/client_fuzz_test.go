// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package orchard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
)

// Orchard answers each call with JSON, and a proxy or a fault can put anything in its place.
// Whatever the status and the body, each call gives a value or an error and never panics, a list
// has no more entries than the body has bytes, and an error never carries more of the body than
// the message of Orchard, which the client reads to 64 KiB at most. The limits of the body are
// separate tests with a body over them, because a fuzzer cannot reach 16 MiB.
func FuzzTheClientSurvivesAnyResponseBody(f *testing.F) {
	f.Add(uint16(200), []byte(`[{"name":"bk-macos-prod-018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d",`+
		`"status":"running","status_message":"the agent started","worker":"mac-03",`+
		`"createdAt":"2026-11-01T10:00:00Z","scheduled_at":"2026-11-01T10:00:05Z",`+
		`"started_at":"2026-11-01T10:00:20Z","resources":{"org.cirruslabs.tart-vms":1},`+
		`"labels":{"pool":"general"}}]`))
	f.Add(uint16(200), []byte(`[{"name":"mac-03","last_seen":"2026-11-01T10:00:00Z",`+
		`"scheduling_paused":true,"resources":{"org.cirruslabs.tart-vms":2},`+
		`"labels":{"pool":"general"}}]`))
	f.Add(uint16(200), []byte(`[{"kind":"log_line","timestamp":1,"payload":"first"},`+
		`{"kind":"log_line","timestamp":2,"payload":"second"}]`))
	f.Add(uint16(200), []byte(``))
	f.Add(uint16(200), []byte(`null`))
	f.Add(uint16(200), []byte(`[]`))
	f.Add(uint16(200), []byte(`{}`))
	f.Add(uint16(200), []byte(`[null,{"createdAt":"yesterday"},{"resources":{"a":-1}}]`))
	f.Add(uint16(200), []byte(`[{"name":"bk-a","status":"runn`))
	f.Add(uint16(200), []byte(`[{"payload":"`+strings.Repeat("x", 200000)+`"}]`))
	f.Add(uint16(400), []byte(`{"message":"the specification is not valid"}`))
	f.Add(uint16(500), []byte(`{"message":"`+strings.Repeat("x", 100000)+`"}`))
	f.Add(uint16(429), []byte(`not json`))
	f.Add(uint16(404), []byte(``))
	f.Add(uint16(409), []byte(`{"message":"exists"}`))
	f.Add(uint16(99), []byte(`[]`))

	var (
		mu     sync.Mutex
		status int
		body   []byte
	)

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		code, data := status, body
		mu.Unlock()

		w.WriteHeader(code)
		_, _ = w.Write(data)
	}))
	f.Cleanup(httpServer.Close)

	client, err := orchard.New(orchard.Options{
		URL:            httpServer.URL,
		ServiceAccount: "bk-agent-stack-orchard",
		Token:          secret.String("orchard-token"),
		Retry: retry.Policy{
			Attempts: 2,
			Sleep:    func(context.Context, time.Duration) error { return nil },
		},
	})
	if err != nil {
		f.Fatalf("New() = %v", err)
	}

	f.Fuzz(func(t *testing.T, code uint16, data []byte) {
		// A fuzzer that grows a body without a bound spends its time on the copy.
		if len(data) > 1<<20 {
			t.Skip("a body of this size is for the test of the limit")
		}

		// A status below 200 is an interim answer, and 204 and 304 have no body, so they stand for
		// a plain 200. Every other status from 200 to 999 reaches the client.
		code = 100 + code%900
		if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified {
			code = http.StatusOK
		}

		mu.Lock()
		status, body = int(code), data
		mu.Unlock()

		ctx := t.Context()

		if got, err := client.ListVMs(ctx); err == nil && len(got) > len(data) {
			t.Errorf("ListVMs() gave %d machines from %d bytes", len(got), len(data))
		}

		if got, err := client.ListWorkers(ctx); err == nil && len(got) > len(data) {
			t.Errorf("ListWorkers() gave %d workers from %d bytes", len(got), len(data))
		}

		if got, err := client.Events(ctx, "bk-a-b"); err == nil && len(got) > len(data) {
			t.Errorf("Events() gave %d events from %d bytes", len(got), len(data))
		}

		if err := client.CreateVM(ctx, orchard.VM{Name: "bk-a-b"}); err != nil &&
			len(err.Error()) > 64<<10+1024 {
			t.Errorf("CreateVM() gave an error of %d bytes, want the message limited to 64 KiB",
				len(err.Error()))
		}

		_ = client.DeleteVM(ctx, "bk-a-b")
	})
}
