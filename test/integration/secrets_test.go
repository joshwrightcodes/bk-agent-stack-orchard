// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

//go:build integration

package integration_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Two values that no other test uses, so that a find of either one in a source is a leak and not
// a coincidence. They stand in for the cluster agent token and the Orchard credential.
const (
	secretAgentToken   = "bkct_integration_7f3a9c51d2e84b06"
	secretOrchardToken = "orchard_integration_b81e40d7a95c2f13"
)

// assertNoSecret fails the test when a secret is in the data of a source. The message names the
// source and never the value, because the value is what must not reach a log.
func assertNoSecret(t *testing.T, source string, data []byte, secrets ...string) {
	t.Helper()

	for _, secret := range secrets {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("a token leaked into %s (%d bytes searched)", source, len(data))
		}
	}
}

// otlpSink is a collector that keeps every body that the controller sends it. The controller
// sends OTLP over HTTP with protobuf, and a string attribute is readable in the raw bytes.
type otlpSink struct {
	server *httptest.Server

	mu     sync.Mutex
	bodies map[string][]byte
}

func newOTLPSink(t *testing.T) *otlpSink {
	t.Helper()

	sink := &otlpSink{bodies: map[string][]byte{}}
	sink.server = httptest.NewServer(http.HandlerFunc(sink.receive))
	t.Cleanup(sink.server.Close)

	return sink
}

func (s *otlpSink) receive(w http.ResponseWriter, r *http.Request) {
	var reader io.Reader = r.Body

	if r.Header.Get("Content-Encoding") == "gzip" {
		unzipped, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		reader = unzipped
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	s.mu.Lock()
	s.bodies[r.URL.Path] = append(s.bodies[r.URL.Path], body...)
	s.mu.Unlock()

	// An empty export response is a success in OTLP.
	w.Header().Set("Content-Type", "application/x-protobuf")
}

// received gives what one route got, and nothing when the route got nothing.
func (s *otlpSink) received(path string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	return bytes.Clone(s.bodies[path])
}

// environment gives the variables that send the telemetry of the controller to the sink, fast
// enough for a test that lasts seconds.
func (s *otlpSink) environment() map[string]string {
	return map[string]string{
		"OTEL_TRACES_EXPORTER":           "otlp",
		"OTEL_METRICS_EXPORTER":          "otlp",
		"OTEL_LOGS_EXPORTER":             "otlp",
		"OTEL_EXPORTER_OTLP_ENDPOINT":    s.server.URL,
		"OTEL_EXPORTER_OTLP_PROTOCOL":    "http/protobuf",
		"OTEL_EXPORTER_OTLP_COMPRESSION": "none",
		"OTEL_METRIC_EXPORT_INTERVAL":    "500",
		"OTEL_BSP_SCHEDULE_DELAY":        "200",
		"OTEL_BLRP_SCHEDULE_DELAY":       "200",
	}
}

// rawGet gives the body of one answer of Orchard, unparsed, so that a field that this project
// does not model is searched too.
func rawGet(t *testing.T, url string) []byte {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building the request for %s: %v", url, err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("asking %s: %v", url, err)
	}

	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}

	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s answered %s", url, response.Status)
	}

	return body
}

// readLog gives what the controller wrote to its log file.
func readLog(t *testing.T, running *controller) []byte {
	t.Helper()

	body, err := os.ReadFile(running.log)
	if err != nil {
		t.Fatalf("reading the log of the controller: %v", err)
	}

	return body
}

// requireWork fails the test when a source holds nothing, because a search of an empty source
// finds no secret and proves nothing.
func requireWork(t *testing.T, source string, data []byte, marker string) {
	t.Helper()

	if len(data) == 0 || !strings.Contains(string(data), marker) {
		t.Fatalf("%s holds no %q, so a search of it proves nothing", source, marker)
	}
}
