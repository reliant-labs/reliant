// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
)

// ============================================================================
// resolveResumeTarget unit tests
// ============================================================================

func resumeTestWorkflow(t *testing.T, yamlStr string) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(yamlStr))
	require.NoError(t, err)
	return wf
}

func TestResolveResumeTarget(t *testing.T) {
	seqWf := resumeTestWorkflow(t, `
name: seq
entry: [plan]
nodes:
  - id: plan
    type: call_llm
  - id: work
    type: call_llm
edges:
  - from: plan
    default: work
`)

	singleLoopWf := resumeTestWorkflow(t, `
name: single-loop
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: iter.iteration < 3
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
`)

	multiLoopWf := resumeTestWorkflow(t, `
name: multi-loop
entry: [loop_a]
nodes:
  - id: loop_a
    type: loop
    while: iter.iteration < 1
    inline:
      entry: [a]
      nodes:
        - id: a
          type: call_llm
  - id: loop_b
    type: loop
    while: iter.iteration < 1
    inline:
      entry: [b]
      nodes:
        - id: b
          type: call_llm
edges:
  - from: loop_a
    default: loop_b
`)

	logger := &resumeTestLogger{}

	t.Run("nil resume means fresh start", func(t *testing.T) {
		node, iter := resolveResumeTarget(seqWf, nil, logger)
		assert.Nil(t, node)
		assert.Equal(t, 0, iter)
	})

	t.Run("checkpoint node is the default target", func(t *testing.T) {
		node, iter := resolveResumeTarget(seqWf, &ResumeInput{NodeID: "work", LoopIteration: 4}, logger)
		require.NotNil(t, node)
		assert.Equal(t, "work", node.GetId())
		assert.Equal(t, 4, iter)
	})

	t.Run("resume_node override wins over checkpoint", func(t *testing.T) {
		wf := resumeTestWorkflow(t, `
name: seq-override
entry: [plan]
resume_node: plan
nodes:
  - id: plan
    type: call_llm
  - id: work
    type: call_llm
edges:
  - from: plan
    default: work
`)
		node, iter := resolveResumeTarget(wf, &ResumeInput{NodeID: "work", LoopIteration: 4}, logger)
		require.NotNil(t, node)
		assert.Equal(t, "plan", node.GetId())
		// Iteration belongs to the checkpoint node; a different override target
		// starts at iteration 0.
		assert.Equal(t, 0, iter)
	})

	t.Run("resume_node override keeps iteration when it matches the checkpoint node", func(t *testing.T) {
		wf := resumeTestWorkflow(t, `
name: loop-override
entry: [agent_loop]
resume_node: agent_loop
nodes:
  - id: agent_loop
    type: loop
    while: iter.iteration < 3
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
`)
		node, iter := resolveResumeTarget(wf, &ResumeInput{NodeID: "agent_loop", LoopIteration: 2}, logger)
		require.NotNil(t, node)
		assert.Equal(t, "agent_loop", node.GetId())
		assert.Equal(t, 2, iter)
	})

	t.Run("unknown checkpoint node falls back to single top-level loop", func(t *testing.T) {
		node, iter := resolveResumeTarget(singleLoopWf, &ResumeInput{NodeID: "gone", LoopIteration: 7}, logger)
		require.NotNil(t, node)
		assert.Equal(t, "agent_loop", node.GetId())
		assert.Equal(t, 0, iter, "fallback target must not inherit the checkpoint iteration")
	})

	t.Run("empty resume falls back to single top-level loop", func(t *testing.T) {
		node, _ := resolveResumeTarget(singleLoopWf, &ResumeInput{}, logger)
		require.NotNil(t, node)
		assert.Equal(t, "agent_loop", node.GetId())
	})

	t.Run("ambiguous loops fall back to graph start", func(t *testing.T) {
		node, _ := resolveResumeTarget(multiLoopWf, &ResumeInput{NodeID: "gone"}, logger)
		assert.Nil(t, node)
	})

	t.Run("no loops and no checkpoint falls back to graph start", func(t *testing.T) {
		node, _ := resolveResumeTarget(seqWf, &ResumeInput{}, logger)
		assert.Nil(t, node)
	})
}

// resumeTestLogger is a minimal log.Logger for pure-function tests.
type resumeTestLogger struct{}

func (l *resumeTestLogger) Debug(string, ...interface{}) {}
func (l *resumeTestLogger) Info(string, ...interface{})  {}
func (l *resumeTestLogger) Warn(string, ...interface{})  {}
func (l *resumeTestLogger) Error(string, ...interface{}) {}

// ============================================================================
// DynamicWorkflow resume-mode integration tests (Temporal test env)
// ============================================================================

