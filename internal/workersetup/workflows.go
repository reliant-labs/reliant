// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

// GenerateTitleWorkflow is a thin inline wrapper around the GenerateTitle activity.
// This provides async execution, retries, and observability.
func GenerateTitleWorkflow(ctx workflow.Context, input map[string]interface{}) error {
	logger := workflow.GetLogger(ctx)

	chatID, _ := input["chat_id"].(string)
	logger.Info("GenerateTitleWorkflow started", "chatID", chatID)

	activityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		WaitForCancellation: true,
	}
	ctx = workflow.WithActivityOptions(ctx, activityOptions)

	var output map[string]interface{}
	err := workflow.ExecuteActivity(ctx, "GenerateTitle", input).Get(ctx, &output)
	if err != nil {
		logger.Error("GenerateTitle activity failed", "error", err, "chatID", chatID)
		return err
	}

	logger.Info("GenerateTitleWorkflow completed successfully", "chatID", chatID)
	return nil
}
