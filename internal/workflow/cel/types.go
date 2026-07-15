package wfcel

import (
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

func iterContextActivationValue(iter *model.IterContext) map[string]interface{} {
	if iter == nil {
		return map[string]interface{}{"iteration": 0, "index": 0}
	}
	activation := map[string]interface{}{
		"iteration": iter.Iteration,
		"index":     iter.Index,
	}
	if iter.Item != nil {
		activation["item"] = iter.Item
	}
	if iter.Key != "" {
		activation["key"] = iter.Key
	}
	return activation
}

// =============================================================================
// CEL NAMESPACE CONSTANTS
// =============================================================================

// CELNamespace defines the available CEL variable namespaces.
type CELNamespace string

const (
	// CELInputs provides access to workflow input parameters.
	// Usage: inputs.query, inputs.mode, inputs.max_turns
	// Type: map[string]any (dynamic - user-defined inputs vary by workflow)
	CELInputs CELNamespace = "inputs"

	// CELWorkflow provides workflow metadata and environment context.
	// Usage: workflow.id, workflow.name, workflow.path, workflow.branch
	// Type: model.WorkflowContext (statically typed - known fields)
	CELWorkflow CELNamespace = "workflow"

	// CELNodes provides access to previous node outputs.
	// Usage: nodes.call_llm.tool_calls, nodes.agent_loop.succeeded
	// Type: map[string]any (dynamic - node outputs vary by activity type)
	CELNodes CELNamespace = "nodes"

	// CELIter provides loop iteration context.
	// Usage: iter.iteration
	// Type: model.IterContext (statically typed)
	CELIter CELNamespace = "iter"

	// CELOutput provides current activity output (save_message context).
	// Usage: output.message.role, output.tool_calls
	// Type: map[string]any (dynamic - activity outputs vary)
	CELOutput CELNamespace = "output"

	// CELOutputs provides sub-workflow outputs (loop while context).
	// Usage: outputs.exit_code, outputs.tool_calls
	// Type: map[string]any (dynamic - workflow outputs vary)
	CELOutputs CELNamespace = "outputs"
)

// AllNamespaces returns all defined CEL namespaces.
func AllNamespaces() []CELNamespace {
	return []CELNamespace{
		CELInputs,
		CELWorkflow,
		CELNodes,
		CELIter,
		CELOutput,
		CELOutputs,
	}
}

// =============================================================================
// TYPED CEL EVALUATION CONTEXTS
// =============================================================================
//
// These typed context structs eliminate raw map[string]interface{} construction
// at CEL evaluation call sites. Each struct enforces correct types at compile time
// and produces the activation map needed for CEL program evaluation.

// CELEvalContext is the interface for all typed CEL evaluation contexts.
type CELEvalContext interface {
	// Activation returns the map for CEL program evaluation.
	Activation() map[string]interface{}
	// Namespaces returns which CEL namespaces this context provides.
	Namespaces() []CELNamespace
}

// =============================================================================
// EDGE EVAL CONTEXT
// =============================================================================

// EdgeEvalContext is used for edge/transition condition evaluation.
// Available namespaces: nodes, inputs, workflow, iter, outputs.
type EdgeEvalContext struct {
	Nodes    map[string]interface{} // dynamic — node outputs vary
	Inputs   map[string]interface{} // dynamic — depends on workflow def
	Workflow *model.WorkflowContext // typed
	Iter     *model.IterContext     // typed — nil when not in a loop
	Outputs  map[string]interface{} // dynamic — loop outputs
}

func (c *EdgeEvalContext) Activation() map[string]interface{} {
	m := make(map[string]interface{})
	if c.Nodes != nil {
		m[string(CELNodes)] = c.Nodes
	}
	if c.Inputs != nil {
		m[string(CELInputs)] = c.Inputs
	}
	if c.Workflow != nil {
		m[string(CELWorkflow)] = c.Workflow
	}
	if c.Iter != nil {
		m[string(CELIter)] = iterContextActivationValue(c.Iter)
	}
	if c.Outputs != nil {
		m[string(CELOutputs)] = c.Outputs
	}
	return EnsureNamespaceDefaults(m, c.Namespaces())
}

func (c *EdgeEvalContext) Namespaces() []CELNamespace {
	return []CELNamespace{CELInputs, CELWorkflow, CELNodes, CELIter, CELOutputs}
}

// =============================================================================
// LOOP EVAL CONTEXT
// =============================================================================

// LoopEvalContext is used for while condition evaluation inside loops.
// Available namespaces: iter, outputs, inputs, nodes (optional).
type LoopEvalContext struct {
	Iter    *model.IterContext     // typed — compile-time enforced
	Outputs map[string]interface{} // dynamic — depends on workflow def
	Inputs  map[string]interface{} // dynamic — depends on workflow def
	Nodes   map[string]interface{} // optional — parent node outputs for while conditions that reference nodes.*
}

func (c *LoopEvalContext) Activation() map[string]interface{} {
	m := make(map[string]interface{})
	if c.Iter != nil {
		m[string(CELIter)] = iterContextActivationValue(c.Iter)
	}
	if c.Outputs != nil {
		m[string(CELOutputs)] = c.Outputs
	}
	if c.Inputs != nil {
		m[string(CELInputs)] = c.Inputs
	}
	if c.Nodes != nil {
		m[string(CELNodes)] = c.Nodes
	}
	return EnsureNamespaceDefaults(m, c.Namespaces())
}

func (c *LoopEvalContext) Namespaces() []CELNamespace {
	return []CELNamespace{CELIter, CELOutputs, CELInputs, CELNodes}
}

// =============================================================================
// POST ACTIVITY CONTEXT
// =============================================================================

// PostActivityContext is used for save_message content/condition evaluation after
// an activity runs.
// Available namespaces: output, inputs, nodes, workflow, iter.
//
// iter is included so a save_message declared on a node inside a loop can reference
// the loop iteration (e.g. "## Attempt {{iter.iteration + 1}}"), exactly like the
// inject and node-config resolution paths can. Iter is nil outside loops, in which
// case EnsureNamespaceDefaults supplies a zero default so bare iter references still
// compile.
type PostActivityContext struct {
	Output   interface{}            // the activity result
	Inputs   map[string]interface{} // dynamic — depends on workflow def
	Nodes    map[string]interface{} // dynamic — node outputs vary
	Workflow *model.WorkflowContext // typed
	Iter     *model.IterContext     // typed — nil when not in a loop
}

func (c *PostActivityContext) Activation() map[string]interface{} {
	m := make(map[string]interface{})
	if c.Output != nil {
		m[string(CELOutput)] = c.Output
	}
	if c.Inputs != nil {
		m[string(CELInputs)] = c.Inputs
	}
	if c.Nodes != nil {
		m[string(CELNodes)] = c.Nodes
	}
	if c.Workflow != nil {
		m[string(CELWorkflow)] = c.Workflow
	}
	if c.Iter != nil {
		m[string(CELIter)] = iterContextActivationValue(c.Iter)
	}
	return EnsureNamespaceDefaults(m, c.Namespaces())
}

func (c *PostActivityContext) Namespaces() []CELNamespace {
	return []CELNamespace{CELOutput, CELInputs, CELNodes, CELWorkflow, CELIter}
}

// =============================================================================
// NODE RESOLUTION CONTEXT
// =============================================================================

// NodeResolutionContext is used for resolving node config/args CEL expressions.
// Available namespaces: inputs, nodes, iter, workflow.
type NodeResolutionContext struct {
	Inputs   map[string]interface{} // dynamic — depends on workflow def
	Nodes    map[string]interface{} // dynamic — node outputs vary
	Iter     *model.IterContext     // typed — nil when not in a loop
	Workflow *model.WorkflowContext // typed
}

func (c *NodeResolutionContext) Activation() map[string]interface{} {
	m := make(map[string]interface{})
	if c.Inputs != nil {
		m[string(CELInputs)] = c.Inputs
	}
	if c.Nodes != nil {
		m[string(CELNodes)] = c.Nodes
	}
	if c.Iter != nil {
		m[string(CELIter)] = iterContextActivationValue(c.Iter)
	}
	if c.Workflow != nil {
		m[string(CELWorkflow)] = c.Workflow
	}
	return EnsureNamespaceDefaults(m, c.Namespaces())
}

func (c *NodeResolutionContext) Namespaces() []CELNamespace {
	return []CELNamespace{CELInputs, CELNodes, CELIter, CELWorkflow}
}
