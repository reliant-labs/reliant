// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"strconv"

	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// spawnConcurrencyCapChangeID gates refusing a spawn past the run's
// concurrency caps (types.SpawnLimits). A history recorded before the caps
// replays every spawn it started.
const spawnConcurrencyCapChangeID = "spawn-concurrency-cap"

// spawnCapSkipsStoppingChangeID gates leaving a sub-agent that was told to
// stop out of the caps' counts (stoppingSpawn). A history recorded before it
// counted every live spawn, and replays that count.
const spawnCapSkipsStoppingChangeID = "spawn-cap-skips-stopping"

// spawnDepthInput is the workflow input prepareSpawnInline reads a spawning
// agent's depth from.
const spawnDepthInput = "spawn_depth"

// withSpawnerDepth returns inputs carrying the spawning agent's own depth, so
// the sub-agents it starts sit one level below it. A spawned agent's executor
// runs on its parent's inputs (runSpawnInlineChild), which carry the PARENT's
// depth, or none for a root; the agent's own depth is on its execution context
// (rtx.SpawnDepth). Without this a depth-1 agent's children would be recorded
// at depth 1 and could spawn again, past tools.MaxSpawnDepth.
func withSpawnerDepth(inputs map[string]interface{}, depth int) map[string]interface{} {
	if depth <= 0 {
		return inputs
	}
	if current, ok := inputs[spawnDepthInput].(int); ok && current == depth {
		return inputs
	}
	out := copyMap(inputs)
	out[spawnDepthInput] = depth
	return out
}

// liveSpawnCounts returns how many sub-agents parentThread holds a slot for,
// and how many the run holds at every depth: the live ones (a parked spawn is
// still live) and the ones admitted but not yet dispatched (reservations). A
// spawn that is both — registered live, its reservation not yet settled — is
// counted once. Only counts are taken, so map order does not matter.
func (t *ChildWorkflowTracker) liveSpawnCounts(parentThread string) (mine, total int) {
	return t.spawnCounts(parentThread, false)
}

// spawnCounts is liveSpawnCounts, leaving out the spawns stoppingSpawn
// reports when skipStopping is set.
func (t *ChildWorkflowTracker) spawnCounts(parentThread string, skipStopping bool) (mine, total int) {
	for _, rec := range t.liveDetachedSpawns {
		if skipStopping && t.stoppingSpawn(rec) {
			continue
		}
		total++
		if rec.ParentThread == parentThread {
			mine++
		}
	}
	for toolCallID, parent := range t.spawnReservations {
		if _, live := t.liveDetachedSpawns[toolCallID]; live {
			continue
		}
		total++
		if parent == parentThread {
			mine++
		}
	}
	return mine, total
}

// stoppingSpawn reports whether rec, or a spawn above it, was told to stop
// (spawn_stop, or cancel on its card). The stop is recorded at once — spawn_status
// and the UI then show it cancelled, and its subtree's rows are ended — but the
// spawn's loop observes it only at its next step boundary, so a long tool call
// or model turn keeps it registered live for minutes. It starts no new step in
// that time, so it no longer holds a concurrency slot: counting it refused
// spawns while spawn_status listed fewer running agents than the cap.
func (t *ChildWorkflowTracker) stoppingSpawn(rec *detachedSpawnRecord) bool {
	if t.spawnCancelled == nil {
		return false
	}
	// Bounded: spawn depth is small, and a malformed parent chain must not
	// loop forever.
	for hops := 0; rec != nil && hops < 16; hops++ {
		if t.spawnCancelled(rec.ToolCallID, rec.ChildThread) {
			return true
		}
		rec = t.liveSpawnFor(rec.ParentThread)
	}
	return false
}

// reserveSpawn holds a concurrency slot for a spawn admitSpawns admitted,
// from admission until its dispatch settles. Dispatch yields (thread prep runs
// activities), so without it two batches admitted at the same moment — two
// sub-agents fanning out together, or one parent's back-to-back turns — would
// each see the slot free and both start.
func (t *ChildWorkflowTracker) reserveSpawn(toolCallID, parentThread string) {
	if t.spawnReservations == nil {
		t.spawnReservations = make(map[string]string)
	}
	t.spawnReservations[toolCallID] = parentThread
}

// settleSpawnReservation releases toolCallID's reservation: its dispatch
// either registered it live (which now holds the slot) or failed, was refused
// or was cancelled (which frees it). Safe for a spawn that holds none.
func (t *ChildWorkflowTracker) settleSpawnReservation(toolCallID string) {
	delete(t.spawnReservations, toolCallID)
}

// admitSpawns splits one batch's spawn calls into those that may start and
// refusals for those past the run's concurrency caps, in batch order. Each
// admitted spawn reserves its slot in the tracker until dispatchSpawnBackground
// settles it, so admissions that overlap in time see one another. A
// refusal is a tool result: the agent learns why and spawns again once a
// sub-agent finishes. Nothing is queued, so a refused spawn holds no slot and
// starts no thread.
//
// A resumption of an agent that is already running is admitted untouched:
// refuseResumingLiveAgent answers it, and it starts nothing new.
func (t *ChildWorkflowTracker) admitSpawns(ctx workflow.Context, parentThread string, configs []*spawnChildWorkflowConfig) (admitted []*spawnChildWorkflowConfig, refused []interface{}) {
	if len(configs) == 0 {
		return configs, nil
	}
	limits := t.spawnLimits.Resolved()
	mine, total := t.liveSpawnCounts(parentThread)
	// A spawn told to stop holds no slot. Versioned only when that changes
	// the counts, so a run with no stopping spawn records no marker.
	if m, tot := t.spawnCounts(parentThread, true); (m != mine || tot != total) &&
		workflow.GetVersion(ctx, spawnCapSkipsStoppingChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion {
		mine, total = m, tot
	}
	gated := false
	for _, cfg := range configs {
		if cfg.isResumption && t.liveSpawnFor(cfg.childThread) != nil {
			admitted = append(admitted, cfg)
			continue
		}
		if mine >= limits.PerAgent || total >= limits.Total {
			// Versioned only where a refusal would happen, so a batch under
			// the caps records no marker.
			if !gated {
				gated = true
				if workflow.GetVersion(ctx, spawnConcurrencyCapChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
					return configs, nil
				}
			}
			refused = append(refused, map[string]interface{}{
				"tool_call_id": cfg.toolCallID,
				"content":      spawnCapRefusal(limits, mine, total),
				"is_error":     true,
			})
			continue
		}
		admitted = append(admitted, cfg)
		t.reserveSpawn(cfg.toolCallID, parentThread)
		mine++
		total++
	}
	return admitted, refused
}

// spawnCapRefusal is the tool result for a spawn refused at a cap. It names
// the cap, the input that sets it, and what to do instead.
func spawnCapRefusal(limits types.SpawnLimits, mine, total int) string {
	var why string
	if mine >= limits.PerAgent {
		why = "you already have " + strconv.Itoa(mine) + " sub-agents running, the most one agent may run at once (the run's " +
			types.MaxConcurrentSubAgentsInput + " input, " + strconv.Itoa(limits.PerAgent) + ")"
	} else {
		why = "this run already has " + strconv.Itoa(total) + " sub-agents running across all agents, the most a run may run at once (the run's " +
			types.MaxConcurrentSubAgentsTotalInput + " input, " + strconv.Itoa(limits.Total) + ")"
	}
	return fmt.Sprintf("Spawn refused, and nothing was started: %s. "+
		"Wait for a sub-agent to finish (you are notified when one does, or call spawn_status with wait: true), then spawn this one again. "+
		"Do not merge units of work to fit under the limit.", why)
}
