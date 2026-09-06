// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// runSpawnInlineChild retries a failed spawn forever, by design: a spawn is
// long-running and must survive any number of worker restarts. That makes
// isTransientSpawnExecutionError the ONLY thing preventing a deterministic
// failure from becoming an infinite retry, and it was reading only half of the
// two error shapes the runtime produces.
//
// An activity's failure crosses the boundary as a temporal.ApplicationError,
// which it did check. But an error raised by in-workflow code — the executors,
// preset loading, input binding — never crosses an activity boundary and stays
// a plain Go error. Those fell through to `return true` and were retried at the
// 30s backoff ceiling forever.
//
// Observed: a spawn of builtin://agent with no project path fails setup with
// "project path not set, cannot load presets", which no retry can fix. It was
// retried ~63,000 times in 20 seconds of wall clock. Because the spawn never
// finished, completeDetachedSpawn never ran, so the PARENT loop stayed parked
// in awaitLiveDetachedSpawns even though its `while` condition had already
// gone false — the workflow hung with its exit condition correctly satisfied.
func TestIsTransientSpawnExecutionError_TerminalShapes(t *testing.T) {
	t.Parallel()

	t.Run("in-workflow TerminalError is terminal", func(t *testing.T) {
		err := &TerminalError{Message: "project path not set, cannot load presets"}
		require.False(t, isTransientSpawnExecutionError(err),
			"a *TerminalError raised in-workflow never becomes an ApplicationError, "+
				"so matching only ApplicationError classifies it as transient and retries it forever")
	})

	t.Run("wrapped TerminalError is terminal", func(t *testing.T) {
		// The real path wraps twice before the classifier sees it:
		// inline_workflow_executor's "load presets for node %s: %w" and
		// runSpawnInlineChild's "sub-workflow %s failed: %w".
		err := fmt.Errorf("sub-workflow builtin://agent failed: %w",
			fmt.Errorf("load presets for node spawn-call_0: %w",
				&TerminalError{Message: "project path not set, cannot load presets"}))
		require.False(t, isTransientSpawnExecutionError(err),
			"the classifier must unwrap — it sees the error only after two layers of %%w wrapping")
	})

	t.Run("non-retryable ApplicationError is terminal", func(t *testing.T) {
		err := temporal.NewNonRetryableApplicationError("bad auth", "TerminalError", nil)
		require.False(t, isTransientSpawnExecutionError(err))
	})

	t.Run("genuinely transient error still retries", func(t *testing.T) {
		// The behavior the unbounded retry exists for must survive the fix:
		// a worker restart / heartbeat timeout has to keep being retried.
		require.True(t, isTransientSpawnExecutionError(errors.New("worker restart: heartbeat timeout")),
			"unclassified errors must remain retryable — a spawn has to survive worker restarts")
	})

	t.Run("nil is not transient", func(t *testing.T) {
		require.False(t, isTransientSpawnExecutionError(nil))
	})
}
