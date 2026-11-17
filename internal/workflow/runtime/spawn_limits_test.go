// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// These pin the sub-agent concurrency caps (reliant#2757): with spawning two
// levels deep, one agent's fan-out and a run's whole tree are both capped, and
// a spawn past either is refused with an explanation rather than started.

type SpawnLimitsSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestSpawnLimits(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(SpawnLimitsSuite))
}

type admitResult struct {
	Admitted []string
	Refused  []string
}

// admitSpawnsWorkflow runs admitSpawns for parent against tracker. The
// tracker is captured rather than passed, so it is the same object the test
// seeded (workflow arguments are round-tripped through the DataConverter).
func admitSpawnsWorkflow(tracker *ChildWorkflowTracker, parent string, configs []*spawnChildWorkflowConfig) func(workflow.Context) (admitResult, error) {
	return func(ctx workflow.Context) (admitResult, error) {
		admitted, refused := tracker.admitSpawns(ctx, parent, configs)
		var out admitResult
		for _, cfg := range admitted {
			out.Admitted = append(out.Admitted, cfg.toolCallID)
		}
		for _, r := range refused {
			m := r.(map[string]interface{})
			if m["is_error"] != true {
				return out, fmt.Errorf("refusal %v is not an error result", m)
			}
			out.Refused = append(out.Refused, m["tool_call_id"].(string)+": "+m["content"].(string))
		}
		return out, nil
	}
}

func (s *SpawnLimitsSuite) admit(tracker *ChildWorkflowTracker, parent string, ids ...string) admitResult {
	var configs []*spawnChildWorkflowConfig
	for _, id := range ids {
		configs = append(configs, &spawnChildWorkflowConfig{toolCallID: id, childThread: "child-" + id})
	}
	env := s.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(admitSpawnsWorkflow(tracker, parent, configs))
	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	var out admitResult
	s.Require().NoError(env.GetWorkflowResult(&out))
	return out
}

func liveSpawns(tracker *ChildWorkflowTracker, parent string, n int) {
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-live-%d", parent, i)
		tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: id, ParentThread: parent, ChildThread: "child-" + id})
	}
}

func (s *SpawnLimitsSuite) TestUnderTheCaps_EveryoneStarts() {
	tracker := &ChildWorkflowTracker{}
	liveSpawns(tracker, "lead", 2)

	got := s.admit(tracker, "lead", "a", "b", "c")
	s.Equal([]string{"a", "b", "c"}, got.Admitted)
	s.Empty(got.Refused)
}

// A batch that crosses the per-agent cap starts what fits, in order, and
// refuses the rest with the cap, the input that sets it, and what to do.
func (s *SpawnLimitsSuite) TestPerAgentCap_RefusesPastIt() {
	tracker := &ChildWorkflowTracker{}
	liveSpawns(tracker, "lead", types.DefaultMaxConcurrentSubAgents-1)

	got := s.admit(tracker, "lead", "a", "b", "c")
	s.Equal([]string{"a"}, got.Admitted)
	s.Require().Len(got.Refused, 2)
	for _, r := range got.Refused {
		s.Contains(r, "nothing was started")
		s.Contains(r, "already have 8 sub-agents running")
		s.Contains(r, types.MaxConcurrentSubAgentsInput)
		s.Contains(r, "spawn_status")
	}
	s.Contains(got.Refused[0], "b: ")
	s.Contains(got.Refused[1], "c: ")
}

// The per-agent cap counts only the spawner's own children: a sibling's
// children do not use its slots.
func (s *SpawnLimitsSuite) TestPerAgentCap_CountsOnlyOwnChildren() {
	tracker := &ChildWorkflowTracker{}
	liveSpawns(tracker, "worker-1", types.DefaultMaxConcurrentSubAgents)

	got := s.admit(tracker, "worker-2", "a")
	s.Equal([]string{"a"}, got.Admitted)
}

// The run-wide cap counts every depth: workers' sub-agents and the lead's
// together.
func (s *SpawnLimitsSuite) TestTotalCap_CountsTheWholeTree() {
	tracker := &ChildWorkflowTracker{spawnLimits: types.SpawnLimits{PerAgent: 8, Total: 5}}
	liveSpawns(tracker, "lead", 2)
	liveSpawns(tracker, "worker-1", 2)

	got := s.admit(tracker, "worker-2", "a", "b")
	s.Equal([]string{"a"}, got.Admitted)
	s.Require().Len(got.Refused, 1)
	s.Contains(got.Refused[0], "this run already has 5 sub-agents running")
	s.Contains(got.Refused[0], types.MaxConcurrentSubAgentsTotalInput)
}

// Resuming an agent that is already running starts nothing, so it neither
// takes a slot nor is refused for one: refuseResumingLiveAgent answers it.
func (s *SpawnLimitsSuite) TestResumingALiveAgent_IsNotCapped() {
	tracker := &ChildWorkflowTracker{}
	liveSpawns(tracker, "lead", types.DefaultMaxConcurrentSubAgents)

	env := s.NewTestWorkflowEnvironment()
	cfg := &spawnChildWorkflowConfig{toolCallID: "again", childThread: "child-lead-live-0", isResumption: true}
	env.ExecuteWorkflow(admitSpawnsWorkflow(tracker, "lead", []*spawnChildWorkflowConfig{cfg}))
	s.Require().NoError(env.GetWorkflowError())
	var out admitResult
	s.Require().NoError(env.GetWorkflowResult(&out))
	s.Equal([]string{"again"}, out.Admitted)
	s.Empty(out.Refused)
}

func TestSpawnLimitsFromInputs(t *testing.T) {
	t.Parallel()
	assert.Equal(t, types.SpawnLimits{PerAgent: 8, Total: 16}, types.SpawnLimitsFromInputs(nil))
	assert.Equal(t, types.SpawnLimits{PerAgent: 3, Total: 40}, types.SpawnLimitsFromInputs(map[string]interface{}{
		types.MaxConcurrentSubAgentsInput:      float64(3),
		types.MaxConcurrentSubAgentsTotalInput: 40,
	}))
	assert.Equal(t, types.SpawnLimits{PerAgent: 8, Total: 16}, types.SpawnLimitsFromInputs(map[string]interface{}{
		types.MaxConcurrentSubAgentsInput:      0,
		types.MaxConcurrentSubAgentsTotalInput: "lots",
	}), "a non-positive or non-numeric value keeps the default")
	assert.Equal(t, types.SpawnLimits{PerAgent: 8, Total: 16}, types.SpawnLimits{}.Resolved())
}

// A spawned agent's executor runs on its PARENT's inputs, so the spawn depth
// its own sub-agents are recorded at must come from its execution context.
// Without that, a depth-1 agent's children were recorded at depth 1 and could
// spawn again, past tools.MaxSpawnDepth.
func TestWithSpawnerDepth_ChildrenSitOneLevelBelowTheSpawner(t *testing.T) {
	t.Parallel()

	root := map[string]interface{}{"mode": "auto"}
	assert.Equal(t, root, withSpawnerDepth(root, 0), "a root agent's inputs are left alone")

	// The depth-1 agent runs on the root's inputs, which carry no depth.
	sub := withSpawnerDepth(root, 1)
	assert.Equal(t, 1, sub[spawnDepthInput])
	assert.Nil(t, root[spawnDepthInput], "the parent's inputs are copied, not mutated")
	assert.Equal(t, "auto", sub["mode"])

	// The depth-2 agent runs on the depth-1 agent's inputs.
	assert.Equal(t, 2, withSpawnerDepth(sub, 2)[spawnDepthInput])
}
