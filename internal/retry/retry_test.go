// SPDX-License-Identifier: AGPL-3.0-or-later

package retry_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/retry"
)

var errBusy = errors.New("answered 503 Service Unavailable")

// waits gives a policy that records each wait and sleeps for none, with a random number that the
// test fixes.
func waits(policy retry.Policy, random float64) (retry.Policy, *[]time.Duration) {
	var slept []time.Duration

	policy.Random = func() float64 { return random }
	policy.Sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)

		return nil
	}

	return policy, &slept
}

// failing gives a call that fails with the error each time, and counts its tries.
func failing(err error) (func(context.Context) error, *int) {
	tries := 0

	return func(context.Context) error {
		tries++

		return err
	}, &tries
}

// Each wait is a random share of a ceiling, and the ceiling doubles after each try up to the
// longest wait. Callers that failed together then try again at different moments, and not
// together a second time.
func TestTheWaitIsARandomShareOfACeilingThatDoubles(t *testing.T) {
	t.Parallel()

	policy, slept := waits(retry.Policy{
		Attempts: 6, First: 100 * time.Millisecond, Longest: time.Second,
	}, 0.5)
	call, tries := failing(retry.Again(errBusy, 0))

	if err := policy.Do(t.Context(), call); !errors.Is(err, errBusy) {
		t.Fatalf("Do() = %v, want the last error", err)
	}

	want := []time.Duration{
		50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
		400 * time.Millisecond, 500 * time.Millisecond,
	}
	if !slices.Equal(*slept, want) {
		t.Errorf("the policy waited %v, want %v", *slept, want)
	}

	if *tries != 6 {
		t.Errorf("the call ran %d times, want 6", *tries)
	}
}

// The share runs from no wait at all to just under the ceiling, which is what full jitter means.
func TestTheShareCoversTheWholeCeiling(t *testing.T) {
	t.Parallel()

	for random, want := range map[float64]time.Duration{
		0:   0,
		0.5: 50 * time.Millisecond,
		1:   100 * time.Millisecond,
	} {
		policy, slept := waits(retry.Policy{
			Attempts: 2, First: 100 * time.Millisecond, Longest: time.Second,
		}, random)
		call, _ := failing(retry.Again(errBusy, 0))

		_ = policy.Do(t.Context(), call)

		if !slices.Equal(*slept, []time.Duration{want}) {
			t.Errorf("with the random number %v the policy waited %v, want %v", random, *slept, want)
		}
	}
}

// A server that names a wait gets that wait in place of the backoff, while it is short enough to
// wait inside the call.
func TestARetryAfterReplacesTheBackoff(t *testing.T) {
	t.Parallel()

	policy, slept := waits(retry.Policy{
		Attempts: 2, First: 100 * time.Millisecond, Longest: 2 * time.Second,
	}, 0.5)
	call, tries := failing(retry.Again(errBusy, 1500*time.Millisecond))

	_ = policy.Do(t.Context(), call)

	if !slices.Equal(*slept, []time.Duration{1500 * time.Millisecond}) {
		t.Errorf("the policy waited %v, want the 1.5s that the server named", *slept)
	}

	if *tries != 2 {
		t.Errorf("the call ran %d times, want 2", *tries)
	}
}

// A longer wait than the longest wait of a call ends the call at once. The error keeps the wait,
// so that the caller, and not the call, waits for it.
func TestALongRetryAfterEndsTheCall(t *testing.T) {
	t.Parallel()

	policy, slept := waits(retry.Policy{
		Attempts: 3, First: 100 * time.Millisecond, Longest: 2 * time.Second,
	}, 0.5)
	call, tries := failing(retry.Again(errBusy, 10*time.Second))

	err := policy.Do(t.Context(), call)

	if *tries != 1 || len(*slept) != 0 {
		t.Errorf("the call ran %d times and waited %v, want one try and no wait", *tries, *slept)
	}

	if after, asked := retry.After(err); !asked || after != 10*time.Second {
		t.Errorf("After(%v) = %s, %t, want the 10s that the server named", err, after, asked)
	}
}

// An error that another try cannot pass, such as a refused credential, is not tried again.
func TestAnErrorThatAnotherTryCannotPassIsNotTried(t *testing.T) {
	t.Parallel()

	policy, slept := waits(retry.Default, 0.5)
	call, tries := failing(errBusy)

	if err := policy.Do(t.Context(), call); !errors.Is(err, errBusy) {
		t.Fatalf("Do() = %v, want the error", err)
	}

	if *tries != 1 || len(*slept) != 0 {
		t.Errorf("the call ran %d times and waited %v, want one try and no wait", *tries, *slept)
	}
}

// A call that succeeds stops the tries.
func TestASuccessStopsTheTries(t *testing.T) {
	t.Parallel()

	policy, _ := waits(retry.Default, 0.5)
	tries := 0

	err := policy.Do(t.Context(), func(context.Context) error {
		tries++
		if tries < 2 {
			return retry.Again(errBusy, 0)
		}

		return nil
	})
	if err != nil || tries != 2 {
		t.Errorf("Do() = %v after %d tries, want success at the second", err, tries)
	}
}

