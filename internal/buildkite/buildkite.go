// Package buildkite talks to the Buildkite Stacks API.
//
// The package holds a narrow interface over github.com/buildkite/stacksapi, so that the
// components of the controller depend on the calls that they make and not on the whole library,
// and so that a test of a component uses an in-memory fake of a few methods.
//
// The library retries a request that failed in transport, and a request that the API answered
// with a 5xx status or with 429. It obeys Retry-After. This package therefore adds no retry of
// its own. It decides what a status means to this controller:
//
//   - 401 means that Buildkite did not accept the agent token. The readiness check of the
//     controller fails until an operator supplies another token and restarts it.
//   - 404 and 422 on a call about one job mean that the job is terminal. The caller marks the job
//     as ignored and sends no second call.
//   - 429 after the retries of the library means that the controller must wait.
//
// The types of this package carry job.ID and secret.String, so that a job acquisition token
// cannot reach a log, and so that the compiler sees the difference between a job and a name.
package buildkite

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/buildkite/stacksapi"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
)

// Errors that a caller decides about.
var (
	// ErrUnauthorized is an agent token that Buildkite did not accept. The controller cannot
	// recover from it, because a token comes from the environment at start-up.
	ErrUnauthorized = errors.New("buildkite did not accept the agent token")

	// ErrTerminal is a job that Buildkite will not accept this call for again, because the job
	// ended, another stack owns it, or it never existed. A second call gives the same answer, so
	// the caller marks the job as ignored and stops.
	ErrTerminal = errors.New("buildkite will not accept this call for this job again")

	// ErrRateLimited is a rate limit that outlived the retries of the library.
	ErrRateLimited = errors.New("buildkite applied a rate limit")

	// ErrRefused is a line that Buildkite did not put on a job page, such as a line for a job
	// that ended. It is an answer about a job, and not a failure of the API.
	ErrRefused = errors.New("buildkite refused a line for a job page")
)

// Stacks is what the rest of the controller sees of Buildkite. Each component depends on this
// interface, so that a test of a component needs no HTTP server.
type Stacks interface {
	// Register announces the stack. The call is idempotent, so a restart repeats it.
	Register(ctx context.Context) error

	// Deregister announces that the stack stops.
	Deregister(ctx context.Context) error

	// ScheduledJobs gives one page of the jobs that wait in the queue. An empty cursor asks for
	// the first page.
	ScheduledJobs(ctx context.Context, cursor string, size int) (Page, error)

	// Reserve holds jobs for this stack until the expiry. A second call for a held job extends
	// it. The answer names the jobs that the call did not hold, which another stack may own.
	Reserve(ctx context.Context, ids []job.ID, expiry time.Duration) (Reservation, error)

	// Tokens gives one job acquisition token for each reserved job. The answer names the jobs
	// that got none, whose reservations then expire.
	Tokens(ctx context.Context, ids []job.ID, lifetime time.Duration) (Tokens, error)

	// States gives the Buildkite state of each job that Buildkite knows.
	States(ctx context.Context, ids []job.ID) (map[job.ID]string, error)

	// Finish ends a job that no agent acquired, with a reason for the job page.
	Finish(ctx context.Context, id job.ID, exitStatus int, detail string) error

	// Notify writes lines on the job pages, which is how an engineer sees the progress of a VM.
	Notify(ctx context.Context, notifications []Notification) error
}

// Options configures the client.
type Options struct {
	// Token is the cluster agent token (bkct_). The Stacks API accepts no other credential.
	Token secret.String

	// Endpoint is the base URL of the API.
	Endpoint string

	// StackKey identifies this controller to Buildkite. It is the controller identifier.
	StackKey string

	// Queue is the cluster queue that this stack serves.
	Queue string

	// Logger takes the request log of the library. A nil logger discards it.
	Logger *slog.Logger

	// HTTPClient is for a test. A nil client gives the default of the library.
	HTTPClient *http.Client
}

// Client calls the Stacks API for one stack and one queue.
type Client struct {
	api      *stacksapi.Client
	stackKey string
	queue    string

	// unauthorized stays set after a 401, because the token comes from the environment at
	// start-up and no call can replace it. The readiness check reads it.
	unauthorized atomic.Bool
}

// Client implements the narrow interface.
var _ Stacks = (*Client)(nil)

