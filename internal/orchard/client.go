// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

// Package orchard talks to the Orchard REST API.
//
// The client belongs to this project. Orchard carries a licence with a field-of-use restriction,
// and this project publishes an image under the AGPL-3.0, which forbids a further restriction on
// a distributed work. The controller therefore calls the API and imports no Orchard package
// (ADR-0010).
//
// The client covers five calls: create a machine, list the machines, delete a machine, list the
// workers, and read the events of a machine. It defines its own types for the fields that the
// controller uses, and it ignores the rest of the API.
package orchard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
)

// Errors that a caller decides about.
var (
	// ErrAlreadyExists is a machine whose name Orchard already has. A second create of the same
	// name means that the first one arrived, which the launcher treats as a success.
	ErrAlreadyExists = errors.New("a virtual machine of this name exists")

	// ErrNotFound is a machine or a worker that Orchard does not have.
	ErrNotFound = errors.New("orchard does not have this resource")

	// ErrUnauthorized is a credential that Orchard refused with 401 or 403, such as a token of the
	// service account that an operator revoked or rotated.
	ErrUnauthorized = errors.New("orchard refused the credentials of the service account")

	// ErrRateLimited is a rate limit of Orchard (429) that outlived the tries of the retry policy.
	ErrRateLimited = errors.New("orchard applied a rate limit")
)

// Status values of a machine inside Orchard. The Buildkite job state, not this status, decides
// when a machine may go.
const (
	// StatusPending is a machine that Orchard has not started yet.
	StatusPending = "pending"

	// StatusRunning is a machine whose process runs. The guest may still be starting.
	StatusRunning = "running"

	// StatusFailed is a machine that did not reach the running state, or that stopped.
	StatusFailed = "failed"
)

// Offline reports a worker that nothing has seen inside the timeout, as of a time. Orchard decides
// the same way, from the time that the worker last reported.
//
// The caller gives the time. A decision that reads the clock itself cannot be tested at a fixed
// moment, and a test that fixes the last report of a worker then passes only while the clock of
// the machine that runs it stays near that moment.
func (w Worker) Offline(now time.Time, timeout time.Duration) bool {
	return now.Sub(w.LastSeen) > timeout
}

// API is the five calls of the client. The controller builds its parts around it, so that the
// component suite can give them one fake of Orchard.
type API interface {
	CreateVM(ctx context.Context, spec VM) error
	ListVMs(ctx context.Context) ([]VM, error)
	DeleteVM(ctx context.Context, name string) error
	ListWorkers(ctx context.Context) ([]Worker, error)
	Events(ctx context.Context, name string) ([]Event, error)
}

var _ API = (*Client)(nil)

// Options configures the client.
type Options struct {
	URL            string
	ServiceAccount string
	Token          secret.String

	// HTTPClient carries the transport. The controller gives one with telemetry.
	HTTPClient *http.Client

	// Retry says how the client tries a call again after an error that can pass. The zero value
	// is retry.Default, the policy of the Buildkite client too.
	Retry retry.Policy

	// Now is the clock that reads a Retry-After date. A nil value gives the clock of the machine.
	Now func() time.Time
}

// Client calls the Orchard API for one controller.
type Client struct {
	url            *url.URL
	serviceAccount string
	token          secret.String
	http           *http.Client
	retry          retry.Policy
	now            func() time.Time
}

// New checks the options and gives a client.
func New(options Options) (*Client, error) {
	var problems []error

	parsed, err := url.ParseRequestURI(options.URL)
	if err != nil {
		problems = append(problems, fmt.Errorf("the Orchard URL %q is not a URL", options.URL))
	}
	if options.ServiceAccount == "" {
		problems = append(problems, errors.New("the Orchard service account is empty"))
	}
	if options.Token == "" {
		problems = append(problems, errors.New("the Orchard token is empty"))
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}

	client := &Client{
		url:            parsed,
		serviceAccount: options.ServiceAccount,
		token:          options.Token,
		http:           options.HTTPClient,
		retry:          options.Retry,
		now:            options.Now,
	}

	if client.now == nil {
		client.now = time.Now //nolint:forbidigo // the default of a nil clock
	}

	if client.http == nil {
		client.http = &http.Client{Timeout: 30 * time.Second}
	}

	return client, nil
}

// CreateVM asks Orchard for one machine. A name that exists gives ErrAlreadyExists.
func (c *Client) CreateVM(ctx context.Context, spec VM) error {
	// G117: the specification carries the job acquisition token, and the image password when a
	// profile uses the SSH transport. The worker needs both, so this call is where they travel.
	// The specification type refuses to print them anywhere else.
	body, err := json.Marshal(spec) //nolint:gosec
	if err != nil {
		return fmt.Errorf("writing the specification of %s: %w", spec.Name, err)
	}

	return c.do(ctx, http.MethodPost, "/v1/vms", body, nil)
}

// ListVMs gives every machine that Orchard has. The list has no filter for a name, so the
// controller selects its own machines by their name prefix.
func (c *Client) ListVMs(ctx context.Context) ([]VM, error) {
	var vms []VM
	if err := c.do(ctx, http.MethodGet, "/v1/vms", nil, &vms); err != nil {
		return nil, err
	}

	return vms, nil
}

