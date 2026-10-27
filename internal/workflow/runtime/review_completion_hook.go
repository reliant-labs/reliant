package runtime

import "go.temporal.io/sdk/workflow"

// WorkflowHooks are server-owned deterministic lifecycle hooks, supplied only by
// private worker registration. They are not YAML, workflow input or agent tools.
// The public DynamicWorkflow always uses an empty value. Hooks must not capture
// mutable per-run authority: resolve it in an activity from exact runtime identity.
type WorkflowHooks struct {
	AfterGraph func(workflow.Context, GraphCompletion) error
}

// GraphCompletion contains settled engine outputs and outcome. It is pending
// evidence only: the workflow has not yet executed terminal status or closed.
type GraphCompletion struct {
	WorkflowName string
	Loaded       LoadedWorkflow
	Outputs      map[string]interface{}
	Outcome      string
}

// NewDynamicWorkflowWithHooks constructs a private worker workflow implementation.
// Policy must pin its registered workflow type as well as immutable graph inputs.
// A hook failure fails the workflow; no completed-only or early success is implied.
func NewDynamicWorkflowWithHooks(hooks WorkflowHooks) func(workflow.Context, WorkflowInput) (*WorkflowResult, error) {
	return func(ctx workflow.Context, in WorkflowInput) (*WorkflowResult, error) {
		return dynamicWorkflowWithHooks(ctx, in, hooks)
	}
}
