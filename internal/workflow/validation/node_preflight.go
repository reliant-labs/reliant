package validation

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/machinepreflight"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// preflightNodeTypes are the node kinds a preflight means something on: they
// run on a machine, or (a loop, a workflow or review node) run a body that
// does. Any other kind runs in the workflow or on the server, so a preflight
// there would check a machine nothing uses, and is an error.
var preflightNodeTypes = map[string]bool{
	model.NodeTypeRun:            true,
	model.NodeTypeGitPublish:     true,
	model.NodeTypeCreateWorktree: true,
	model.NodeTypeExecuteTools:   true,
	model.NodeTypeInvokeTool:     true,
	model.NodeTypeAction:         true,
	model.NodeTypeWorkflow:       true,
	model.NodeTypeLoop:           true,
	model.NodeTypeReview:         true,
}

// validateNodePreflight holds Node.preflight to the node kinds that run on a
// machine and to values the check can use.
func validateNodePreflight(node *reliantv1.Node, nodePath []string, result *Result) {
	if node.GetPreflight() == nil {
		return
	}
	if !preflightNodeTypes[node.GetType()] {
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "preflight",
			fmt.Sprintf("preflight checks the machine a node runs on, and a %s node does not run on one", node.GetType()),
			"remove preflight, or set it on the run, agent, workflow or loop node that does the machine's work")
		return
	}
	if node.GetType() == model.NodeTypeAction && actionIsServerPlaced(node.GetAction()) {
		result.AddErrorWithSuggestion(CategoryStructure, nodePath, "preflight",
			fmt.Sprintf("preflight checks the machine a node runs on, and action %s runs on the server", model.CelStringRaw(node.GetAction().GetUses())),
			"remove preflight from this action")
		return
	}
	if _, _, err := machinepreflight.FromProto(node.GetPreflight()); err != nil {
		result.AddError(CategoryStructure, nodePath, "preflight", "preflight."+err.Error())
	}
}

// actionIsServerPlaced reports whether an action node's literal `uses` names
// an action that never runs on a machine: a Reliant server action, or a
// catalog action placed on the server. An expression, or an action this
// build cannot resolve, is not known to be either.
func actionIsServerPlaced(args *reliantv1.ActionArgs) bool {
	uses := args.GetUses()
	if uses == nil || model.CelStringIsExpr(uses) {
		return false
	}
	ref := strings.TrimSpace(model.CelStringRaw(uses))
	if serveraction.IsRef(ref) {
		return true
	}
	c, err := catalog.Builtin()
	if err != nil {
		return false
	}
	resolved, err := c.Resolve(ref)
	if err != nil {
		return false
	}
	return resolved.Spec.GetPlacement() != manifest.PlacementDaemon
}
