// SPDX-License-Identifier: AGPL-3.0-or-later

// Package buildkite talks to the Buildkite Stacks API.
//
// The package holds a narrow interface over github.com/buildkite/stacksapi, so that the
// components of the controller depend on the calls that they make and not on the whole library,
// and so that a test of a component uses an in-memory fake of a few methods.
//
// The library tries each request once. The client tries a request again by the retry policy that
// it shares with the Orchard client (internal/retry): after a failure in transport, a 5xx status,
// or 429, with a short wait, and with the wait that Buildkite names in Retry-After when that wait
// is short. A longer wait ends the call, and the error keeps it for the caller. The library alone
// would try five times and sleep through any Retry-After, which holds the one goroutine of the
// control loop for minutes. The client decides what a status means to this controller:
//
//   - 401 means that Buildkite did not accept the agent token. The readiness check of the
//     controller fails until an operator supplies another token and restarts it.
//   - 404 and 422 on a call about one job mean that the job is terminal. The caller marks the job
//     as ignored and sends no second call.
//   - 429 after the tries of the policy means that the controller must wait.
//
// The types of this package carry job.ID and secret.String, so that a job acquisition token
// cannot reach a log, and so that the compiler sees the difference between a job and a name.
package buildkite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/buildkite/stacksapi"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
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

	// ErrRateLimited is a rate limit that outlived the tries of the retry policy.
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

	// Retry says how the client tries a call again. The zero value is retry.Default, the policy
	// of the Orchard client too.
	Retry retry.Policy

	// Now is the clock that reads a Retry-After date. A nil value gives the clock of the machine.
	Now func() time.Time
}

// Client calls the Stacks API for one stack and one queue.
type Client struct {
	api      *stacksapi.Client
	retry    retry.Policy
	stackKey string
	queue    string
	now      func() time.Time

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

	opts = append(opts, stacksapi.WithHTTPClient(limitBodies(options.HTTPClient)))

	api, err := stacksapi.NewClient(options.Token.Reveal(), opts...)
	if err != nil {
		return nil, fmt.Errorf("building the Buildkite client: %w", err)
	}

	now := options.Now
	if now == nil {
		now = time.Now //nolint:forbidigo // the default of a nil clock
	}

	return &Client{
		api: api, retry: options.Retry, stackKey: options.StackKey, queue: options.Queue, now: now,
	}, nil
}

// try runs one request of the library by the retry policy of the client. The request gets
// stacksapi.WithNoRetry, so that the policy alone decides the tries and the waits.
func (c *Client) try(ctx context.Context, request func(context.Context) error) error {
	return c.retry.Do(ctx, func(ctx context.Context) error {
		return c.again(request(ctx))
	})
}

// again marks an answer of the library that another try can pass: a failure in transport, or a
// status that the library itself would retry (a 5xx status or 429), with the wait that Buildkite
// named. A refusal, and an answer that does not decode, cannot pass.
func (c *Client) again(err error) error {
	var answer *stacksapi.ErrorResponse
	if errors.As(err, &answer) {
		if answer.Response == nil || !answer.IsRetryableStatus() {
			return err
		}

		return retry.Again(err, retry.ParseAfter(answer.Response.Header.Get("Retry-After"), c.now()))
	}

	var transport *url.Error
	if errors.As(err, &transport) {
		return retry.Again(err, 0)
	}

	return err
}

// Unauthorized reports an agent token that Buildkite refused. The readiness check of the
// controller fails while it holds, because every call needs that token and only an operator with
// a new token and a restart can clear it.
func (c *Client) Unauthorized() bool {
	return c.unauthorized.Load()
}

// Register announces the stack to Buildkite.
func (c *Client) Register(ctx context.Context) error {
	err := c.try(ctx, func(ctx context.Context) error {
		_, _, err := c.api.RegisterStack(ctx, stacksapi.RegisterStackRequest{
			Key:      c.stackKey,
			Type:     stacksapi.StackTypeCustom,
			QueueKey: c.queue,
			Metadata: map[string]string{},
		}, stacksapi.WithNoRetry())

		return err
	})

	return c.classify(err)
}

// Deregister announces that the stack stops.
func (c *Client) Deregister(ctx context.Context) error {
	err := c.try(ctx, func(ctx context.Context) error {
		_, err := c.api.DeregisterStack(ctx, c.stackKey, stacksapi.WithNoRetry())

		return err
	})

	return c.classify(err)
}

