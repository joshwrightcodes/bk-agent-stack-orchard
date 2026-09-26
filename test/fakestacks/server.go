// Package fakestacks is a fake Buildkite Stacks API server for the tests of this project.
//
// Buildkite maintains this fake, and it tests its own controller with it. The file comes from
// github.com/buildkite/agent-stack-k8s/v2/api/fake_agent_server.go at commit a2aec00a70a9, under
// the MIT licence below. This project copies it instead of importing it, because the package of
// that file also holds a Kubernetes controller: an import puts k8s.io/apimachinery, k8s.io/klog,
// and an older stacksapi into the module graph of this project for one test helper.
//
// This project made seven changes, because the original models only what its own controller
// calls. Each one carries a comment that starts with "Change of this project":
//
//  1. The stack key is a field. The original writes "test-stack" into every route.
//  2. A scheduled-jobs route exists, which the original does not model.
//  3. A test can set the headers of an answer, which a rate-limit test needs for Retry-After.
//  4. A reserved job leaves the scheduled-jobs list and takes the state "reserved", as the API
//     states. A test that runs a controller end to end needs the list to move.
//  5. A test can set a job state, schedule a job, pause the queue, and read the calls while a
//     controller runs, under the lock of the server. The original is read after each call returns, and a running
//     controller makes calls at any moment.
//  6. A finish call records the exit status and the detail that it sends, which a test of a job
//     that no profile carries reads. The original records the job UUID only.
//  7. A test can revoke the token while a controller runs, and every call then gets HTTP 401.
//     The original models no credential.
//
// The fake is not a conformance suite. It answers as its author believed the API answers, so a
// test against it proves the behaviour of this controller and not the behaviour of Buildkite.
// Open item O-1 of the test strategy holds: the fixtures come from a real cluster in W-24.
//
// # MIT License
//
// # Copyright (c) 2021 Buildkite
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
package fakestacks

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"time"

	"github.com/buildkite/stacksapi"
)

// DefaultStackKey is the key that the routes carry when a test sets none.
const DefaultStackKey = "test-stack"

// Server is an httptest.Server that answers as the Buildkite Stacks API. It records the calls, so
// a test can state what the controller sent as well as what it did with the answer.
type Server struct {
	server *httptest.Server
	mu     sync.Mutex

	// revoked answers every call with HTTP 401, as Buildkite does for a token that an owner
	// revoked.
	//
	// Change of this project: the original models no credential.
	revoked bool

	// StackKey is the stack that the routes answer for. A call for another stack gives 404.
	//
	// Change of this project: the original writes "test-stack" into each route, and the stack key
	// of this controller comes from its configuration.
	StackKey string

	// Headers go on the next answer of any endpoint, which a test needs for Retry-After.
	//
	// Change of this project: the original sets a status code but no header.
	Headers http.Header

	// ScheduledJobsCalls records the query of each scheduled-jobs call.
	//
	// Change of this project: the original models no scheduled-jobs route.
	ScheduledJobsCalls []ScheduledJobsQuery

	// ScheduledJobs is the page that the scheduled-jobs route gives. The zero value is an empty
	// page, which is what the API gives for a queue with no waiting jobs.
	ScheduledJobs stacksapi.ListScheduledJobsResponse

	// ScheduledJobsStatusCode is the status of the scheduled-jobs answer. Default 200.
	ScheduledJobsStatusCode int

	// ScheduledJobsError is a message to give instead of a page.
	ScheduledJobsError string

	// ReserveCalls records the job IDs passed to each ReserveJobs call.
	ReserveCalls [][]string

	// ReserveExpirySeconds records the reservation_expiry_seconds value from each ReserveJobs call.
	ReserveExpirySeconds []int

	// ReserveResponse configures the response for ReserveJobs.
	// If nil, returns all job IDs as reserved.
	ReserveResponse *stacksapi.BatchReserveJobsResponse

	// ReserveStatusCode configures the HTTP status code for ReserveJobs.
	// Default is 200.
	ReserveStatusCode int

	// ReserveError configures an error message to return.
	ReserveError string

	JobAcquisitionTokenCalls      []stacksapi.IssueJobAcquisitionTokensRequest
	JobAcquisitionTokenResponse   *stacksapi.IssueJobAcquisitionTokensResponse
	JobAcquisitionTokenStatusCode int
	JobAcquisitionTokenError      string

	// RegisterCalls records each stack registration.
	RegisterCalls []stacksapi.RegisterStackRequest

	// DeregisterCalls counts the deregistrations.
	DeregisterCalls int

	// NotificationCalls records all notification batches sent to the server.
	NotificationCalls [][]stacksapi.StackNotification

	// JobStates maps job UUIDs to their state strings for GetJobStates.
	JobStates map[string]string

	// GetJobStatesCalls records the job UUIDs of each get-states call.
	GetJobStatesCalls [][]string

	// GetJobStatesStatusCode configures the HTTP status code for GetJobStates.
	// Default is 200.
	GetJobStatesStatusCode int

	// GetJobStatesError configures an error message to return for GetJobStates.
	GetJobStatesError string

	// FinishJobCalls records the job UUIDs from each FinishJob call.
	FinishJobCalls []string

	// Change of this project: the original records the job UUID only.
	FinishJobRequests []stacksapi.FinishJobRequest

	// FinishJobStatusCode configures the HTTP status code for FinishJob.
	// Default is 200.
	FinishJobStatusCode int

	// FinishJobError configures an error message to return for FinishJob.
	FinishJobError string
}

