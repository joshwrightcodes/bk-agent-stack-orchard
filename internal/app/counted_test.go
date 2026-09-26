package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
)

// A failure of the system counts, with a name that an operator can group by. An answer about one
// job is not a failure of the system, and a call that the shutdown cancelled failed nowhere.
func TestTheCauseOfAFailedCall(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		err     error
		cause   string
		counted bool
	}{
		{nil, "", false},
		{context.Canceled, "", false},
		{fmt.Errorf("finishing: %w", buildkite.ErrTerminal), "", false},
		{orchard.ErrAlreadyExists, "", false},
		{orchard.ErrNotFound, "", false},
		{fmt.Errorf("polling: %w", buildkite.ErrUnauthorized), errorUnauthorized, true},
		{buildkite.ErrRateLimited, errorRateLimited, true},
		{fmt.Errorf("listing: %w", context.DeadlineExceeded), errorTimeout, true},
		{errors.New("orchard answered 503 Service Unavailable"), errorOther, true},
	} {
		got, counted := cause(c.err)
		if got != c.cause || counted != c.counted {
			t.Errorf("cause(%v) = %q, %t, want %q, %t", c.err, got, counted, c.cause, c.counted)
		}
	}
}
