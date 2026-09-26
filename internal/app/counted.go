package app

import (
	"context"
	"errors"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// The values of error.type for a call that failed. The conventions give _OTHER for a failure that
// has no name of its own.
const (
	errorUnauthorized = "unauthorized"
	errorRateLimited  = "rate_limited"
	errorTimeout      = "timeout"
	errorOther        = "_OTHER"
)

// cause gives the error.type of a call that failed, and reports whether the failure is one of the
// system. An answer about one job, such as a job that ended or a VM that exists, is what the API
// knows and not a failure of it, and a call that the shutdown cancelled failed nowhere.
func cause(err error) (string, bool) {
	switch {
	case err == nil,
		errors.Is(err, context.Canceled),
		errors.Is(err, buildkite.ErrTerminal),
		errors.Is(err, orchard.ErrAlreadyExists),
		errors.Is(err, orchard.ErrNotFound):
		return "", false
	case errors.Is(err, buildkite.ErrUnauthorized):
		return errorUnauthorized, true
	case errors.Is(err, buildkite.ErrRateLimited):
		return errorRateLimited, true
	case errors.Is(err, context.DeadlineExceeded):
		return errorTimeout, true
	}

	return errorOther, true
}

// counter records the failures of the calls to one system in cicd.system.errors.
type counter struct {
	metrics   *telemetry.Metrics
	component telemetry.Component
}

func (c counter) count(ctx context.Context, err error) {
	if name, failed := cause(err); failed {
		c.metrics.SystemError(ctx, c.component, name)
	}
}

// countedStacks is the Stacks API that counts each call that fails. The wrapper is the one place
// that sees every call, so no call site can forget to count.
type countedStacks struct {
	next buildkite.Stacks
	counter
}

func (s countedStacks) Register(ctx context.Context) error {
	err := s.next.Register(ctx)
	s.count(ctx, err)

	return err
}

func (s countedStacks) Deregister(ctx context.Context) error {
	err := s.next.Deregister(ctx)
	s.count(ctx, err)

	return err
}

func (s countedStacks) ScheduledJobs(ctx context.Context, cursor string, size int) (
	buildkite.Page, error,
) {
	page, err := s.next.ScheduledJobs(ctx, cursor, size)
	s.count(ctx, err)

	return page, err
}

func (s countedStacks) Reserve(ctx context.Context, ids []job.ID, expiry time.Duration) (
	buildkite.Reservation, error,
) {
	answer, err := s.next.Reserve(ctx, ids, expiry)
	s.count(ctx, err)

	return answer, err
}

func (s countedStacks) Tokens(ctx context.Context, ids []job.ID, lifetime time.Duration) (
	buildkite.Tokens, error,
) {
	answer, err := s.next.Tokens(ctx, ids, lifetime)
	s.count(ctx, err)

	return answer, err
}

func (s countedStacks) States(ctx context.Context, ids []job.ID) (map[job.ID]string, error) {
	states, err := s.next.States(ctx, ids)
	s.count(ctx, err)

	return states, err
}

func (s countedStacks) Finish(ctx context.Context, id job.ID, exitStatus int, detail string) error {
	err := s.next.Finish(ctx, id, exitStatus, detail)
	s.count(ctx, err)

	return err
}

func (s countedStacks) Notify(ctx context.Context, notifications []buildkite.Notification) error {
	err := s.next.Notify(ctx, notifications)
	s.count(ctx, err)

	return err
}

// countedOrchard is the Orchard API that counts each call that fails.
type countedOrchard struct {
	next orchard.API
	counter
}

func (o countedOrchard) CreateVM(ctx context.Context, spec orchard.VM) error {
	err := o.next.CreateVM(ctx, spec)
	o.count(ctx, err)

	return err
}

func (o countedOrchard) ListVMs(ctx context.Context) ([]orchard.VM, error) {
	vms, err := o.next.ListVMs(ctx)
	o.count(ctx, err)

	return vms, err
}

func (o countedOrchard) DeleteVM(ctx context.Context, name string) error {
	err := o.next.DeleteVM(ctx, name)
	o.count(ctx, err)

	return err
}

func (o countedOrchard) ListWorkers(ctx context.Context) ([]orchard.Worker, error) {
	workers, err := o.next.ListWorkers(ctx)
	o.count(ctx, err)

	return workers, err
}

func (o countedOrchard) Events(ctx context.Context, name string) ([]orchard.Event, error) {
	events, err := o.next.Events(ctx, name)
	o.count(ctx, err)

	return events, err
}
