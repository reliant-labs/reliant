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

// EdgeEvalContext is used for edge case conditions, node conditions, loop
// `items` and preset-name templates.
// Available namespaces: nodes, inputs, workflow, iter, plus outputs inside a
// loop body (see withLoopOutputs).
type EdgeEvalContext struct {
	Nodes    map[string]interface{} // dynamic — node outputs vary
	Inputs   map[string]interface{} // dynamic — depends on workflow def
	Workflow *model.WorkflowContext // typed
	Iter     *model.IterContext     // typed — nil when not in a loop
	Outputs  map[string]interface{} // enclosing loop's previous-iteration outputs; nil outside a loop body
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
	return withLoopOutputs([]CELNamespace{CELInputs, CELWorkflow, CELNodes, CELIter}, c.Outputs)
}

// =============================================================================
// LOOP EVAL CONTEXT
// =============================================================================

// LoopEvalContext is used for a loop's own `while` and `key` expressions.
// Available namespaces: iter (the loop's OWN current iteration), inputs, nodes
// (the scope the loop node lives in), workflow, plus outputs for `while` (the
// iteration's declared outputs; see withLoopOutputs). `key` is evaluated before
// any iteration has run, so it carries no outputs.
type LoopEvalContext struct {
	Iter     *model.IterContext     // typed — compile-time enforced
	Outputs  map[string]interface{} // the iteration's declared outputs (while); nil for key
	Inputs   map[string]interface{} // dynamic — depends on workflow def
	Nodes    map[string]interface{} // parent-scope node outputs
	Workflow *model.WorkflowContext // typed
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
	if c.Workflow != nil {
		m[string(CELWorkflow)] = c.Workflow
	}
	return EnsureNamespaceDefaults(m, c.Namespaces())
}

func (c *LoopEvalContext) Namespaces() []CELNamespace {
	return withLoopOutputs([]CELNamespace{CELIter, CELInputs, CELNodes, CELWorkflow}, c.Outputs)
}

// =============================================================================
// POST ACTIVITY CONTEXT
// =============================================================================

// PostActivityContext is used for save_message content/condition evaluation after
// a node runs.
// Available namespaces: output, inputs, workflow, iter.
//
// `nodes` is deliberately NOT available: a node's save_message is written by
// whoever executes the node — for an activity, the worker, which has no view of
// sibling node outputs. The environment is the same wherever the save runs.
//
// iter is included so a save_message declared on a node inside a loop can reference
// the loop iteration (e.g. "## Attempt {{iter.iteration + 1}}"), exactly like the
// inject and node-config resolution paths can. Iter is nil outside loops, in which
// case EnsureNamespaceDefaults supplies a zero default so bare iter references still
// compile.
type PostActivityContext struct {
	Output   interface{}            // the activity result
	Inputs   map[string]interface{} // dynamic — depends on workflow def
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
	if c.Workflow != nil {
		m[string(CELWorkflow)] = c.Workflow
	}
	if c.Iter != nil {
		m[string(CELIter)] = iterContextActivationValue(c.Iter)
	}
	return EnsureNamespaceDefaults(m, c.Namespaces())
}

func (c *PostActivityContext) Namespaces() []CELNamespace {
	return SaveMessageCELEnvConfig().Namespaces
}

// =============================================================================
// NODE RESOLUTION CONTEXT
// =============================================================================

// NodeResolutionContext is used for resolving node config/args CEL expressions
// (including thread.inject) and declared workflow outputs.
// Available namespaces: inputs, nodes, iter, workflow, plus outputs inside a
// loop body (see withLoopOutputs).
type NodeResolutionContext struct {
	Inputs   map[string]interface{} // dynamic — depends on workflow def
	Nodes    map[string]interface{} // dynamic — node outputs vary
	Iter     *model.IterContext     // typed — nil when not in a loop
	Workflow *model.WorkflowContext // typed
	// Outputs is the enclosing loop's PREVIOUS iteration outputs. Non-nil
	// (empty at iteration 0, always empty in a parallel iteration) inside a
	// loop body; nil everywhere else, which leaves `outputs` undeclared.
	Outputs map[string]interface{}
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
	if c.Outputs != nil {
		m[string(CELOutputs)] = c.Outputs
	}
	return EnsureNamespaceDefaults(m, c.Namespaces())
}

func (c *NodeResolutionContext) Namespaces() []CELNamespace {
	return withLoopOutputs([]CELNamespace{CELInputs, CELNodes, CELIter, CELWorkflow}, c.Outputs)
}

// withLoopOutputs appends `outputs` to a context's fixed namespaces when the
// context carries loop outputs.
//
// `outputs` is the one namespace whose presence depends on WHERE an expression
// sits rather than on which kind of expression it is: it exists inside a loop
// (the previous iteration's declared outputs for a body expression, the
// just-finished iteration's for `while`) and nowhere else. Every runtime site
// populates it with a non-nil map exactly when it is inside a loop, so "non-nil"
// is the in-loop signal. Declaring it outside a loop would compile
// `outputs.x` and then fail with "no such key" at runtime — validation reads
// these same Namespaces(), so undeclared is the honest answer.
func withLoopOutputs(fixed []CELNamespace, outputs map[string]interface{}) []CELNamespace {
	if outputs == nil {
		return fixed
	}
	return append(fixed, CELOutputs)
}

// =============================================================================
// ROUTER OUTPUT CONTEXT
// =============================================================================

// RouterOutputContext is used for a router node's declared `outputs`, evaluated
// after the selected workflow finished.
// Available namespaces: outputs (the SELECTED workflow's outputs), inputs,
// workflow. `outputs` here is the routed child's result, not loop outputs: a
// router's output mapping is written in terms of what it routed to.
type RouterOutputContext struct {
	Outputs  map[string]interface{} // the selected workflow's outputs
	Inputs   map[string]interface{} // dynamic — depends on workflow def
	Workflow *model.WorkflowContext // typed
}

func (c *RouterOutputContext) Activation() map[string]interface{} {
	m := make(map[string]interface{})
	if c.Outputs != nil {
		m[string(CELOutputs)] = c.Outputs
	}
	if c.Inputs != nil {
		m[string(CELInputs)] = c.Inputs
	}
	if c.Workflow != nil {
		m[string(CELWorkflow)] = c.Workflow
	}
	return EnsureNamespaceDefaults(m, c.Namespaces())
}

func (c *RouterOutputContext) Namespaces() []CELNamespace {
	return []CELNamespace{CELOutputs, CELInputs, CELWorkflow}
}

// =============================================================================
// WORKFLOW TEMPLATE CONTEXT
// =============================================================================

// WorkflowTemplateContext is used for the templates resolved once, when a
// workflow definition is loaded, before any node runs (see
// runtime.ResolveWorkflowTemplates): only inputs and workflow exist yet.
// Available namespaces: inputs, workflow.
type WorkflowTemplateContext struct {
	Inputs   map[string]interface{} // dynamic — depends on workflow def
	Workflow *model.WorkflowContext // typed
}

func (c *WorkflowTemplateContext) Activation() map[string]interface{} {
	m := make(map[string]interface{})
	if c.Inputs != nil {
		m[string(CELInputs)] = c.Inputs
	}
	if c.Workflow != nil {
		m[string(CELWorkflow)] = c.Workflow
	}
	return EnsureNamespaceDefaults(m, c.Namespaces())
}

func (c *WorkflowTemplateContext) Namespaces() []CELNamespace {
	return TemplateResolutionCELEnvConfig().Namespaces
}