// resumeEnvRecorder captures which nodes executed (via the CallLLM stub) and
// which position checkpoints were persisted (via the WorkflowCheckpoint stub).
type resumeEnvRecorder struct {
	executedNodes []string
	checkpoints   []capturedCheckpoint
	// llmCalls records each CallLLM execution with the nested loop iteration it
	// ran under (RuntimeContext.LoopIteration), so nested-loop tests can assert
	// which iteration a node ran in.
	llmCalls []capturedCheckpoint
	// statuses records every WorkflowStatus notification the run emitted, so a
	// test can assert what the run REPORTED about itself — not just what it did.
	statuses []map[string]interface{}
}

type capturedCheckpoint struct {
	nodeID    string
	iteration int
}

// setupResumeEnv registers the stub activities DynamicWorkflow needs and wires
// the recorder. The workflow definition is served from the given YAML.
func setupResumeEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, yamlStr string) *resumeEnvRecorder {
	t.Helper()
	rec := &resumeEnvRecorder{}

	wf, err := wfyaml.ParseWorkflow([]byte(yamlStr))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (interface{}, error) {
			rec.statuses = append(rec.statuses, input)
			return nil, nil
		},
		activity.RegisterOptions{Name: "WorkflowStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			nodeID, _ := input["node_id"].(string)
			iter := 0
			if f, ok := input["loop_iteration"].(float64); ok {
				iter = int(f)
			}
			rec.checkpoints = append(rec.checkpoints, capturedCheckpoint{nodeID: nodeID, iteration: iter})
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowCheckpoint"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "Cleanup"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			rec.executedNodes = append(rec.executedNodes, input.Node.GetId())
			rec.llmCalls = append(rec.llmCalls, capturedCheckpoint{
				nodeID:    input.Node.GetId(),
				iteration: input.Runtime.LoopIteration,
			})
			return map[string]interface{}{"response_text": "ok"}, nil
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)

	return rec
}

func resumeWorkflowInput(chatID string, resume *ResumeInput) WorkflowInput {
	return WorkflowInput{
		ChatID:       chatID,
		WorkflowName: "resume-test",
		Inputs:       map[string]interface{}{},
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-resume",
			ChatID:       chatID,
			Thread:       "thread-resume",
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "resume-test",
		},
		Resume: resume,
	}
}

const resumeSeqYAML = `
name: resume-test
entry: [plan]
nodes:
  - id: plan
    type: call_llm
  - id: work
    type: call_llm
  - id: final
    type: call_llm
edges:
  - from: plan
    default: work
  - from: work
    default: final
`

const resumeLoopYAML = `
name: resume-test
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: iter.iteration < 3
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
`

func TestDynamicWorkflow_FreshStart_ChecksNodeEntryCheckpoints(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, resumeSeqYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-fresh", nil))

	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, []string{"plan", "work", "final"}, rec.executedNodes,
		"fresh start executes the full graph from entry")
	assert.Equal(t, []capturedCheckpoint{
		{nodeID: "plan"}, {nodeID: "work"}, {nodeID: "final"},
	}, rec.checkpoints, "every top-level node entry persists a position checkpoint")
}

func TestDynamicWorkflow_Resume_EntersAtCheckpointNode(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, resumeSeqYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-resume", &ResumeInput{NodeID: "work"}))

	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, []string{"work", "final"}, rec.executedNodes,
		"resume enters at the checkpointed node (skipping graph entry) and continues edge routing from there")
	assert.NotContains(t, rec.executedNodes, "plan", "entry node must not re-run on resume")
}

func TestDynamicWorkflow_Resume_YAMLOverrideWins(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	yamlWithOverride := resumeSeqYAML + "resume_node: final\n"
	rec := setupResumeEnv(t, env, yamlWithOverride)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-override", &ResumeInput{NodeID: "work"}))

	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, []string{"final"}, rec.executedNodes,
		"resume_node YAML override takes precedence over the checkpoint node")
}

func TestDynamicWorkflow_Resume_EmptyCheckpointFallsBackToGraphStart(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, resumeSeqYAML)

	// Resume mode with no checkpoint and no single top-level loop: the engine
	// falls back to normal graph entry (fresh run with thread history).
	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-fallback", &ResumeInput{}))

	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, []string{"plan", "work", "final"}, rec.executedNodes)
}

func TestDynamicWorkflow_FreshLoop_ChecksIterationCheckpoints(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, resumeLoopYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-loop-fresh", nil))

	require.NoError(t, env.GetWorkflowError())
	assert.Len(t, rec.executedNodes, 3, "while iter.iteration < 3 runs iterations 0..2")
	assert.Equal(t, []capturedCheckpoint{
		{nodeID: "agent_loop", iteration: 0},
		{nodeID: "agent_loop", iteration: 1},
		{nodeID: "agent_loop", iteration: 2},
	}, rec.checkpoints, "loop nodes checkpoint per iteration, not on node entry")
}

func TestDynamicWorkflow_ResumeLoop_ReentersAtCheckpointedIteration(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, resumeLoopYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-loop-resume", &ResumeInput{
		NodeID:        "agent_loop",
		LoopIteration: 2,
	}))

	require.NoError(t, env.GetWorkflowError())
	assert.Len(t, rec.executedNodes, 1,
		"resuming at iteration 2 of a 3-iteration guard runs exactly the interrupted iteration")
	assert.Equal(t, []capturedCheckpoint{
		{nodeID: "agent_loop", iteration: 2},
	}, rec.checkpoints, "the resumed loop re-checkpoints from the recorded iteration, keeping max-iteration guards honest")
}

