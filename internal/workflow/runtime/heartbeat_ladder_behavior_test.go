// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The decision the three executors actually make on a retry-exhausted step:
// re-dispatch with a fresh ladder, or pause the chat and tell the user.
//
// This models that branch directly — the real sites are inside ~200-line
// select loops in DynamicWorkflow, InlineLoopExecutor and
// InlineWorkflowExecutor that need a full workflow environment, a chat, a
// provider and a database to reach. What decides the outcome there is exactly
// the conjunction below, so pinning it here pins the behavior that failed for
// chat ee527bdd, and does it in milliseconds.
//
// Kept in lockstep with the three call sites by
// TestRetryExhaustionSitesShareOneDecision, which greps them.
func retryExhaustedAction(restarts *ladderRestarts, stepID string, err error) string {
	if heartbeatCancelExhausted(err) && restarts.grantRestart(stepID) {
		return "redispatch"
	}
	return "pause"
}

// Chat ee527bdd's five CallLLM attempts were all killed by heartbeat RPC
// failures inside a single burst — the stream was healthy every time, and the
// chat still auto-paused showing the user a Temporal deadline error. After this
// change the step keeps going, and only pauses if the burst outlasts the bound.
func TestHeartbeatExhaustionRedispatchesInsteadOfPausing(t *testing.T) {
	t.Parallel()

	heartbeatErr := exhaustedErrorOfType(t, heartbeatCancelErrorType,
		"heartbeat RPC failed while running CallLLM; retrying")

	t.Run("a heartbeat burst re-dispatches rather than pausing", func(t *testing.T) {
		var restarts ladderRestarts
		assert.Equal(t, "redispatch", retryExhaustedAction(&restarts, "call_llm", heartbeatErr),
			"this is the exact case that paused chat ee527bdd")
	})

	t.Run("a sustained outage still pauses once the bound is spent", func(t *testing.T) {
		var restarts ladderRestarts
		for i := range maxHeartbeatLadderRestarts {
			require.Equal(t, "redispatch", retryExhaustedAction(&restarts, "call_llm", heartbeatErr),
				"restart %d should still be granted", i+1)
		}
		assert.Equal(t, "pause", retryExhaustedAction(&restarts, "call_llm", heartbeatErr),
			"a Temporal server that is down must eventually pause, not spin forever")
	})

	t.Run("a real provider failure pauses immediately", func(t *testing.T) {
		rateLimited := exhaustedErrorOfType(t, "RateLimitError", "429 Too Many Requests")
		var restarts ladderRestarts
		assert.Equal(t, "pause", retryExhaustedAction(&restarts, "call_llm", rateLimited),
			"retrying a rate limit harder does not make it succeed")
	})

	t.Run("a recovered step regains its full allowance", func(t *testing.T) {
		var restarts ladderRestarts
		for range maxHeartbeatLadderRestarts {
			require.Equal(t, "redispatch", retryExhaustedAction(&restarts, "call_llm", heartbeatErr))
		}
		require.Equal(t, "pause", retryExhaustedAction(&restarts, "call_llm", heartbeatErr))

		// The step later succeeds; the executors call clear() on that path.
		restarts.clear("call_llm")

		assert.Equal(t, "redispatch", retryExhaustedAction(&restarts, "call_llm", heartbeatErr),
			"a burst hours later must not inherit an allowance spent this morning")
	})
}