// ScheduledJobs gives one page of the jobs that wait in the queue.
func (c *Client) ScheduledJobs(ctx context.Context, cursor string, size int) (Page, error) {
	var answer *stacksapi.ListScheduledJobsResponse

	err := c.try(ctx, func(ctx context.Context) error {
		var err error

		answer, _, err = c.api.ListScheduledJobs(ctx, stacksapi.ListScheduledJobsRequest{
			StackKey:        c.stackKey,
			ClusterQueueKey: c.queue,
			PageSize:        size,
			StartCursor:     cursor,
		}, stacksapi.WithNoRetry())

		return err
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

	var answer *stacksapi.BatchReserveJobsResponse

	err := c.try(ctx, func(ctx context.Context) error {
		var err error

		answer, _, err = c.api.BatchReserveJobs(ctx, stacksapi.BatchReserveJobsRequest{
			StackKey:                 c.stackKey,
			JobUUIDs:                 texts(ids),
			ReservationExpirySeconds: int(expiry.Seconds()),
		}, stacksapi.WithNoRetry())

		return err
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

	var answer *stacksapi.IssueJobAcquisitionTokensResponse

	err := c.try(ctx, func(ctx context.Context) error {
		var err error

		answer, _, err = c.api.IssueJobAcquisitionTokens(ctx,
			stacksapi.IssueJobAcquisitionTokensRequest{
				StackKey:             c.stackKey,
				JobUUIDs:             texts(ids),
				TokenLifetimeSeconds: int(lifetime.Seconds()),
			}, stacksapi.WithNoRetry())

		return err
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
//
// The call asks about many jobs at once, so a 404 or a 422 on it is a failure of the call, such as
// a stack that someone removed, and not an answer about one job. It is not ErrTerminal, and the
// error metric counts it.
func (c *Client) States(ctx context.Context, ids []job.ID) (map[job.ID]string, error) {
	if len(ids) == 0 {
		return map[job.ID]string{}, nil
	}

	var answer *stacksapi.GetJobStatesResponse

	err := c.try(ctx, func(ctx context.Context) error {
		var err error

		answer, _, err = c.api.GetJobStates(ctx, stacksapi.GetJobStatesRequest{
			StackKey: c.stackKey,
			JobUUIDs: texts(ids),
		}, stacksapi.WithNoRetry())

		return err
	})
	if err != nil {
		return nil, c.classify(err)
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
	err := c.try(ctx, func(ctx context.Context) error {
		_, err := c.api.FinishJob(ctx, stacksapi.FinishJobRequest{
			StackKey:   c.stackKey,
			JobUUID:    string(id),
			ExitStatus: exitStatus,
			Detail:     detail,
		}, stacksapi.WithNoRetry())

		return err
	})

	return c.classifyJob(err)
}

// NotificationsPerRequest is the most lines that one request can carry (Stacks API documentation,
// https://buildkite.com/docs/apis/agent-api/stacks, read on 2026-11-01).
const NotificationsPerRequest = 1000

// RefusedError is the answer to a request in which Buildkite refused some lines and created the
// others. Indexes are the 0-based places of the refused lines in the request.
type RefusedError struct {
	Indexes []int
	Total   int
	Reason  string
}

// Error gives the count and the reason of the first refusal.
func (e *RefusedError) Error() string {
	return fmt.Sprintf("%s: %d of %d lines: %s", ErrRefused, len(e.Indexes), e.Total, e.Reason)
}

// Is makes a RefusedError match ErrRefused.
func (e *RefusedError) Is(target error) bool { return target == ErrRefused }

// NotificationLimit is the longest detail of a notification, in characters, that Buildkite
// accepts (Stacks API documentation, read on 2026-11-01). The library does not cut.
const NotificationLimit = 256

// cut shortens text to at most limit characters, and ends a cut text with an ellipsis that says
// so. It cuts between characters, never inside the bytes of one. It gives a word for empty text,
// because the API refuses an empty detail.
//
// A line on a job page is one line of plain text, and a detail can quote text that a guest wrote.
// Each control character, such as a line break or an escape sequence, becomes a space. So does each
// format character (Unicode category Cf, which holds the bidirectional overrides and the
// zero-width characters) and each line or paragraph separator. The count of characters is the count
// after that. A detail with nothing to read, only blanks, gets the same word as an empty one.
//
// The client replaces these characters with a space, where the finish message of the reaper drops
// them. A notification is one line, and a space keeps its words apart. A finish message holds the
// lines of a script, and a dropped character leaves the word that it sat in whole.
func cut(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}

		return r
	}, text)

	if strings.TrimSpace(text) == "" {
		// The API refuses an empty detail, and a blank one shows as a blank line.
		return "(no detail)"
	}

	if utf8.RuneCountInString(text) <= limit {
		return text
	}

	return string([]rune(text)[:limit-1]) + "…"
}

// Notify writes lines on the job pages. A detail over NotificationLimit characters is cut.
func (c *Client) Notify(ctx context.Context, notifications []Notification) error {
	if len(notifications) == 0 {
		return nil
	}

	if len(notifications) > NotificationsPerRequest {
		return fmt.Errorf("a request takes at most %d notifications, and this one has %d",
			NotificationsPerRequest, len(notifications))
	}

	lines := make([]stacksapi.StackNotification, 0, len(notifications))
	for _, notification := range notifications {
		lines = append(lines, stacksapi.StackNotification{
			JobUUID:   string(notification.JobID),
			Detail:    cut(notification.Detail, NotificationLimit),
			Timestamp: notification.At,
		})
	}

	var answer *stacksapi.CreateStackNotificationsResponse

	err := c.try(ctx, func(ctx context.Context) error {
		var err error

		answer, _, err = c.api.CreateStackNotifications(ctx,
			stacksapi.CreateStackNotificationsRequest{
				StackKey:      c.stackKey,
				Notifications: lines,
			}, stacksapi.WithNoRetry())

		return err
	})
	if err != nil {
		return c.classify(err)
	}

	// A notification is a line on a job page. A refused line loses information, and it must not
	// stop the launch of a VM, so the caller logs the answer and continues.
	if len(answer.Errors) > 0 {
		refused := &RefusedError{Total: len(lines), Reason: answer.Errors[0].Error}

		for _, refusal := range answer.Errors {
			refused.Indexes = append(refused.Indexes, refusal.Indexes...)
		}

		return refused
	}

	return nil
}

// classify turns the answer of the library into the errors that a caller decides about. The wait
// that Buildkite named travels with the error, so that a source of the loop waits for it before
// it reads again.
func (c *Client) classify(err error) error {
	if err == nil {
		return nil
	}

	// A status that keeps the answer in its chain keeps the mark too. One that gives an error
	// of its own, such as ErrRateLimited, gets the mark again.
	classified := c.status(err)
	if after, asked := retry.After(err); asked && !retry.Retryable(classified) {
		return retry.Again(classified, after)
	}

	return classified
}

// status gives the error that a status of the API means to a caller.
func (c *Client) status(err error) error {
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

// maxResponseBytes is the most that the client reads from the body of an answer. The library reads
// a whole body into memory, so a body over the limit fails the call, and a cut body never reaches
// the decoder.
const maxResponseBytes = 16 << 20 // 16 MiB

// limitBodies gives a copy of the client whose transport limits each answer body. A nil client
// stands for the default client of the library.
func limitBodies(client *http.Client) *http.Client {
	limited := &http.Client{}
	if client != nil {
		copied := *client
		limited = &copied
	}

	limited.Transport = limitedTransport{next: limited.Transport}

	return limited
}

// limitedTransport replaces the body of each answer with a reader that fails past the limit.
type limitedTransport struct{ next http.RoundTripper }

func (t limitedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}

	response, err := next.RoundTrip(request)
	if err != nil {
		return response, err
	}

	response.Body = &limitedBody{body: response.Body, left: maxResponseBytes}

	return response, nil
}

// limitedBody reads up to the limit, and then fails if the body has more.
type limitedBody struct {
	body io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, errBodyOverLimit
	}

	// One byte more than the limit shows that the body is longer.
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}

	n, err := b.body.Read(p)
	b.left -= int64(n)

	if b.left < 0 {
		return n - 1, errBodyOverLimit
	}

	return n, err
}

func (b *limitedBody) Close() error { return b.body.Close() }

var errBodyOverLimit = fmt.Errorf("the body of the answer is over the limit of %d bytes", maxResponseBytes)