// ScheduledJobsQuery is one call of the scheduled-jobs route.
//
// Change of this project: the original models no scheduled-jobs route.
type ScheduledJobsQuery struct {
	QueueKey string
	Limit    string
	After    string
}

// New creates and starts a fake Stacks API server. Use URL() as the endpoint of a real client.
func New() *Server {
	fake := &Server{
		StackKey:                      DefaultStackKey,
		ScheduledJobsStatusCode:       http.StatusOK,
		ReserveStatusCode:             http.StatusOK,
		JobAcquisitionTokenStatusCode: http.StatusCreated,
		GetJobStatesStatusCode:        http.StatusOK,
		FinishJobStatusCode:           http.StatusOK,
	}

	// Change of this project: the routes carry the stack key as a wildcard, and each handler
	// confirms it. The original writes "test-stack" into the pattern of each route.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /stacks/register", fake.handleRegisterStack)
	mux.HandleFunc("POST /stacks/{key}/deregister", fake.handleDeregisterStack)
	mux.HandleFunc("GET /stacks/{key}/scheduled-jobs", fake.handleScheduledJobs)
	mux.HandleFunc("PUT /stacks/{key}/scheduled-jobs/batch-reserve", fake.handleReserveJobs)
	mux.HandleFunc("POST /stacks/{key}/job-acquisition-tokens", fake.handleIssueJobAcquisitionTokens)
	mux.HandleFunc("POST /stacks/{key}/notifications", fake.handleNotifications)
	mux.HandleFunc("POST /stacks/{key}/jobs/get-states", fake.handleGetJobStates)
	mux.HandleFunc("POST /stacks/{key}/jobs/{uuid}/finish", fake.handleFinishJob)

	fake.server = httptest.NewServer(mux)

	return fake
}

// URL returns the base URL of the fake server.
func (f *Server) URL() string {
	return f.server.URL
}

// Close shuts down the fake server.
func (f *Server) Close() {
	f.server.Close()
}

// Revoke refuses the agent token from now on: every call gets HTTP 401.
//
// Change of this project: the original models no credential.
func (f *Server) Revoke() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.revoked = true
}

// stack confirms that a call names the stack that this server answers for, with a token that
// the server still accepts.
//
// Change of this project: the original has one stack key in its routes, so it needs no check.
func (f *Server) stack(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	revoked := f.revoked
	f.mu.Unlock()

	if revoked {
		f.writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "invalid token"})

		return false
	}

	if r.PathValue("key") == f.StackKey {
		return true
	}

	f.writeJSON(w, http.StatusNotFound, map[string]string{"message": "stack not found"})

	return false
}

// writeJSON encodes payload as JSON and writes it with the given status code.
func (f *Server) writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		http.Error(w, "fake: failed to encode response: "+err.Error(), http.StatusInternalServerError)

		return
	}

	// Change of this project: a test sets the headers of an answer, for Retry-After.
	for name, values := range f.Headers {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("fake: failed to write response: %v", err)
	}
}

func (f *Server) handleRegisterStack(w http.ResponseWriter, r *http.Request) {
	var req stacksapi.RegisterStackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	f.mu.Lock()
	f.RegisterCalls = append(f.RegisterCalls, req)
	f.mu.Unlock()

	f.writeJSON(w, http.StatusOK, stacksapi.RegisterStackResponse{
		Key:             req.Key,
		ClusterQueueKey: req.QueueKey,
		Type:            req.Type,
		Metadata:        req.Metadata,
		State:           stacksapi.StackStateConnected,
	})
}

