package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestDynamicWorkflowAskQuestionDoesNotScheduleUnregisteredActivity(t *testing.T) {
	t.Parallel()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	metadata := `{"questions":[{"question":"Proceed?","options":["Continue"]}]}`
	workflowBytes, err := protojson.Marshal(&reliantv1.Workflow{
		Name:  "ask-question-workflow",
		Entry: []string{"ask"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "ask",
				Type: model.NodeTypeAskQuestion,
				Args: &reliantv1.Node_AskQuestion{AskQuestion: &reliantv1.AskQuestionArgs{
					Metadata: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: metadata}},
				}},
			},
		},
		Outputs: map[string]string{
			"response": "{{nodes.ask.response}}",
		},
	})
	require.NoError(t, err)

	var scheduledActivityNames []string
	env.SetOnActivityStartedListener(func(activityInfo *activity.Info, _ context.Context, _ converter.EncodedValues) {
		scheduledActivityNames = append(scheduledActivityNames, activityInfo.ActivityType.Name)
	})

	var questionID string
	env.RegisterActivityWithOptions(
		func(_ context.Context, input json.RawMessage) (map[string]interface{}, error) {
			var payload map[string]interface{}
			require.NoError(t, json.Unmarshal(input, &payload))
			require.Equal(t, metadata, payload["metadata"])
			questionID = "question-123"
			return map[string]interface{}{
				"question_id":      questionID,
				"already_resolved": false,
				"response_data":    "",
			}, nil
		},
		activity.RegisterOptions{Name: "QuestionCreate"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: workflowBytes}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return nil, nil
		},
		activity.RegisterOptions{Name: "WorkflowStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowCheckpoint"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "Cleanup"},
	)

	env.RegisterDelayedCallback(func() {
		require.NotEmpty(t, questionID)
		env.SignalWorkflow("signal.question."+questionID, map[string]interface{}{
			"action":        "reply",
			"response_data": `{"answers":[{"question":"Proceed?","selected":[],"freetext":"Looks good"}]}`,
		})
	}, time.Second)

	env.ExecuteWorkflow(DynamicWorkflow, WorkflowInput{
		ChatID:       "chat-ask",
		WorkflowName: "ask-question-workflow",
		Inputs:       map[string]interface{}{},
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-ignored",
			ChatID:       "chat-ask",
			Thread:       "thread-ask",
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "ask-question-workflow",
		},
	})

	require.NoError(t, env.GetWorkflowError())
	require.NotContains(t, scheduledActivityNames, "AskQuestion")
	require.Contains(t, scheduledActivityNames, "QuestionCreate")

	var result WorkflowResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, "Looks good", result.Outputs["response"])
}
