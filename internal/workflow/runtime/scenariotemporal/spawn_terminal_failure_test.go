// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A spawn whose failure is DETERMINISTIC must not hang the workflow that
// spawned it.
//
// structured-agent's `spawn_drops_response_data` was the only scenario in this
// lane that exhausted scenarioRunBudget, and the reason was not the thing its
// name suggests. The agent loop's exit logic was correct throughout: at
// iteration 2 the response tool's response_data arrives, `completed` evaluates
// true, and the `while` condition is logged going false. The workflow hung
// anyway, AFTER the exit decision, on this chain:
//
//  1. Iteration 1's `spawn` tool call is NOT satisfied by the scenario mock —
//     executeToolsWithSpawnSupport splits spawn calls out and runs them through
//     the real runtime, so the harness's ExecuteTools mock never sees it.
//  2. That spawn runs builtin://agent inline with no project path and fails
//     setup: "project path not set, cannot load presets". No retry can fix it.
//  3. isTransientSpawnExecutionError read only the ApplicationError shape, so
//     this plain in-workflow error classified as TRANSIENT, and
//     runSpawnInlineChild's deliberately unbounded retry loop retried it at the
//     30s backoff ceiling — ~63,000 times in 20s of wall clock.
//  4. The spawn therefore never completed, completeDetachedSpawn never ran, and
//     the parent loop — which parks in awaitLiveDetachedSpawns precisely when
//     it is about to exit — waited forever on a child that could never finish.
//
// The failure mode is the dangerous one: the loop's exit condition was
// satisfied and the run still never ended. In production this is a chat that
// shows as running indefinitely after the agent has already finished its work,
// with a worker burning a retry every 30 seconds.
//
// This asserts TERMINATION, not the scenario's expectations. The scenario is
// separately under-specified (see spawn_drops_response_data's mismatches: the
// loop legitimately re-enters after the spawn resolves and outruns its 3
// mocks), and pinning its pass/fail here would couple this regression to that
// unrelated question. What must never regress is that the run ENDS.
func TestSpawnTerminalFailure_DoesNotHangParentLoop(t *testing.T) {
	wf := loadBuiltin(t, "structured-agent")
	all := loadScenarios(t, "../../builtin/testdata/structured-agent_scenarios.yaml")
	sc := findScenario(t, all, "spawn_drops_response_data")

	start := time.Now()
	res := NewRunner(wf).Run(sc)
	elapsed := time.Since(start)

	t.Logf("status=%s outcome=%s elapsed=%s", res.Status, res.Execution.Outcome, elapsed)

	// The budget is the harness's last-resort guard against exactly this hang.
	// Tripping it means the workflow did not terminate on its own.
	require.Less(t, elapsed, scenarioRunBudget,
		"the workflow must terminate on its own, not be cut off by the run budget")
	for _, m := range res.Mismatches {
		require.NotEqual(t, nonTerminatingMessage, m,
			"a deterministic spawn failure must not leave the parent loop parked "+
				"in awaitLiveDetachedSpawns; the spawn has to fail terminally so "+
				"completeDetachedSpawn releases the parent")
	}
}