// handleDeregisterStack answers the shutdown call.
//
// Change of this project: the original models no deregistration.
func (f *Server) handleDeregisterStack(w http.ResponseWriter, r *http.Request) {
	if !f.stack(w, r) {
		return
	}

	f.mu.Lock()
	f.DeregisterCalls++
	f.mu.Unlock()

	f.writeJSON(w, http.StatusOK, struct{}{})
}

// handleScheduledJobs gives one page of the jobs that wait in the queue.
//
// Change of this project: the original models no scheduled-jobs route.
func (f *Server) handleScheduledJobs(w http.ResponseWriter, r *http.Request) {
	if !f.stack(w, r) {
		return
	}

	query := r.URL.Query()

	f.mu.Lock()
	f.ScheduledJobsCalls = append(f.ScheduledJobsCalls, ScheduledJobsQuery{
		QueueKey: query.Get("queue_key"),
		Limit:    query.Get("limit"),
		After:    query.Get("after"),
	})
	f.mu.Unlock()

	if f.ScheduledJobsError != "" {
		f.writeJSON(w, f.ScheduledJobsStatusCode, map[string]string{"message": f.ScheduledJobsError})

		return
	}

	f.mu.Lock()
	page := f.ScheduledJobs
	page.Jobs = append([]stacksapi.ScheduledJob{}, f.ScheduledJobs.Jobs...)
	f.mu.Unlock()

	f.writeJSON(w, f.ScheduledJobsStatusCode, page)
}

func (f *Server) handleReserveJobs(w http.ResponseWriter, r *http.Request) {
	if !f.stack(w, r) {
		return
	}

	var req stacksapi.BatchReserveJobsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	f.mu.Lock()
	f.ReserveCalls = append(f.ReserveCalls, req.JobUUIDs)
	f.ReserveExpirySeconds = append(f.ReserveExpirySeconds, req.ReservationExpirySeconds)
	f.mu.Unlock()

	if f.ReserveError != "" {
		f.writeJSON(w, f.ReserveStatusCode, map[string]string{"message": f.ReserveError})

		return
	}

	resp := f.ReserveResponse
	if resp == nil {
		// Default: all jobs reserved successfully
		resp = &stacksapi.BatchReserveJobsResponse{
			Reserved:    req.JobUUIDs,
			NotReserved: []string{},
		}
	}

	// Change of this project: a reserved job leaves the scheduled-jobs list and takes the state
	// "reserved", which is what the API documents. The original keeps the list as a test set it.
	f.mu.Lock()
	f.reserve(resp.Reserved)
	f.mu.Unlock()

	f.writeJSON(w, f.ReserveStatusCode, resp)
}

// reserve moves jobs from the scheduled-jobs list to the state "reserved". The caller holds the
// lock.
//
// Change of this project: the original models no list for a reservation to change.
func (f *Server) reserve(ids []string) {
	held := make(map[string]bool, len(ids))
	for _, id := range ids {
		held[id] = true
	}

	waiting := f.ScheduledJobs.Jobs[:0:0]
	for _, scheduled := range f.ScheduledJobs.Jobs {
		if !held[scheduled.ID] {
			waiting = append(waiting, scheduled)
		}
	}

	f.ScheduledJobs.Jobs = waiting

	if f.JobStates == nil {
		f.JobStates = map[string]string{}
	}

	for id := range held {
		if _, known := f.JobStates[id]; !known {
			f.JobStates[id] = "reserved"
		}
	}
}

