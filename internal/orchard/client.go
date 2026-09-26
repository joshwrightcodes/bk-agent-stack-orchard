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
	"net/http"
	"net/url"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
)

// Errors that a caller decides about.
var (
	// ErrAlreadyExists is a machine whose name Orchard already has. A second create of the same
	// name means that the first one arrived, which the launcher treats as a success.
	ErrAlreadyExists = errors.New("a virtual machine of this name exists")

	// ErrNotFound is a machine or a worker that Orchard does not have.
	ErrNotFound = errors.New("orchard does not have this resource")
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

	// Attempts and RetryWait control what the client does with an error that can pass.
	Attempts  int
	RetryWait time.Duration
}

// Client calls the Orchard API for one controller.
type Client struct {
	url            *url.URL
	serviceAccount string
	token          secret.String
	http           *http.Client
	attempts       int
	retryWait      time.Duration
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
		attempts:       options.Attempts,
		retryWait:      options.RetryWait,
	}

	if client.http == nil {
		client.http = &http.Client{Timeout: 30 * time.Second}
	}
	if client.attempts <= 0 {
		client.attempts = 3
	}
	if client.retryWait <= 0 {
		client.retryWait = time.Second
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

// do sends one request, and sends it again while the error can pass and the attempts remain.
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var last error

	for attempt := 1; attempt <= c.attempts; attempt++ {
		err := c.attempt(ctx, method, path, body, out)
		if err == nil {
			return nil
		}

		last = err
		if !retryable(err) {
			return err
		}

		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), last)
		case <-time.After(c.retryWait):
		}
	}

	return fmt.Errorf("orchard %s %s after %d attempts: %w", method, path, c.attempts, last)
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
		return retry{err}
	}
	defer func() { _ = response.Body.Close() }()

	if err := statusError(response); err != nil {
		return err
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
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
func statusError(response *http.Response) error {
	switch {
	case response.StatusCode < http.StatusBadRequest:
		return nil
	case response.StatusCode == http.StatusConflict:
		return ErrAlreadyExists
	case response.StatusCode == http.StatusNotFound:
		return ErrNotFound
	}

	message := apiMessage(response)
	err := fmt.Errorf("orchard answered %s: %s", response.Status, message)

	// A server error, or a limit, can pass. A refusal of the request does not.
	if response.StatusCode >= http.StatusInternalServerError ||
		response.StatusCode == http.StatusTooManyRequests {
		return retry{err}
	}

	return err
}

// apiMessage reads the message that Orchard writes with an error.
func apiMessage(response *http.Response) string {
	var body struct {
		Message string `json:"message"`
	}

	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body.Message == "" {
		return "no message"
	}

	return body.Message
}

// retry marks an error that another attempt can pass.
type retry struct{ err error }

func (r retry) Error() string { return r.err.Error() }
func (r retry) Unwrap() error { return r.err }

func retryable(err error) bool {
	var marker retry

	return errors.As(err, &marker)
}