// New builds a client. It fails when a setting is missing, because a controller that starts with
// an unusable client only finds out at its first tick.
func New(options Options) (*Client, error) {
	var problems []error

	if options.Token.Empty() {
		problems = append(problems, errors.New("the Buildkite agent token is empty"))
	}

	if options.StackKey == "" {
		problems = append(problems, errors.New("the stack key is empty"))
	}

	if options.Queue == "" {
		problems = append(problems, errors.New("the queue is empty"))
	}

	endpoint, err := url.ParseRequestURI(options.Endpoint)
	if err != nil {
		problems = append(problems, fmt.Errorf("the Buildkite endpoint %q is not a URL: %w",
			options.Endpoint, err))
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	opts := []stacksapi.ClientOpt{stacksapi.WithBaseURL(endpoint)}
	if options.Logger != nil {
		opts = append(opts, stacksapi.WithLogger(options.Logger))
	}

	if options.HTTPClient != nil {
		opts = append(opts, stacksapi.WithHTTPClient(options.HTTPClient))
	}

	api, err := stacksapi.NewClient(options.Token.Reveal(), opts...)
	if err != nil {
		return nil, fmt.Errorf("building the Buildkite client: %w", err)
	}

	return &Client{api: api, stackKey: options.StackKey, queue: options.Queue}, nil
}

// Unauthorized reports an agent token that Buildkite refused. The readiness check of the
// controller fails while it holds, because every call needs that token and only an operator with
// a new token and a restart can clear it.
func (c *Client) Unauthorized() bool {
	return c.unauthorized.Load()
}

// Register announces the stack to Buildkite.
func (c *Client) Register(ctx context.Context) error {
	_, _, err := c.api.RegisterStack(ctx, stacksapi.RegisterStackRequest{
		Key:      c.stackKey,
		Type:     stacksapi.StackTypeCustom,
		QueueKey: c.queue,
		Metadata: map[string]string{},
	})

	return c.classify(err)
}

// Deregister announces that the stack stops.
func (c *Client) Deregister(ctx context.Context) error {
	_, err := c.api.DeregisterStack(ctx, c.stackKey)

	return c.classify(err)
}

// ScheduledJobs gives one page of the jobs that wait in the queue.
func (c *Client) ScheduledJobs(ctx context.Context, cursor string, size int) (Page, error) {
	answer, _, err := c.api.ListScheduledJobs(ctx, stacksapi.ListScheduledJobsRequest{
		StackKey:        c.stackKey,
		ClusterQueueKey: c.queue,
		PageSize:        size,
		StartCursor:     cursor,
	})
	if err != nil {
		return Page{}, c.classify(err)
	}

	page := Page{
		Paused: answer.ClusterQueue.Paused,
		Jobs:   make([]ScheduledJob, 0, len(answer.Jobs)),
	}

	if answer.PageInfo.HasNextPage {
		page.NextCursor = answer.PageInfo.EndCursor
	}

	for _, scheduled := range answer.Jobs {
		id, err := job.ParseID(scheduled.ID)
		if err != nil {
			return Page{}, fmt.Errorf("the scheduled-jobs page of queue %s: %w", c.queue, err)
		}

		page.Jobs = append(page.Jobs, ScheduledJob{
			ID:          id,
			Priority:    scheduled.Priority,
			Rules:       scheduled.AgentQueryRules,
			RunnableAt:  scheduled.RunnableAt,
			ScheduledAt: scheduled.ScheduledAt,
			Pipeline:    scheduled.Pipeline.Slug,
			Build:       scheduled.Build.Number,
		})
	}

	return page, nil
}

// Reserve holds jobs for this stack until the expiry.
func (c *Client) Reserve(ctx context.Context, ids []job.ID, expiry time.Duration) (Reservation, error) {
	if len(ids) == 0 {
		return Reservation{}, nil
	}

	answer, _, err := c.api.BatchReserveJobs(ctx, stacksapi.BatchReserveJobsRequest{
		StackKey:                 c.stackKey,
		JobUUIDs:                 texts(ids),
		ReservationExpirySeconds: int(expiry.Seconds()),
	})
	if err != nil {
		return Reservation{}, c.classify(err)
	}

	reserved, err := identifiers(answer.Reserved)
	if err != nil {
		return Reservation{}, fmt.Errorf("the reserved jobs: %w", err)
	}

	refused, err := identifiers(answer.NotReserved)
	if err != nil {
		return Reservation{}, fmt.Errorf("the jobs that the call did not reserve: %w", err)
	}

	return Reservation{Reserved: reserved, NotReserved: refused}, nil
}

// Tokens gives one job acquisition token for each reserved job.
func (c *Client) Tokens(ctx context.Context, ids []job.ID, lifetime time.Duration) (Tokens, error) {
	if len(ids) == 0 {
		return Tokens{}, nil
	}

	answer, _, err := c.api.IssueJobAcquisitionTokens(ctx, stacksapi.IssueJobAcquisitionTokensRequest{
		StackKey:             c.stackKey,
		JobUUIDs:             texts(ids),
		TokenLifetimeSeconds: int(lifetime.Seconds()),
	})
	if err != nil {
		return Tokens{}, c.classify(err)
	}

	issued := make([]Token, 0, len(answer.JobAcquisitionTokens))

	for _, token := range answer.JobAcquisitionTokens {
		id, err := job.ParseID(token.JobUUID)
		if err != nil {
			return Tokens{}, fmt.Errorf("the issued tokens: %w", err)
		}

		issued = append(issued, Token{
			JobID:     id,
			Token:     secret.String(token.Token),
			ExpiresAt: token.ExpiresAt,
		})
	}

	refused, err := identifiers(answer.NotIssued)
	if err != nil {
		return Tokens{}, fmt.Errorf("the jobs that got no token: %w", err)
	}

	return Tokens{Issued: issued, NotIssued: refused}, nil
}

// States gives the Buildkite state of each job that Buildkite knows. A job that Buildkite does
// not name is absent from the result.
func (c *Client) States(ctx context.Context, ids []job.ID) (map[job.ID]string, error) {
	if len(ids) == 0 {
		return map[job.ID]string{}, nil
	}

	answer, _, err := c.api.GetJobStates(ctx, stacksapi.GetJobStatesRequest{
		StackKey: c.stackKey,
		JobUUIDs: texts(ids),
	})
	if err != nil {
		return nil, c.classifyJob(err)
	}

	states := make(map[job.ID]string, len(answer.States))

	for text, state := range answer.States {
		id, err := job.ParseID(text)
		if err != nil {
			return nil, fmt.Errorf("the job states: %w", err)
		}

		states[id] = state
	}

	return states, nil
}

// DetailBudget is the longest finish detail, in bytes, that the library sends unchanged.
//
// Buildkite accepts 4 KB. The library cuts a longer detail at a rune boundary and ends it with a
// note that says so, and it keeps the room for that note, so it begins to cut a little below
// 4 KB. It cuts the end of the detail. A caller whose message matters most at its end, such as
// the last lines of a startup script, shortens the message itself to this budget, so that the
// library has nothing to cut.
//
// The value copies a constant that the library does not export. A test sends a detail of exactly
// this length, and one of a byte more, through the library, so a version that moves the limit
// fails that test instead of cutting a message quietly.
const DetailBudget = 4*1024 - len("… (Message was cropped because it exceeded the max size)")

// Finish ends a job that no agent acquired. A job that already ended gives ErrTerminal, which
// the caller treats as complete.
//
// The library cuts a detail over 4 KB, at a rune boundary, and says so at its end.
func (c *Client) Finish(ctx context.Context, id job.ID, exitStatus int, detail string) error {
	_, err := c.api.FinishJob(ctx, stacksapi.FinishJobRequest{
		StackKey:   c.stackKey,
		JobUUID:    string(id),
		ExitStatus: exitStatus,
		Detail:     detail,
	})

	return c.classifyJob(err)
}

// Notify writes lines on the job pages.
func (c *Client) Notify(ctx context.Context, notifications []Notification) error {
	if len(notifications) == 0 {
		return nil
	}

	lines := make([]stacksapi.StackNotification, 0, len(notifications))
	for _, notification := range notifications {
		lines = append(lines, stacksapi.StackNotification{
			JobUUID:   string(notification.JobID),
			Detail:    notification.Detail,
			Timestamp: notification.At,
		})
	}

	answer, _, err := c.api.CreateStackNotifications(ctx, stacksapi.CreateStackNotificationsRequest{
		StackKey:      c.stackKey,
		Notifications: lines,
	})
	if err != nil {
		return c.classify(err)
	}

	// A notification is a line on a job page. A refused line loses information, and it must not
	// stop the launch of a VM, so the caller logs the answer and continues.
	if len(answer.Errors) > 0 {
		return fmt.Errorf("%w: %d of %d lines: %s", ErrRefused,
			len(answer.Errors), len(lines), answer.Errors[0].Error)
	}

	return nil
}

// classify turns the answer of the library into the errors that a caller decides about.
func (c *Client) classify(err error) error {
	if err == nil {
		return nil
	}

	var answer *stacksapi.ErrorResponse
	if !errors.As(err, &answer) || answer.Response == nil {
		return err
	}

	switch answer.Response.StatusCode {
	case http.StatusUnauthorized:
		// The token cannot change while the controller runs, so one 401 ends the readiness of
		// the controller until an operator restarts it with another token.
		c.unauthorized.Store(true)

		return fmt.Errorf("%w: %s", ErrUnauthorized, answer.Message)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrRateLimited, answer.Message)
	default:
		return err
	}
}

// classifyJob is classify for a call about one job, where 404 and 422 mean that the job ended.
// The caller marks the job as ignored and sends no second call.
func (c *Client) classifyJob(err error) error {
	var answer *stacksapi.ErrorResponse
	if errors.As(err, &answer) && answer.Response != nil {
		switch answer.Response.StatusCode {
		case http.StatusNotFound, http.StatusUnprocessableEntity:
			return fmt.Errorf("%w: %s", ErrTerminal, answer.Message)
		}
	}

	return c.classify(err)
}

// texts gives the wire form of identifiers.
func texts(ids []job.ID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}

	return out
}

// identifiers reads the identifiers of an answer. Text that is not an identifier means that the
// contract moved, which the caller must see and not ignore.
func identifiers(texts []string) ([]job.ID, error) {
	out := make([]job.ID, 0, len(texts))

	for _, text := range texts {
		id, err := job.ParseID(text)
		if err != nil {
			return nil, err
		}

		out = append(out, id)
	}

	return out, nil
}