func (f *Server) handleIssueJobAcquisitionTokens(w http.ResponseWriter, r *http.Request) {
	if !f.stack(w, r) {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	var req stacksapi.IssueJobAcquisitionTokensRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	f.mu.Lock()
	f.JobAcquisitionTokenCalls = append(f.JobAcquisitionTokenCalls, req)
	f.mu.Unlock()

	if f.JobAcquisitionTokenError != "" {
		f.writeJSON(w, f.JobAcquisitionTokenStatusCode,
			map[string]string{"message": f.JobAcquisitionTokenError})

		return
	}

	resp := f.JobAcquisitionTokenResponse
	if resp == nil {
		resp = &stacksapi.IssueJobAcquisitionTokensResponse{}
		for _, id := range req.JobUUIDs {
			resp.JobAcquisitionTokens = append(resp.JobAcquisitionTokens, stacksapi.JobAcquisitionToken{
				JobUUID:   id,
				Token:     "bkjat_" + id,
				ExpiresAt: time.Now().Add(15 * time.Minute),
			})
		}
	}

	f.writeJSON(w, f.JobAcquisitionTokenStatusCode, resp)
}

func (f *Server) handleGetJobStates(w http.ResponseWriter, r *http.Request) {
	if !f.stack(w, r) {
		return
	}

	var req stacksapi.GetJobStatesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	f.mu.Lock()
	f.GetJobStatesCalls = append(f.GetJobStatesCalls, req.JobUUIDs)
	f.mu.Unlock()

	if f.GetJobStatesError != "" {
		f.writeJSON(w, f.GetJobStatesStatusCode, map[string]string{"message": f.GetJobStatesError})

		return
	}

	states := make(map[string]string)

	f.mu.Lock()
	for _, id := range req.JobUUIDs {
		if s, ok := f.JobStates[id]; ok {
			states[id] = s
		}
	}
	f.mu.Unlock()

	f.writeJSON(w, f.GetJobStatesStatusCode, stacksapi.GetJobStatesResponse{States: states})
}

func (f *Server) handleFinishJob(w http.ResponseWriter, r *http.Request) {
	if !f.stack(w, r) {
		return
	}

	// Change of this project: the request carries the exit status and the detail.
	var req stacksapi.FinishJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid JSON"})

		return
	}

	req.JobUUID = r.PathValue("uuid")

	f.mu.Lock()
	f.FinishJobCalls = append(f.FinishJobCalls, r.PathValue("uuid"))
	f.FinishJobRequests = append(f.FinishJobRequests, req)
	f.mu.Unlock()

	if f.FinishJobError != "" {
		f.writeJSON(w, f.FinishJobStatusCode, map[string]string{"message": f.FinishJobError})

		return
	}

	f.writeJSON(w, f.FinishJobStatusCode, struct{}{})
}

func (f *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	if !f.stack(w, r) {
		return
	}

	var req stacksapi.CreateStackNotificationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	f.mu.Lock()
	f.NotificationCalls = append(f.NotificationCalls, req.Notifications)
	f.mu.Unlock()

	f.writeJSON(w, http.StatusOK, stacksapi.CreateStackNotificationsResponse{})
}

// SetJobState gives a job a state, as an agent that acquires and runs it would.
//
// Change of this project: a test that runs a controller end to end moves a job while the
// controller reads it, so the write takes the lock of the server.
func (f *Server) SetJobState(id, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.JobStates == nil {
		f.JobStates = map[string]string{}
	}

	f.JobStates[id] = state
}

// Schedule puts jobs on the scheduled-jobs list in the state "scheduled", as Buildkite does when a
// reservation expires.
//
// Change of this project: a test that runs a controller end to end moves a job while the
// controller reads it, so the write takes the lock of the server.
func (f *Server) Schedule(jobs ...stacksapi.ScheduledJob) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.JobStates == nil {
		f.JobStates = map[string]string{}
	}

	for _, scheduled := range jobs {
		f.ScheduledJobs.Jobs = append(f.ScheduledJobs.Jobs, scheduled)
		f.JobStates[scheduled.ID] = "scheduled"
	}
}

// Pause stops or resumes the dispatch of the queue, as an operator does.
//
// Change of this project: a test that runs a controller end to end moves the queue while the
// controller reads it, so the write takes the lock of the server.
func (f *Server) Pause(paused bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.ScheduledJobs.ClusterQueue.Paused = paused
}

// Reserved reports the jobs that the server holds for this stack.
//
// Change of this project: the original is read after a call returns.
func (f *Server) Reserved() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	var held []string

	for _, call := range f.ReserveCalls {
		held = append(held, call...)
	}

	return held
}

// Notifications gives every line that the server received, in order.
//
// Change of this project: the original is read after a call returns.
func (f *Server) Notifications() []stacksapi.StackNotification {
	f.mu.Lock()
	defer f.mu.Unlock()

	var lines []stacksapi.StackNotification

	for _, call := range f.NotificationCalls {
		lines = append(lines, call...)
	}

	return lines
}

// Finished gives every finish call that the server received, in order.
//
// Change of this project: the original records the job UUID only.
func (f *Server) Finished() []stacksapi.FinishJobRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.FinishJobRequests)
}

// Deregistrations counts the withdrawals of the stack.
//
// Change of this project: the original models no deregistration.
func (f *Server) Deregistrations() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.DeregisterCalls
}
