// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package stack

import (
	"context"
	"time"
)

// SetSleep replaces the wait of Register, so that a test records each wait and does not sleep.
func (s *Stack) SetSleep(sleep func(ctx context.Context, d time.Duration) error) { s.sleep = sleep }
