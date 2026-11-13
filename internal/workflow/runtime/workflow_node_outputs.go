// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// workflowNodeOutputsChangeID gates publishing a `type: workflow` node's child
// outputs under nodes.<id>.outputs as well as flat. Histories recorded before
// it read the flat shape only, and a branch on `nodes.<id>.?outputs` must
// replay the way it ran.
const workflowNodeOutputsChangeID = "workflow-node-publishes-outputs"

// workflowNodeOutputsField is the key the reference (WorkflowOutput.outputs),
// the validator and scenario mocks all name for a workflow node's outputs.
const workflowNodeOutputsField = "outputs"

// publishWorkflowNodeOutputs is the value a completed `type: workflow` node
// publishes: the child's declared outputs flat on the node (nodes.<id>.result),
// as always, and the same map under nodes.<id>.outputs.
//
// The reference documents `outputs`, the validator accepts it on a workflow
// node, and a scenario's black-box mock may supply it, but the runtime only
// flattened: a workflow written against the documented shape passed
// validation and its scenarios and read nothing at runtime (control-plane
// cp-merge read nodes.regen.?outputs.?result and got "error" for every clean
// run). A child that declares its own output named `outputs` keeps it. Other
// node types, routers included (they set their own `outputs`), are returned
// unchanged.
func publishWorkflowNodeOutputs(ctx workflow.Context, node *reliantv1.Node, out map[string]interface{}) map[string]interface{} {
	if node.GetType() != model.NodeTypeWorkflow {
		return out
	}
	if _, declared := out[workflowNodeOutputsField]; declared {
		return out
	}
	if workflow.GetVersion(ctx, workflowNodeOutputsChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return out
	}
	children := make(map[string]interface{}, len(out))
	published := make(map[string]interface{}, len(out)+1)
	for k, v := range out {
		children[k] = v
		published[k] = v
	}
	published[workflowNodeOutputsField] = children
	return published
}
