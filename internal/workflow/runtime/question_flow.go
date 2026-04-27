package runtime

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type askQuestionExecution struct {
	ChatID        string
	WorkflowID    string
	ThreadID      string
	StepID        string
	LoopNodeID    string
	LoopIteration int
	Metadata      string
	Logger        log.Logger
}

func executeAskQuestionSignalFlow(ctx workflow.Context, input askQuestionExecution) (map[string]interface{}, error) {
	const questionTimeout = 24 * time.Hour

	temporalWorkflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	createCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	questionInput := map[string]interface{}{
		"chat_id":              input.ChatID,
		"workflow_id":          input.WorkflowID,
		"temporal_workflow_id": temporalWorkflowID,
		"thread_id":            input.ThreadID,
		"step_id":              input.StepID,
		"loop_node_id":         input.LoopNodeID,
		"loop_iteration":       input.LoopIteration,
		"metadata":             input.Metadata,
	}

	var createOutput struct {
		QuestionID      string `json:"question_id"`
		AlreadyResolved bool   `json:"already_resolved"`
		ResponseData    string `json:"response_data"`
	}
	if err := workflow.ExecuteActivity(createCtx, "QuestionCreate", questionInput).Get(ctx, &createOutput); err != nil {
		return nil, fmt.Errorf("QuestionCreate failed: %w", err)
	}

	if createOutput.AlreadyResolved {
		input.Logger.Info("[AskQuestion] Already resolved (replay)", "stepID", input.StepID, "questionID", createOutput.QuestionID)
		return parseQuestionResponse(createOutput.ResponseData), nil
	}

	signalName := "signal.question." + createOutput.QuestionID
	signalCh := workflow.GetSignalChannel(ctx, signalName)
	timeoutCtx, cancelTimer := workflow.WithCancel(ctx)
	timeoutFuture := workflow.NewTimer(timeoutCtx, questionTimeout)

	selector := workflow.NewSelector(ctx)
	var responseData string
	var actionTaken string

	selector.AddReceive(signalCh, func(ch workflow.ReceiveChannel, more bool) {
		var signalData map[string]interface{}
		ch.Receive(ctx, &signalData)
		if action, ok := signalData["action"].(string); ok {
			actionTaken = action
		}
		if data, ok := signalData["response_data"].(string); ok {
			responseData = data
		}
		cancelTimer()
	})

	selector.AddFuture(timeoutFuture, func(f workflow.Future) {
		actionTaken = "timeout"
	})

	selector.Select(ctx)

	if actionTaken == "timeout" {
		resolveCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 30 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				MaximumAttempts: 3,
			},
		})
		var resolveOutput map[string]interface{}
		if err := workflow.ExecuteActivity(resolveCtx, "QuestionResolve", map[string]interface{}{
			"question_id":   createOutput.QuestionID,
			"response_data": "",
		}).Get(ctx, &resolveOutput); err != nil {
			input.Logger.Warn("[AskQuestion] Failed to resolve question as timeout in DB",
				"stepID", input.StepID,
				"questionID", createOutput.QuestionID,
				"error", err,
			)
		}
	}

	return parseQuestionResponse(responseData), nil
}