// The zero value is the default policy, so a client that sets nothing gets the policy of the
// design.
func TestTheZeroPolicyIsTheDefault(t *testing.T) {
	t.Parallel()

	policy, slept := waits(retry.Policy{}, 1)
	call, tries := failing(retry.Again(errBusy, 0))

	_ = policy.Do(t.Context(), call)

	if *tries != retry.Default.Attempts {
		t.Errorf("the call ran %d times, want the %d of the default", *tries, retry.Default.Attempts)
	}

	if len(*slept) == 0 || (*slept)[0] != retry.Default.First {
		t.Errorf("the policy waited %v, want the first ceiling %s first", *slept, retry.Default.First)
	}
}

// The deadline of a call bounds its waits as well as its tries. A wait that the deadline cuts
// ends the call with the error of the deadline.
func TestTheDeadlineEndsTheWait(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	policy := retry.Policy{
		Attempts: 3, First: time.Hour, Longest: time.Hour,
		Random: func() float64 { return 0.9 },
	}
	call, tries := failing(retry.Again(errBusy, 0))

	started := time.Now()
	err := policy.Do(ctx, call)

	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errBusy) {
		t.Errorf("Do() = %v, want the deadline and the last error", err)
	}

	if waited := time.Since(started); waited > 5*time.Second {
		t.Errorf("Do() returned after %s, want it at the deadline", waited)
	}

	if *tries != 1 {
		t.Errorf("the call ran %d times, want 1", *tries)
	}
}

// A call that ends after its context ended is not tried again.
func TestAnEndedContextStopsTheTries(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	policy, slept := waits(retry.Default, 0.5)
	call, tries := failing(retry.Again(errBusy, 0))

	err := policy.Do(ctx, call)

	if *tries != 1 || len(*slept) != 0 {
		t.Errorf("the call ran %d times and waited %v, want one try and no wait", *tries, *slept)
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Do() = %v, want the cancellation", err)
	}
}

// A source of the loop waits for the time that a server named before it reads again, up to a
// cap, so that a wrong or a hostile value cannot stop the source for long.
func TestPauseGivesTheNamedWaitUpToTheCap(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		err  error
		want time.Duration
	}{
		"no error":              {nil, 0},
		"an error with no wait": {errBusy, 0},
		"a short wait":          {retry.Again(errBusy, 10*time.Second), 10 * time.Second},
		"a wrapped wait": {
			fmt.Errorf("reading: %w", retry.Again(errBusy, 5*time.Second)), 5 * time.Second,
		},
		"a wait over the cap": {retry.Again(errBusy, 2*time.Hour), retry.MaxPause},
	} {
		if got := retry.Pause(c.err); got != c.want {
			t.Errorf("Pause() with %s = %s, want %s", name, got, c.want)
		}
	}
}

// Retry-After gives a number of seconds. The client reads no other form, and a value that it
// cannot read is no wait.
func TestParseAfterReadsSeconds(t *testing.T) {
	t.Parallel()

	now := time.Date(2015, 10, 21, 7, 28, 0, 0, time.UTC)

	for header, want := range map[string]time.Duration{
		"120":                  2 * time.Minute,
		"0":                    0,
		"":                     0,
		"-1":                   0,
		"1.5":                  0,
		"soon":                 0,
		"99999999999999999999": 0,
	} {
		if got := retry.ParseAfter(header, now); got != want {
			t.Errorf("ParseAfter(%q) = %s, want %s", header, got, want)
		}
	}
}

// A server can name the wait as a date (RFC 9110, section 10.2.3). The wait is the time from now,
// on the clock of the controller, to that date, in each of the three date forms that a recipient
// must read. A date that has passed names no wait.
func TestParseAfterReadsADate(t *testing.T) {
	t.Parallel()

	now := time.Date(2015, 10, 21, 7, 28, 0, 0, time.UTC)

	for header, want := range map[string]time.Duration{
		"Wed, 21 Oct 2015 07:28:30 GMT":     30 * time.Second,
		"Wednesday, 21-Oct-15 07:29:00 GMT": time.Minute,
		"Wed Oct 21 07:28:05 2015":          5 * time.Second,
		"Wed, 21 Oct 2015 07:28:00 GMT":     0,
		"Wed, 21 Oct 2015 07:27:00 GMT":     0,
		"21 Oct 2015":                       0,
	} {
		if got := retry.ParseAfter(header, now); got != want {
			t.Errorf("ParseAfter(%q) = %s, want %s", header, got, want)
		}
	}
}

// The marker of an error that another try can pass stays visible through wrapping, and the error
// keeps its message and its chain.
func TestAgainKeepsTheError(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("listing: %w", retry.Again(errBusy, 0))

	if !retry.Retryable(err) || !errors.Is(err, errBusy) {
		t.Errorf("the marked error %v lost its marker or its chain", err)
	}

	if err.Error() != "listing: "+errBusy.Error() {
		t.Errorf("the marked error reads %q, want the message of the error", err)
	}

	if retry.Retryable(errBusy) {
		t.Error("an unmarked error is retryable, want not")
	}
}
