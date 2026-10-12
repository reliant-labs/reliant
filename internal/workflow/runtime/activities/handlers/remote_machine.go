// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"slices"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// machineOfflineHint is appended to a machine tool's daemon-offline error when
// the turn was offered wait_for_machine. The original text stays first: the
// daemon-offline circuit breaker counts on its "no daemon connected" substring.
const machineOfflineHint = "\n\n[The machine this run uses is not connected — it may be restarting, " +
	"reconnecting or asleep — so this call did not run. Do not end your turn over this, and do not keep " +
	"retrying the call: call wait_for_machine, which returns as soon as the machine is back, then retry.]"

// withWaitForMachine hands wait_for_machine to a node that can reach any
// machine tool. A node that reaches none can never see a daemon-offline error,
// so it is not given the tool. Idempotent; the caller's slices are not mutated.
func withWaitForMachine(access tools.ToolAccess) tools.ToolAccess {
	if slices.Contains(access.Preloaded, tools.ToolWaitForMachine) {
		return access
	}
	reachesMachine := access.LoadableAll ||
		slices.ContainsFunc(access.Preloaded, tools.NeedsMachine) ||
		slices.ContainsFunc(access.Loadable, tools.NeedsMachine)
	if !reachesMachine {
		return access
	}
	access.Preloaded = append(slices.Clone(access.Preloaded), tools.ToolWaitForMachine)
	return access
}
