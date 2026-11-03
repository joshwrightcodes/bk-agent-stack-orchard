// SPDX-License-Identifier: AGPL-3.0-or-later

// Package retry holds the one retry policy of both API clients.
//
// The target design (section 15) says that the Orchard client obeys the same retry rules as the
// Buildkite client. One policy in one package keeps that true. A call that fails in a way that
// another try can pass waits, and then tries again:
//
//   - The wait is a random share of a ceiling, and the ceiling doubles after each try (full
//     jitter). Callers that failed together then try again at different moments.
//   - A server that names a wait in Retry-After gets that wait, in place of the backoff.
//   - A wait longer than the longest wait of a call ends the call. The error keeps the wait, and
//     the caller waits instead: the loop tries again at a later tick, and a source of the loop
//     reads Pause to know when.
//   - The context ends a wait, so the deadline of a call bounds each try and each wait of it.
//
// The control loop runs on one goroutine, and a call that waits holds the loop. The policy
// therefore makes few tries with short waits: the next tick is the real retry.
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Policy says how a client tries a call again. A zero field takes the value of Default.
type Policy struct {
	// Attempts is the most tries of one call, the first one included.
	Attempts int

	// First is the ceiling of the wait after the first try. The ceiling doubles after each try.
	First time.Duration

	// Longest is the longest wait inside one call. It caps the ceiling of the backoff. A server
	// that names a longer wait ends the call, and the caller waits.
	Longest time.Duration

	// Random gives a number from 0 up to, but not including, 1. A nil value takes it from
	// math/rand/v2. A test gives a fixed one.
	Random func() float64

	// Sleep waits for a time, and ends early with the error of the context when the context ends.
	// A nil value uses a timer. A test gives one that records the wait and does not sleep.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Default is the policy of both clients.
//
// Three tries with waits under half a second and then under one second add at most 1.5 s of
// waits to a call. A failure that lasts longer than that is an outage, and the next tick tries
// again. A server can name a wait of up to 2 s that the call still waits for, because that is
// short beside the deadline of a call.
var Default = Policy{Attempts: 3, First: 500 * time.Millisecond, Longest: 2 * time.Second}

// MaxPause is the longest that a source of the loop waits for a time that a server named.
//
// A wrong or a hostile value must not stop a source for long. After the pause the source reads
// again, and a server that still limits it names a new wait, so a short cap costs one request.
// The cap is half of the default stateFreshness (60 s): a source that waits once and then answers
// stays fresh, so admission and the reaper keep their data.
const MaxPause = 30 * time.Second

// Do runs the call, and runs it again while its error is one that Again marked, the attempts
// remain, the wait fits inside the longest wait, and the context lasts. It gives the last error.
// An error that a wait cut short carries the error of the context too.
func (p Policy) Do(ctx context.Context, call func(context.Context) error) error {
	p = p.complete()

	for attempt := 1; ; attempt++ {
		err := call(ctx)
		if err == nil || !Retryable(err) {
			return err
		}

		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}

		if attempt >= p.Attempts {
			return err
		}

		wait, fits := p.wait(attempt, err)
		if !fits {
			return err
		}

		if slept := p.Sleep(ctx, wait); slept != nil {
			return errors.Join(err, slept)
		}
	}
}

// wait gives the wait after a try that failed, and reports whether the call may wait that long.
func (p Policy) wait(attempt int, err error) (time.Duration, bool) {
	if after, asked := After(err); asked {
		return after, after <= p.Longest
	}

	return p.jitter(attempt), true
}

// Backoff gives the wait after the try number attempt failed with err, for a caller that tries
// again itself and has no deadline for all of its tries. The wait is the one that the server
// named, up to Longest, or else the backoff of Do: a random share of a ceiling that starts at
// First and doubles after each try, up to Longest.
func (p Policy) Backoff(attempt int, err error) time.Duration {
	p = p.complete()

	if after, asked := After(err); asked {
		return min(after, p.Longest)
	}

	return p.jitter(attempt)
}

// jitter gives a random share of the ceiling of the backoff after the try number attempt (full
// jitter).
func (p Policy) jitter(attempt int) time.Duration {
	ceiling := p.Longest
	if attempt < 32 {
		if doubled := p.First << (attempt - 1); doubled > 0 && doubled < ceiling {
			ceiling = doubled
		}
	}

	return time.Duration(p.Random() * float64(ceiling))
}

// complete gives the policy with the value of Default in each zero field.
func (p Policy) complete() Policy {
	if p.Attempts <= 0 {
		p.Attempts = Default.Attempts
	}

	if p.First <= 0 {
		p.First = Default.First
	}

	if p.Longest <= 0 {
		p.Longest = Default.Longest
	}

	if p.Random == nil {
		// G404: the jitter spreads the tries of callers in time, and needs no secret randomness.
		p.Random = rand.Float64 //nolint:gosec
	}

	if p.Sleep == nil {
		p.Sleep = Sleep
	}

	return p
}

// Sleep waits for a time, or until the context ends. It gives the error of the context when the
// context ends first.
func Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// again marks an error that another try can pass.
type again struct {
	err   error
	after time.Duration
}

func (a *again) Error() string { return a.err.Error() }
func (a *again) Unwrap() error { return a.err }

// Again marks err as a failure that another try of the same call can pass, such as a failure in
// transport, a 5xx status, or 429. After is the wait that the server named, and zero when it named
// none. The error keeps its message and its chain.
func Again(err error, after time.Duration) error {
	return &again{err: err, after: after}
}

// Retryable reports an error that Again marked.
func Retryable(err error) bool {
	var marked *again

	return errors.As(err, &marked)
}

// Unavailable reports an error that says that the API does not answer now: a failure that another
// try could pass (a failure in transport, a 5xx status, or 429), or a call that ran out of time or
// was cancelled. A step that makes one call for each machine stops at such an error, because the
// calls after it would wait for the same API. A refusal of one call is an answer, and the calls
// after it may pass.
func Unavailable(err error) bool {
	return Retryable(err) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)
}

// After gives the wait that the server named in an error that Again marked, and reports whether
// it named one.
func After(err error) (time.Duration, bool) {
	var marked *again
	if !errors.As(err, &marked) || marked.after <= 0 {
		return 0, false
	}

	return marked.after, true
}

// Pause gives how long a source of the loop waits before it reads again after the error: the
// wait that the server named, up to MaxPause, and zero when the server named none.
func Pause(err error) time.Duration {
	after, asked := After(err)
	if !asked {
		return 0
	}

	return min(after, MaxPause)
}

// ParseAfter reads the value of a Retry-After header, and gives the wait that it names. Now is the
// time on the clock of the controller.
//
// The value is a whole number of seconds, or a date (RFC 9110, section 10.2.3). A date names the
// wait from now to that date, and a recipient must read each of the three forms of a date that
// section 5.6.7 lists. A date that has passed, and a value of neither form, give zero, and the
// backoff applies.
//
// A date compares the clock of the server with the clock of the controller, so a difference
// between the two clocks changes the wait. MaxPause limits the effect of a wrong clock, as it
// limits the effect of a wrong number.
func ParseAfter(header string, now time.Time) time.Duration {
	// 32 bits of seconds is more than a century, which still fits a time.Duration.
	if seconds, err := strconv.ParseUint(header, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}

	date, err := http.ParseTime(header)
	if err != nil || !date.After(now) {
		return 0
	}

	return date.Sub(now)
}
