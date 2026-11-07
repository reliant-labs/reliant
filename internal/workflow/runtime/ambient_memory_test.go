package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
)

func registerAmbientWorkflow(t *testing.T, env *testsuite.TestWorkflowEnvironment, source string) {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(source))
	require.NoError(t, err)
	data, err := protojson.Marshal(wf)
	require.NoError(t, err)
	env.RegisterActivityWithOptions(func(context.Context, types.LoadWorkflowInput) (LoadedWorkflow, error) {
		return LoadedWorkflow{WorkflowJSON: data}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
}
func replaceAmbientLoopParallel(source string) string {
	return strings.Replace(source, "type: loop\n", "type: loop\n          parallel: true\n", 1)
}
func replaceAmbientCallOptOut(source string) string {
	return strings.Replace(source, "type: call_llm\n", "type: call_llm\n          args:\n            exclude_ambient_memory: true\n", 1)
}

func ambientBoundaryNode(kind, mode string, excluded bool) *reliantv1.Node {
	tc := &reliantv1.ThreadConfig{Mode: mode, ExcludeAmbientMemory: excluded}
	node := &reliantv1.Node{Id: "boundary", Type: kind}
	switch kind {
	case "workflow":
		node.Args = &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Thread: tc}}
	case "loop":
		node.Args = &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{Thread: tc}}
	case "router":
		node.Args = &reliantv1.Node_Router{Router: &reliantv1.RouterArgs{Thread: tc}}
	}
	return node
}

func TestAmbientMemoryPolicyNestedBoundaries(t *testing.T) {
	for _, kind := range []string{"workflow", "loop", "router"} {
		for _, mode := range []string{"inherit", "new", "fork"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				parent := NewExecutionContext("wf", "chat", "root", "root-thread")
				node := ambientBoundaryNode(kind, mode, true)
				child := parent.ForChild("child", mode, "child-workflow", false)
				tracker := &ChildWorkflowTracker{}
				var actual *ExecutionContext
				switch kind {
				case "workflow":
					actual = (&InlineWorkflowExecutor{evalResult: node, childTracker: tracker}).WithExecContext(child).execContext
				case "loop":
					actual = (&InlineLoopExecutor{loopStep: &core.TriggeredNode{Node: node}, childTracker: tracker}).WithExecContext(child).execContext
				case "router":
					actual = (&RouterExecutor{evalResult: node, childTracker: tracker}).WithExecContext(child).execContext
				}
				require.True(t, actual.ExcludeAmbientMemory)
				require.False(t, parent.ExcludeAmbientMemory)
				require.False(t, child.ExcludeAmbientMemory, "policy derivation must not mutate shared context")
				for _, descendantMode := range []string{"inherit", "new", "fork"} {
					nested := actual.ForIteration(2, false).ForChildWorkflow("nested-wf", "nested", descendantMode, "nested", false)
					nested = nested.withAmbientMemoryPolicy(ambientBoundaryNode("workflow", descendantMode, false))
					require.True(t, nested.ExcludeAmbientMemory, "false descendant cannot reenable")
					require.True(t, nested.Clone().ExcludeAmbientMemory)
					data, err := json.Marshal(nested)
					require.NoError(t, err)
					var restored ExecutionContext
					require.NoError(t, json.Unmarshal(data, &restored))
					require.True(t, restored.ExcludeAmbientMemory, "Temporal JSON roundtrip preserves exclusion")
				}
				normal := parent.withAmbientMemoryPolicy(ambientBoundaryNode(kind, mode, false))
				require.False(t, normal.ExcludeAmbientMemory)
			})
		}
	}
}

// A whole DynamicWorkflow, with real child initialization, serial/parallel
// iteration and activity dispatch. Only activities are stubbed by the existing
// deterministic spawn harness; runtime policy travels through the real engine.
func TestAmbientMemoryPolicyDynamicNestedWorkflows(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(map[bool]string{false: "sequential", true: "parallel"}[parallel], func(t *testing.T) {
			var suite SpawnBackgroundE2ESuite
			env := suite.NewTestWorkflowEnvironment()
			e := newSpawnE2EEnv(t, env, nil)
			source := `
name: ambient-nested
entry: [excluded]
nodes:
  - id: excluded
    type: workflow
    thread:
      mode: new
      exclude_ambient_memory: true
    inline:
      entry: [iterations]
      nodes:
        - id: iterations
          type: loop
          items: '{{["one", "two"]}}'
          while: iter.index < 2
          thread:
            mode: new
            exclude_ambient_memory: false
          inline:
            entry: [inner]
            nodes:
              - id: inner
                type: workflow
                thread:
                  mode: new
                  exclude_ambient_memory: false
                inline:
                  entry: [draft]
                  nodes:
                    - id: draft
                      type: call_llm
                      args:
                        exclude_ambient_memory: false
`
			if parallel {
				source = replaceAmbientLoopParallel(source)
			}
			registerAmbientWorkflow(t, env, source)
			input := spawnE2EWorkflowInput("ambient-nested")
			env.ExecuteWorkflow(DynamicWorkflow, input)
			require.NoError(t, env.GetWorkflowError())
			require.Len(t, e.callLLMRuntimes, 2)
			for _, rtx := range e.callLLMRuntimes {
				require.True(t, rtx.ExcludeAmbientMemory, rtx.NodePath)
			}
		})
	}
}

