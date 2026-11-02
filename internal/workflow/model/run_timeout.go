// Copyright (c) 2025 Reliant Labs
package model

import (
	"fmt"
	"time"
)

// MaxRunStepTimeout is the longest timeout: a run node may declare. A run
// node's timeout is the limit on its command (not the 10-minute cap on a
// model's shell call), so release cuts, full gates, builds and deploys run for
// hours; it is still a cap, so a typo'd timeout: cannot pin a machine's
// command, and the lane it holds, for days. Validation refuses a literal one
// above it at load; the shell tool caps one written as an expression.
const MaxRunStepTimeout = 12 * time.Hour

// RunStepTimeoutError reports why timeout cannot bound a run node's command,
// or nil when it can.
func RunStepTimeoutError(timeout time.Duration) error {
	if timeout > MaxRunStepTimeout {
		return fmt.Errorf("a run node's timeout may be at most %s, not %s", MaxRunStepTimeout, timeout)
	}
	return nil
}
