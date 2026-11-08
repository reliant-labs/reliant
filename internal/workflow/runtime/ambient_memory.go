package runtime

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// Derive, rather than mutate a shared parent's context: sibling workflows may
// have different policies. OR makes false/unset incapable of widening a child.
func (ctx *ExecutionContext) withAmbientMemoryPolicy(node *reliantv1.Node) *ExecutionContext {
	if ctx == nil {
		return nil
	}
	child := *ctx
	child.ExcludeAmbientMemory = ctx.ExcludeAmbientMemory || model.NodeThreadConfig(node).GetExcludeAmbientMemory()
	return &child
}

func (t *ChildWorkflowTracker) ambientMemoryContext(ctx *ExecutionContext, node *reliantv1.Node) *ExecutionContext {
	child := ctx.withAmbientMemoryPolicy(node)
	if child != nil {
		child.ExcludeAmbientMemory = child.ExcludeAmbientMemory || t.excludesAmbientMemory(child.Thread) || t.excludesAmbientMemory(child.ParentThread)
	}
	return child
}

// Private runtime input, never a workflow/model-controlled tool argument. It is
// carried in spawn child inputs, including their continue-as-new handoff.
const ambientMemoryInput = "_runtime_exclude_ambient_memory"

func excludesAmbientMemory(inputs map[string]interface{}) bool {
	excluded, _ := inputs[ambientMemoryInput].(bool)
	return excluded
}

func (t *ChildWorkflowTracker) excludesAmbientMemory(thread string) bool {
	return t != nil && t.ambientMemoryExcluded[thread]
}
func (t *ChildWorkflowTracker) excludeAmbientMemory(thread string) {
	if t == nil {
		return
	}
	if t.ambientMemoryExcluded == nil {
		t.ambientMemoryExcluded = map[string]bool{}
	}
	t.ambientMemoryExcluded[thread] = true
}
func (t *ChildWorkflowTracker) ambientMemoryHandoff() map[string]bool {
	if t == nil || len(t.ambientMemoryExcluded) == 0 {
		return nil
	}
	out := make(map[string]bool, len(t.ambientMemoryExcluded))
	for thread, excluded := range t.ambientMemoryExcluded {
		out[thread] = excluded
	}
	return out
}
