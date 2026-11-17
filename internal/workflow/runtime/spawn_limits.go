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

// liveSpawnCounts returns how many sub-agents parentThread has running, and
// how many the run has running at every depth. A parked spawn is still live.
func (t *ChildWorkflowTracker) liveSpawnCounts(parentThread string) (mine, total int) {
	for _, rec := range t.liveDetachedSpawns {
		total++
		if rec.ParentThread == parentThread {
			mine++
		}
	}
	return mine, total
}

// admitSpawns splits one batch's spawn calls into those that may start and
// refusals for those past the run's concurrency caps, in batch order. A
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
