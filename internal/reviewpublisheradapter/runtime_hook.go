package reviewpublisheradapter

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

// ResolveAssignmentActivityName is private worker registration, not a model action.
const ResolveAssignmentActivityName = "ActivityPrivateReviewAssignment"

// ResolveAssignmentActivity loads an assignment only by authenticated runtime
// execution, never a YAML/model-provided assignment or current chat pointer.
type ResolveAssignmentActivity struct {
	Store   store.ReviewAuthorityStore
	History TemporalEvidence
}

// Execute uses actual activity context and returns only assignment identity.
func (a ResolveAssignmentActivity) Execute(ctx context.Context) (string, error) {
	if !activity.IsActivity(ctx) {
		return "", reviewpublisher.ErrUnauthorized
	}
	info := activity.GetInfo(ctx)
	assignment, err := a.Store.AssignmentForRoot(ctx, info.WorkflowExecution.ID)
	if err != nil {
		return "", err
	}
	if err = a.History.VerifyRecipient(ctx, assignment, info.WorkflowExecution.RunID); err != nil {
		return "", err
	}
	return assignment.ID, nil
}

// VerifyRecipient accepts only the shared proof's exact current execution from
// the immutable assignment original. Reset/retry/follow-up/latest fallbacks are
// never accepted; full graph/digest evidence is still checked by completion.
func (h TemporalEvidence) VerifyRecipient(ctx context.Context, a store.ReviewAuthority, actualRun string) error {
	if h.Service == nil || h.Namespace == "" || actualRun == "" {
		return reviewpublisher.ErrUnauthorized
	}
	proof, err := runcontinuity.Resolve(ctx, runcontinuity.TemporalReader{Service: h.Service, Namespace: h.Namespace}, runcontinuity.Execution{WorkflowID: a.Binding.Reviewer.RootID, RunID: a.Binding.Reviewer.RunID})
	if err != nil {
		return err
	}
	if !proof.Live || proof.Current.RunID != actualRun || proof.Current.WorkflowID != a.Binding.Reviewer.RootID {
		return reviewpublisher.ErrUnauthorized
	}
	return nil
}

// PrivateReviewWorkflow is registered explicitly under the policy-pinned private
// workflow type. The ordinary public runtime.DynamicWorkflow has no hook.
// No mutable per-run assignment is captured; a server activity resolves it.
func PrivateReviewWorkflow(ctx workflow.Context, in runtime.WorkflowInput) (*runtime.WorkflowResult, error) {
	return runtime.NewDynamicWorkflowWithHooks(runtime.WorkflowHooks{AfterGraph: privateReviewAfterGraph})(ctx, in)
}

func privateReviewAfterGraph(ctx workflow.Context, done runtime.GraphCompletion) error {
	if err := validateReviewOutputExpressions(done.Loaded.YAML, done.Loaded.WorkflowJSON, done.Loaded.HasTemplates); err != nil {
		return err
	}
	verdict, ok := done.Outputs["review_verdict"].(string)
	if !ok || !validVerdict(reviewpublisher.Verdict(verdict)) {
		return reviewpublisher.ErrUnauthorized
	}
	options := workflow.ActivityOptions{StartToCloseTimeout: 15 * time.Second}
	activityCtx := workflow.WithActivityOptions(ctx, options)
	var assignmentID string
	if err := workflow.ExecuteActivity(activityCtx, ResolveAssignmentActivityName).Get(ctx, &assignmentID); err != nil {
		return err
	}
	input := CompletionInput{AssignmentID: assignmentID, Runtime: types.RuntimeContext{WorkflowID: workflow.GetInfo(ctx).WorkflowExecution.ID, StepID: ReviewCompletionPath, NodePath: ReviewCompletionPath}, Result: GraphResult{Verdict: reviewpublisher.Verdict(verdict)}}
	options.ActivityID = "private-review-completion"
	return workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, options), CompletionActivityName, input).Get(ctx, nil)
}
