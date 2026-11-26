// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// serverActionFrom is the node a server action's rendered `with: {from:
// <id>}` names, as this executor holds it: the definition it runs (type,
// role, and whether it is an engine-owned review node) and the output it
// recorded. nil for any other node, and when from names no node of this
// executor's scope.
//
// The author picks which node; the engine supplies what that node is and
// what it produced. That is what lets pr.verdict@1 trust a verdict: the
// review output a `with:` could carry is the author's rendering, this is
// the run's own record. Both are workflow state, so a replay derives the
// same value.
func (e *StepExecutor) serverActionFrom(evalResult *reliantv1.Node) *types.ServerActionFrom {
	args := evalResult.GetAction()
	if args == nil || !serveraction.IsRef(model.CelStringRaw(args.GetUses())) {
		return nil
	}
	from := strings.TrimSpace(args.GetWith()["from"].GetStringValue())
	if from == "" || e.workflow == nil {
		return nil
	}
	var def *reliantv1.Node
	for _, n := range e.workflow.GetNodes() {
		if n.GetId() == from {
			def = n
			break
		}
	}
	if def == nil {
		return nil
	}
	out := &types.ServerActionFrom{
		NodeID: from,
		Type:   model.NodeType(def),
		Role:   def.GetRole(),
		Review: model.IsLoweredReviewNode(def),
	}
	if recorded, ok := e.nodeOutputs[from].(map[string]interface{}); ok {
		out.Ran = true
		out.Output = recorded
	}
	return out
}