// DeleteVM removes one machine. A machine that Orchard does not have is not an error, because
// the caller wanted it gone.
func (c *Client) DeleteVM(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodDelete, "/v1/vms/"+url.PathEscape(name), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}

	return err
}

// ListWorkers gives every worker, with the resources and the labels that admission counts.
func (c *Client) ListWorkers(ctx context.Context) ([]Worker, error) {
	var workers []Worker
	if err := c.do(ctx, http.MethodGet, "/v1/workers", nil, &workers); err != nil {
		return nil, err
	}

	return workers, nil
}

// Events gives the lines that the startup script of a machine wrote.
func (c *Client) Events(ctx context.Context, name string) ([]Event, error) {
	var events []Event
	path := "/v1/vms/" + url.PathEscape(name) + "/events"
	if err := c.do(ctx, http.MethodGet, path, nil, &events); err != nil {
		return nil, err
	}

	return events, nil
}

// do sends one request, and sends it again by the retry policy while the error can pass.
//
// A create is safe to send again after a failure in transport, because the name of a machine is
// fixed: a create that arrived the first time gives ErrAlreadyExists the second time, which the
// launcher takes as a success.
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	err := c.retry.Do(ctx, func(ctx context.Context) error {
		return c.attempt(ctx, method, path, body, out)
	})
	if retry.Retryable(err) {
		return fmt.Errorf("orchard %s %s: %w", method, path, err)
	}

	return err
}

func (c *Client) attempt(ctx context.Context, method, path string, body []byte, out any) error {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	request, err := c.request(ctx, method, path, reader)
	if err != nil {
		return err
	}

	response, err := c.http.Do(request)
	if err != nil {
		return retry.Again(err, 0)
	}
	defer func() {
		drain(response.Body)
		_ = response.Body.Close()
	}()

	if err := c.statusError(response); err != nil {
		return err
	}

	if out == nil {
		return nil
	}

	data, err := readLimited(response.Body, maxResponseBytes)
	if err != nil {
		return fmt.Errorf("reading the answer of %s %s: %w", method, path, err)
	}

	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("reading the answer of %s %s: %w", method, path, err)
	}

	return nil
}

func (c *Client) request(ctx context.Context, method, path string, body *bytes.Reader) (
	*http.Request, error,
) {
	target := c.url.JoinPath(path)

	var request *http.Request
	var err error

	if body == nil {
		request, err = http.NewRequestWithContext(ctx, method, target.String(), nil)
	} else {
		request, err = http.NewRequestWithContext(ctx, method, target.String(), body)
		if request != nil {
			request.Header.Set("Content-Type", "application/json")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("building the request for %s %s: %w", method, path, err)
	}

	// Orchard authenticates a service account with HTTP basic: the name and the token.
	request.SetBasicAuth(c.serviceAccount, c.token.Reveal())
	request.Header.Set("Accept", "application/json")

	return request, nil
}

// statusError turns a status into the error that a caller decides about. The message of Orchard
// travels with it, and the token never does.
func (c *Client) statusError(response *http.Response) error {
	switch {
	case response.StatusCode < http.StatusBadRequest:
		return nil
	case response.StatusCode == http.StatusConflict:
		return ErrAlreadyExists
	case response.StatusCode == http.StatusNotFound:
		return ErrNotFound
	}

	message := apiMessage(response)

	// A server error, or a limit, can pass. A refusal of the request does not. A server that
	// names a wait gets it.
	switch {
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: orchard answered %s: %s", ErrUnauthorized, response.Status, message)
	case response.StatusCode == http.StatusTooManyRequests:
		return retry.Again(
			fmt.Errorf("%w: orchard answered %s: %s", ErrRateLimited, response.Status, message),
			retry.ParseAfter(response.Header.Get("Retry-After"), c.now()))
	case response.StatusCode >= http.StatusInternalServerError:
		return retry.Again(fmt.Errorf("orchard answered %s: %s", response.Status, message),
			retry.ParseAfter(response.Header.Get("Retry-After"), c.now()))
	}

	return fmt.Errorf("orchard answered %s: %s", response.Status, message)
}

// apiMessage reads the message that Orchard writes with an error.
func apiMessage(response *http.Response) string {
	var body struct {
		Message string `json:"message"`
	}

	data, err := readLimited(response.Body, maxErrorBytes)
	if err != nil || json.Unmarshal(data, &body) != nil || body.Message == "" {
		return "no message"
	}

	return body.Message
}

// The most that the client reads from the body of an answer. A body over the limit fails,
// because a cut body can still decode into a list that is short.
const (
	maxResponseBytes = 16 << 20 // 16 MiB
	maxErrorBytes    = 64 << 10 // 64 KiB, for an error, where only a message matters
)

// drain reads what is left of a body, up to the limit of an error body.
//
// HTTP/1.1 keeps a connection for the next call only after the body of the answer was read to its
// end. A create, a delete, a conflict, and a machine that is gone all answer with a body that the
// client does not need, and without this read each of them would close its connection, so that
// the next call opens a new one, with a new TLS handshake. The limit keeps a long body from
// holding the call: a body longer than that closes its connection, which costs less than reading
// it.
func drain(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxErrorBytes))
}

// readLimited reads a whole body, and fails when the body is longer than the limit.
func readLimited(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the body is over the limit of %d bytes", limit)
	}

	return data, nil
}
