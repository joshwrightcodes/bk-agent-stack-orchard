// Package health answers the liveness and the readiness probes of the platform that runs the
// controller, through the gRPC health checking protocol (grpc.health.v1).
//
// Liveness says that the process serves. A failed liveness probe restarts the process, which
// cures nothing that a slow API causes, so liveness does not depend on either API. Readiness says
// that the controller acts on the world as it is: every source answered inside the readiness
// window, and Buildkite accepts the agent token. A controller that is not ready still runs its
// loop.
//
// The protocol carries a status and no reason, so each change of readiness is also a log record
// that names every reason. An operator reads why there.
package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// Window is how old the last answer of a source may be while the controller is ready (target
// design, section 11).
const Window = 30 * time.Second

// The services of the health server. A probe names one of them. The empty name is the health of
// the whole server in the protocol, and it follows readiness.
const (
	ServiceLiveness  = "liveness"
	ServiceReadiness = "readiness"
)

// Source is one supply of observed state, and the time of its last answer.
type Source interface {
	Source() telemetry.Source
	At() time.Time
}

// Options configures the checks.
type Options struct {
	// Sources are the supplies of the loop. The controller is ready only while each of them
	// answered inside the window.
	Sources []Source

	// Unauthorized reports an agent token that Buildkite refused.
	Unauthorized func() bool

	// Now is the clock of the controller.
	Now func() time.Time

	// Logger takes a record at each change of readiness.
	Logger *slog.Logger
}

// Checks keeps the health server current.
type Checks struct {
	sources      []Source
	unauthorized func() bool
	now          func() time.Time
	logger       *slog.Logger
	server       *grpchealth.Server

	// kinds are the kinds of reason of the last update. A record follows a change of kind and not
	// of text, because the age in a reason changes at each update. Only the goroutine that updates
	// the checks reads and writes them.
	kinds   []string
	updated bool
}

// New checks the options and gives the checks. The process is alive from here, and it is not
// ready until the first update finds every source fresh.
func New(options Options) (*Checks, error) {
	var problems []error

	if len(options.Sources) == 0 {
		problems = append(problems, errors.New("the readiness check needs the sources"))
	}

	if options.Unauthorized == nil {
		problems = append(problems, errors.New("the readiness check needs the state of the token"))
	}

	if options.Now == nil {
		problems = append(problems, errors.New("the readiness check needs a clock"))
	}

	if options.Logger == nil {
		problems = append(problems, errors.New("the readiness check needs a logger"))
	}

	if err := errors.Join(problems...); err != nil {
		return nil, err
	}

	server := grpchealth.NewServer()
	server.SetServingStatus(ServiceLiveness, healthpb.HealthCheckResponse_SERVING)
	server.SetServingStatus(ServiceReadiness, healthpb.HealthCheckResponse_NOT_SERVING)
	server.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)

	return &Checks{
		sources:      options.Sources,
		unauthorized: options.Unauthorized,
		now:          options.Now,
		logger:       options.Logger,
		server:       server,
	}, nil
}

// Server gives the health service, which the gRPC server of the probes registers.
func (c *Checks) Server() *grpchealth.Server {
	return c.server
}

// Run updates the checks at each interval until the context ends, and then marks every service
// as not serving, so that no probe passes while the process stops.
func (c *Checks) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	c.Update(ctx)

	for {
		select {
		case <-ctx.Done():
			c.server.Shutdown()

			return
		case <-ticker.C:
			c.Update(ctx)
		}
	}
}

// Update decides readiness now, gives it to the health service, and writes a record when it
// changed.
func (c *Checks) Update(ctx context.Context) {
	found := c.problems()

	status := healthpb.HealthCheckResponse_SERVING
	if len(found) > 0 {
		status = healthpb.HealthCheckResponse_NOT_SERVING
	}

	c.server.SetServingStatus(ServiceReadiness, status)
	c.server.SetServingStatus("", status)

	kinds := make([]string, 0, len(found))
	reasons := make([]string, 0, len(found))

	for _, p := range found {
		kinds = append(kinds, p.kind)
		reasons = append(reasons, p.reason)
	}

	if c.updated && slices.Equal(kinds, c.kinds) {
		return
	}

	c.updated, c.kinds = true, kinds

	if len(found) == 0 {
		c.logger.InfoContext(ctx, "the controller is ready")

		return
	}

	c.logger.WarnContext(ctx, "the controller is not ready", "reasons", reasons)
}

// Unready gives each reason that the controller is not ready, and nothing when it is.
func (c *Checks) Unready() []string {
	var reasons []string

	for _, p := range c.problems() {
		reasons = append(reasons, p.reason)
	}

	return reasons
}

// problem is one reason that the controller is not ready. The kind stays the same while the
// reason stays, and the text says it to a person.
type problem struct {
	kind   string
	reason string
}

func (c *Checks) problems() []problem {
	var found []problem

	if c.unauthorized() {
		found = append(found, problem{"token", "buildkite refused the agent token: replace " +
			"the token and restart the controller"})
	}

	now := c.now()

	for _, source := range c.sources {
		at := source.At()

		switch {
		case at.IsZero():
			found = append(found, problem{
				"unanswered " + string(source.Source()),
				fmt.Sprintf("the source %s has not answered yet", source.Source()),
			})
		case now.Sub(at) > Window:
			found = append(found, problem{
				"old " + string(source.Source()),
				fmt.Sprintf("the last answer of the source %s is %s old, and readiness needs "+
					"one inside %s", source.Source(), now.Sub(at).Round(time.Second), Window),
			})
		}
	}

	return found
}
