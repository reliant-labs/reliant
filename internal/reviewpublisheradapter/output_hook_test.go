package reviewpublisheradapter

import (
	"context"
	"strings"
	"testing"

	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestPrivateReviewHookOutputExpressionBoundary(t *testing.T) {
	for _, kind := range []string{"changes", "approve", "invalid verdict", "false metadata", "load-time template", "different graph"} {
		t.Run(kind, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			loaded := runtime.LoadedWorkflow{YAML: []byte(reviewOutputYAML), WorkflowJSON: reviewGraphJSON(t, reviewOutputYAML), HasTemplates: true}
			verdict := kind
			switch kind {
			case "false metadata":
				loaded.HasTemplates = false
			case "load-time template":
				loaded.YAML = []byte(strings.Replace(reviewOutputYAML, "name: trusted", "name: '{{ inputs.name }}'", 1))
			case "different graph":
				loaded.WorkflowJSON = reviewGraphJSON(t, strings.ReplaceAll(reviewOutputYAML, "nodes.review.response_text", "inputs.verdict"))
			}
			resolved, completed := 0, 0
			env.RegisterActivityWithOptions(func(context.Context) (string, error) {
				resolved++
				return "assignment", nil
			}, activity.RegisterOptions{Name: ResolveAssignmentActivityName})
			env.RegisterActivityWithOptions(func(ctx context.Context, input CompletionInput) error {
				completed++
				require.Equal(t, "private-review-completion", activity.GetInfo(ctx).ActivityID)
				require.Equal(t, "assignment", input.AssignmentID)
				require.Equal(t, ReviewCompletionPath, input.Runtime.NodePath)
				require.Equal(t, ReviewCompletionPath, input.Runtime.StepID)
				require.Equal(t, verdict, string(input.Result.Verdict))
				return nil
			}, activity.RegisterOptions{Name: CompletionActivityName})
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				return privateReviewAfterGraph(ctx, runtime.GraphCompletion{Loaded: loaded, Outputs: map[string]interface{}{"review_verdict": verdict}})
			})
			if kind == "changes" || kind == "approve" {
				require.NoError(t, env.GetWorkflowError())
				require.Equal(t, 1, resolved)
				require.Equal(t, 1, completed)
			} else {
				require.Error(t, env.GetWorkflowError())
				require.Zero(t, resolved)
				require.Zero(t, completed)
			}
		})
	}
}
