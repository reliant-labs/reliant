// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// titleLLMAttempts is how many times the LLM is asked for a title before the
// workflow settles for the truncated first message. Title generation is a
// single cheap call on a fast model, and a provider blip is the common failure,
// so a few spaced retries recover most of them.
const titleLLMAttempts = 3

// GenerateTitleWorkflow generates a chat title, retrying the LLM before
// settling for a deterministic fallback.
//
// The fallback (truncated first message) is a LAST RESORT, not an error
// handler. It used to live inside the activity, which returned success after
// substituting it — so a provider outage produced a chat titled with the user's
// own text and no retry, no alert, and no way to tell the two apart. Keeping
// the decision here means the retries actually happen, and the one case where
// we give up is explicit and logged.
func GenerateTitleWorkflow(ctx workflow.Context, input map[string]interface{}) error {
	logger := workflow.GetLogger(ctx)

	chatID, _ := input["chat_id"].(string)
	logger.Info("GenerateTitleWorkflow started", "chatID", chatID)

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    10 * time.Second,
			MaximumAttempts:    titleLLMAttempts,
		},
	})

	var output map[string]interface{}
	err := workflow.ExecuteActivity(ctx, "GenerateTitle", input).Get(ctx, &output)
	if err == nil {
		logger.Info("GenerateTitleWorkflow completed successfully", "chatID", chatID)
		return nil
	}

	// Never leave a chat untitled: the activity returns early once a title is
	// set, so this is the only chance to write one.
	logger.Error("GenerateTitle failed after retries, falling back to truncated first message",
		"error", err, "chatID", chatID, "attempts", titleLLMAttempts)

	fallbackInput := make(map[string]interface{}, len(input)+1)
	for k, v := range input {
		fallbackInput[k] = v
	}
	fallbackInput["use_fallback"] = true

	// One attempt: the fallback is pure string manipulation, so a failure here
	// is a DB problem that a retry in this workflow will not fix.
	fallbackCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		WaitForCancellation: true,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	if err := workflow.ExecuteActivity(fallbackCtx, "GenerateTitle", fallbackInput).Get(fallbackCtx, &output); err != nil {
		logger.Error("GenerateTitle fallback failed; chat will remain untitled",
			"error", err, "chatID", chatID)
		return err
	}

	logger.Info("GenerateTitleWorkflow completed with fallback title", "chatID", chatID)
	return nil
}