// ============================================================================
// Nested-loop (get-it-right-shaped) resume: Finding-1 regression coverage
// ============================================================================

// resumeNestedLoopYAML mirrors get-it-right's structure: a top-level workflow
// node ("gir") whose INLINE sub-workflow's top node is a loop ("attempt") whose
// body ("work") calls the LLM. Because sub-workflows run INLINE in the parent's
// single Temporal execution, the nested loop's iteration counter lives in the
// parent run's goroutine stack — which is exactly why reset-and-replay of the
// parent rebuilds it, and why the FLAT position checkpoint (top-level node only)
// cannot.
const resumeNestedLoopYAML = `
name: resume-test
entry: [gir]
nodes:
  - id: gir
    type: workflow
    inline:
      name: inner
      entry: [attempt]
      nodes:
        - id: attempt
          type: loop
          while: iter.iteration < 3
          inline:
            entry: [work]
            nodes:
              - id: work
                type: call_llm
`

// workIterations returns the nested loop iteration of every `work` CallLLM.
func workIterations(rec *resumeEnvRecorder) []int {
	var iters []int
	for _, c := range rec.llmCalls {
		if c.nodeID == "work" {
			iters = append(iters, c.iteration)
		}
	}
	return iters
}

// The nested loop runs inline in the parent run, and the FLAT checkpoint records
// only the TOP-LEVEL node — never the nested loop node or its iteration. This is
// the structural reason the coarse fresh-restart-with-checkpoint cannot express
// a mid-nested-get-it-right position (Finding-1's root cause).
//
// This limitation is real and expected: resuming precisely inside a nested loop
// is what reset-and-replay is for, and Temporal already provides it. A position
// table that tried to describe the nesting was the wrong answer to it.
func TestDynamicWorkflow_NestedLoop_FlatCheckpointOmitsNestedIteration(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, resumeNestedLoopYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-nested-fresh", nil))

	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, []int{0, 1, 2}, workIterations(rec),
		"the nested loop executes inline within the parent Temporal run")

	require.NotEmpty(t, rec.checkpoints)
	for _, cp := range rec.checkpoints {
		assert.Equal(t, "gir", cp.nodeID,
			"only the top-level workflow node is flat-checkpointed; the nested loop is invisible to it")
	}
}

// Coarse resume can only name the TOP-LEVEL node ("gir") in the flat checkpoint;
// there is no field to carry the nested loop iteration, so the resumed run
// re-enters gir and the nested loop restarts from iteration 0 — the Finding-1
// regression. Reset-and-replay (exercised end-to-end against a live Temporal
// server in e2e/stories/story_10_nested_loop_resume_test.go) is what preserves
// the nested iteration instead; this test pins the coarse path's limitation that
// motivated the fix.
func TestDynamicWorkflow_NestedLoop_CoarseResumeRestartsNestedAtZero(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, resumeNestedLoopYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-nested-coarse", &ResumeInput{NodeID: "gir"}))

	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, []int{0, 1, 2}, workIterations(rec),
		"coarse resume at the top-level node restarts the nested loop at iteration 0 — the flat checkpoint cannot resume mid-nested-loop")
}

// The nested-resume limitation is engine-level, so it applies to ANY nested
// loop — not only the get-it-right (loop-inside-a-workflow-node) shape. This
// uses a PLAIN loop-nested-in-loop and shows the same structural fact: the flat
// checkpoint records only the TOP-LEVEL loop ("outer"), never the inner loop's
// iteration, which is why only reset-and-replay can resume the inner loop
// precisely.
const resumeGenericNestedLoopYAML = `
name: resume-test
entry: [outer]
nodes:
  - id: outer
    type: loop
    while: iter.iteration < 2
    inline:
      entry: [inner]
      nodes:
        - id: inner
          type: loop
          while: iter.iteration < 3
          inline:
            entry: [work]
            nodes:
              - id: work
                type: call_llm
`

func TestDynamicWorkflow_GenericNestedLoop_FlatCheckpointOmitsInnerIteration(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, resumeGenericNestedLoopYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-generic-nested", nil))

	require.NoError(t, env.GetWorkflowError())
	// 2 outer iterations × 3 inner iterations = 6 inline work executions.
	assert.Len(t, workIterations(rec), 6, "the inner loop runs inline within each outer iteration")

	// Every flat checkpoint is the TOP-LEVEL loop ("outer"); the inner loop's
	// iteration is never captured — the generic root cause of Finding-1.
	require.NotEmpty(t, rec.checkpoints)
	for _, cp := range rec.checkpoints {
		assert.Equal(t, "outer", cp.nodeID,
			"only the top-level loop is flat-checkpointed; the inner loop iteration is invisible")
	}
}
