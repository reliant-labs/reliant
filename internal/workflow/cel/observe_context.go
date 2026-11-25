package wfcel

import "github.com/reliant-labs/reliant/internal/workflow/model"

// The two namespaces only an observe: block sees. They are deliberately not
// in AllNamespaces: no other site binds them, and `item` is a common
// comprehension variable name elsewhere.
const (
	// CELNode is the node that finished: node.id, node.type, node.path,
	// node.attempt, node.duration_ms (model.ObservedNode).
	CELNode CELNamespace = "node"
	// CELItem is the element an observe event's `each:` is emitting.
	CELItem CELNamespace = "item"
)

// ObserveContext is the scope of a node's observe: block, evaluated after the
// node finishes, wherever it finishes (the worker for an activity node, the
// workflow for a node it completes itself). It is save_message's scope
// (PostActivityContext: output, inputs, trigger, workflow, iter, and failure
// or run in a terminal handler) plus `node`, and `item` inside `each:`. As in
// save_message there is no `nodes`: a node records what its own output says.
type ObserveContext struct {
	Output   interface{}            // the node's result
	Inputs   map[string]interface{} // only the inputs the block references
	Workflow *model.WorkflowContext
	Iter     *model.IterContext // nil when not in a loop
	Node     *model.ObservedNode
	// InEach binds `item` (Item) for an event's attributes under `each:`.
	InEach bool
	Item   interface{}
}

func (c *ObserveContext) Activation() map[string]interface{} {
	m := (&PostActivityContext{Output: c.Output, Inputs: c.Inputs, Workflow: c.Workflow, Iter: c.Iter}).Activation()
	m[string(CELNode)] = observedNodeActivation(c.Node)
	if c.InEach {
		m[string(CELItem)] = c.Item
	}
	return EnsureNamespaceDefaults(m, c.Namespaces())
}

func (c *ObserveContext) Namespaces() []CELNamespace {
	fixed := append(append([]CELNamespace{}, SaveMessageCELEnvConfig().Namespaces...), CELNode)
	if c.InEach {
		fixed = append(fixed, CELItem)
	}
	return withFailure(fixed, c.Inputs)
}

// observedNodeActivation is node as a CEL value.
func observedNodeActivation(n *model.ObservedNode) map[string]interface{} {
	if n == nil {
		n = &model.ObservedNode{}
	}
	return map[string]interface{}{
		"id":          n.ID,
		"type":        n.Type,
		"path":        n.Path,
		"attempt":     n.Attempt,
		"duration_ms": n.DurationMs,
	}
}
