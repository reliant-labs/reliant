// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/workersetup"
)

// temporalDrafter is TriggerService.DraftAutomation's drafter: it runs
// DraftAutomationWorkflow on the worker, where the model drivers are, and
// waits for the draft. Nothing it starts stores anything.
type temporalDrafter struct {
	client    client.Client
	taskQueue string
}

// automationDrafter is nil — never a typed nil in the handler's interface —
// on a deployment without Temporal, so DraftAutomation answers Unavailable.
func automationDrafter(c client.Client, taskQueue string) trigger.AutomationDrafter {
	if c == nil {
		return nil
	}
	return &temporalDrafter{client: c, taskQueue: taskQueue}
}

func (d *temporalDrafter) DraftAutomation(ctx context.Context, in trigger.DraftRequest) (*reliantv1.AutomationDraft, error) {
	run, err := d.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       "draft-automation-" + uuid.NewString(),
		TaskQueue:                d.taskQueue,
		WorkflowExecutionTimeout: 2 * time.Minute,
	}, workersetup.DraftAutomationWorkflowName, map[string]interface{}{
		"user_id":     in.UserID,
		"description": in.Description,
		"workflow":    in.Workflow,
		"timezone":    in.Timezone,
	})
	if err != nil {
		return nil, fmt.Errorf("start draft workflow: %w", err)
	}
	var out map[string]interface{}
	if err := run.Get(ctx, &out); err != nil {
		return nil, fmt.Errorf("draft workflow: %w", err)
	}
	raw, _ := out["draft"].(string)
	draft := &reliantv1.AutomationDraft{}
	if err := protojson.Unmarshal([]byte(raw), draft); err != nil {
		return nil, fmt.Errorf("decode draft: %w", err)
	}
	return draft, nil
}
