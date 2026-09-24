package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A workflow declaring one input of every type, none with a default.
const declaredInputsYAML = `
name: child
entry: [a]
inputs:
  text:  {type: string}
  msg:   {type: message}
  count: {type: integer}
  ratio: {type: number}
  flag:  {type: boolean}
  list:  {type: array}
  tools: {type: tools}
  files: {type: attachments}
  obj:   {type: object}
  blob:  {type: any}
  pick:  {type: enum, enum: [a, b]}
  picks: {type: enum, enum: [a, b], multi: true}
  preset:  {type: preset}
  presets: {type: preset, multi: true}
  mdl:   {type: model}
nodes:
  - id: a
    type: run
    command: "echo {{inputs.text}}"
`

// Each expression reads an unsupplied input with the operator its type is
// normally used with. Before inputs were bound to typed zeros, the ones over
// attachments / any / multi-select enum and preset failed ("no such overload"
// on a "" placeholder).
var declaredInputZeroExprs = []string{
	"inputs.text == ''",
	"inputs.msg == ''",
	"inputs.count == 0",
	"inputs.ratio == 0.0",
	"inputs.flag == false",
	"size(inputs.list) == 0",
	"size(inputs.tools + ['ask_user']) == 1",
	"size(inputs.files + [{'id': 'f'}]) == 1",
	"size(inputs.obj) == 0",
	"!has(inputs.blob.field) && size(inputs.blob) == 0",
	"inputs.pick == ''",
	"!('a' in inputs.picks)",
	"inputs.preset == ''",
	"size(inputs.presets) == 0",
	"inputs.mdl == null && size(inputs.mdl) == 0",
}

func parseDeclaredInputsWorkflow(t *testing.T) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(declaredInputsYAML))
	require.NoError(t, err)
	return wf
}

func assertDeclaredInputZeros(t *testing.T, inputs map[string]interface{}) {
	t.Helper()
	ctx := &wfcel.NodeResolutionContext{Inputs: inputs, Nodes: map[string]interface{}{}}
	for _, expr := range declaredInputZeroExprs {
		got, err := wfcel.EvaluateBool(expr, ctx)
		if assert.NoError(t, err, expr) {
			assert.True(t, got, expr)
		}
	}
	rendered, err := wfcel.EvaluateTemplate("[{{inputs.text}}]", ctx)
	require.NoError(t, err)
	assert.Equal(t, "[]", rendered)
}

func TestDeclaredInputsBoundToTypedZeros_TopLevel(t *testing.T) {
	t.Parallel()
	wf := parseDeclaredInputsWorkflow(t)
	assertDeclaredInputZeros(t, ApplyDefaultsForRuntime(map[string]interface{}{}, wf.GetInputs()))
}

func TestDeclaredInputsBoundToTypedZeros_SubWorkflow(t *testing.T) {
	t.Parallel()
	executor := &InlineWorkflowExecutor{
		subWorkflowInputs:  map[string]interface{}{},
		subWorkflow:        parseDeclaredInputsWorkflow(t),
		invocationContract: &core.SubWorkflowContract{InputPolicy: core.InputPolicyRefPresetsArgsDefaults},
	}
	assertDeclaredInputZeros(t, executor.buildSubWorkflowInputs())
}

// A spawn child runs as a synthetic workflow node built by newSpawnNode and
// executed by the same InlineWorkflowExecutor input assembly.
func TestDeclaredInputsBoundToTypedZeros_SpawnChild(t *testing.T) {
	t.Parallel()
	spawnNode := newSpawnNode("tc-1", spawnTargetWorkflow, "", buildSpawnChildInputs(map[string]interface{}{}))
	executor := &InlineWorkflowExecutor{
		node:               spawnNode,
		evalResult:         spawnNode,
		subWorkflowInputs:  model.NodeMergedSubWorkflowInputs(spawnNode),
		subWorkflow:        parseDeclaredInputsWorkflow(t),
		invocationContract: &core.SubWorkflowContract{InputPolicy: core.InputPolicyRefPresetsArgsDefaults},
	}
	assertDeclaredInputZeros(t, executor.buildSubWorkflowInputs())
}

func TestDeclaredInputsBoundToTypedZeros_LoopBody(t *testing.T) {
	t.Parallel()
	loopNode := &reliantv1.Node{
		Id:   "loop_node",
		Type: "loop",
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			While: &reliantv1.DirectCelBool{Expr: "iter.iteration < 1"},
		}},
	}
	executor := &InlineLoopExecutor{
		loopID:             "loop_node",
		loopStep:           &core.TriggeredNode{Node: loopNode},
		workflowInputs:     map[string]interface{}{},
		subWorkflow:        parseDeclaredInputsWorkflow(t),
		invocationContract: &core.SubWorkflowContract{InputPolicy: core.InputPolicyRefPresetsArgsDefaults},
	}
	iterInputs, err := executor.buildIterationInputs()
	require.NoError(t, err)
	assertDeclaredInputZeros(t, iterInputs)
}

// Binding zeros happens only AFTER required inputs are validated: an input
// with no default is required, and omitting it is still a start error.
func TestDeclaredInputs_RequiredStillRejected(t *testing.T) {
	t.Parallel()
	wf := parseDeclaredInputsWorkflow(t)
	result := validation.ValidateInputs(wf, map[string]any{})
	require.True(t, result.HasErrors())
	assert.Contains(t, result.Error(), "required input 'text' is not provided")
}