func TestAmbientMemoryPolicySpawnAndRelaunch(t *testing.T) {
	var suite SpawnBackgroundE2ESuite
	env := suite.NewTestWorkflowEnvironment()
	e := newSpawnE2EEnv(t, env, []scriptedToolCallsResponse{{toolCalls: []map[string]interface{}{spawnToolCall("tc-memory", "approved brief")}}, {}})
	e.childTurnWaitsFor = milestoneParentTurnDelivered(2)
	registerAmbientWorkflow(t, env, replaceAmbientCallOptOut(spawnBackgroundE2EYAML))
	e.execute("ambient-spawn")
	require.NoError(t, env.GetWorkflowError())
	require.GreaterOrEqual(t, len(e.callLLMRuntimes), 3)
	for _, rtx := range e.callLLMRuntimes {
		require.True(t, rtx.ExcludeAmbientMemory, rtx.Thread)
	}
	// Relaunch a false/default child handoff under an excluded parent.
	handoff := SpawnHandoff{ParentThread: "parent", ChildThread: "child", ChildInputs: map[string]interface{}{ambientMemoryInput: false}}
	inputs := map[string]interface{}{ambientMemoryInput: true}
	prep := prepareSpawnRelaunch(&spawnChildWorkflowConfig{childThread: "child"}, handoff, "chat", "", inputs, func(string) *PauseController { return nil })
	require.True(t, prep.childExecContext.ExcludeAmbientMemory)
}

func TestAmbientMemoryPolicyDynamicRouterDispatch(t *testing.T) {
	var suite SpawnBackgroundE2ESuite
	env := suite.NewTestWorkflowEnvironment()
	newSpawnE2EEnv(t, env, nil)
	registerAmbientWorkflow(t, env, `
name: ambient-router
entry: [classify]
nodes:
  - id: classify
    type: router
    model: mock-model
    thread:
      mode: new
      exclude_ambient_memory: true
    nodes:
      - id: draft
        description: approved draft
  - id: draft
    type: call_llm
`)
	var sent []types.RuntimeContext
	env.RegisterActivityWithOptions(func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
		sent = append(sent, input.Runtime)
		if input.Node.GetCallLlm().GetResponseTool() != nil {
			return map[string]interface{}{"response_text": `{"selected_node":"draft","reasoning":"approved"}`}, nil
		}
		return map[string]interface{}{"tool_calls": []interface{}{}}, nil
	}, activity.RegisterOptions{Name: "CallLLM"})
	env.ExecuteWorkflow(DynamicWorkflow, spawnE2EWorkflowInput("ambient-router"))
	require.NoError(t, env.GetWorkflowError())
	require.NotEmpty(t, sent)
	require.True(t, sent[0].ExcludeAmbientMemory, "router's direct CallLLM path must carry its policy")
}

func TestAmbientMemoryPolicyHandoffAndDefaults(t *testing.T) {
	tracker := &ChildWorkflowTracker{}
	tracker.excludeAmbientMemory("parent")
	data, err := json.Marshal(ResumeInput{AmbientMemoryExcluded: tracker.ambientMemoryHandoff()})
	require.NoError(t, err)
	var restored ResumeInput
	require.NoError(t, json.Unmarshal(data, &restored))
	require.True(t, restored.AmbientMemoryExcluded["parent"])
	require.Empty(t, (&ChildWorkflowTracker{}).ambientMemoryHandoff())
	child := tracker.ambientMemoryContext((&ExecutionContext{Thread: "parent"}).ForChild("nested", model.ThreadModeNew, "nested", false), ambientBoundaryNode("workflow", "new", false))
	require.True(t, child.ExcludeAmbientMemory)
	require.True(t, excludesAmbientMemory(buildSpawnChildInputs(map[string]interface{}{ambientMemoryInput: true})))
	require.False(t, excludesAmbientMemory(buildSpawnChildInputs(nil)))
}
