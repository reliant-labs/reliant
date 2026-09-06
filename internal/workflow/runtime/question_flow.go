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
	// NodePath is the fully-qualified dotted graph position of the
	// ask_question node. Observability only; never written to loop_node_id.
	NodePath   string
	Metadata   string
	Unattended bool
	Logger     log.Logger
}

// autoResolveUnattendedQuestion is what an ask_question node returns when the run
// has no human in it. It selects NOTHING: the shape is the same "no answer came
// back" shape the 24h timeout already produces, reached in zero seconds and
// labelled with the real reason instead of "the user did not respond in time".
//
// Selecting an option — even the recommended one — would put words in a person's
// mouth, and for get-it-right's stuck_checkpoint it would also be wrong: that
// loop's `while` re-enters unbounded on has_feedback == true, so a fabricated
// "I fixed it, continue" retries a broken environment forever. No answer means
// `stuck && !has_feedback`, which exits the loop with strategy=stuck — the
// correct unattended outcome for a problem outside the codebase.
//
// auto_resolved/resolved_by are what make this distinguishable in the record: a
// human reply goes through parseQuestionResponse, which never sets either.
func autoResolveUnattendedQuestion() map[string]interface{} {
	return map[string]interface{}{
		"has_feedback":  false,
		"response":      "",
		"auto_resolved": true,
		"resolved_by":   UnattendedResolver,
		"record": UnattendedMarker + " no human is available in this run — this checkpoint " +
			"was resolved with NO answer. No option was selected on anyone's behalf.",
	}
}

func executeAskQuestionSignalFlow(ctx workflow.Context, input askQuestionExecution) (map[string]interface{}, error) {
	const questionTimeout = 24 * time.Hour

	// Unattended: never create the questions row, never wait. There is nobody to
	// ask, so a pending row would only be a gate that can never be lifted.
	if input.Unattended {
		input.Logger.Info("[Unattended] ask_question auto-resolved with no answer",
			"stepID", input.StepID,
			"loopNodeID", input.LoopNodeID,
			"loopIteration", input.LoopIteration,
			"metadata", input.Metadata,
		)
		return autoResolveUnattendedQuestion(), nil
	}

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
		"node_path":            input.NodePath,
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
