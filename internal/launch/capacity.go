package launch

import (
	"context"
	"errors"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

// ExecutionDescriber deliberately uses the existing Temporal boundary. An
// admitted start's memo recovers an accepted-but-unacknowledged execution.
var errAdmissionSuperseded = errors.New("admitted execution was superseded")

type ExecutionDescriber interface {
	DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
}

func (l *Launcher) recoverAdmission(ctx context.Context, eventID, workflowID string, spec Spec) error {
	if spec.RecordExecution == nil {
		return nil
	}
	reader, ok := l.temporal.(ExecutionDescriber)
	if !ok {
		return errors.New("admitted launch requires Temporal execution descriptions")
	}
	desc, err := reader.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		return err
	}
	info := desc.GetWorkflowExecutionInfo()
	if info == nil || info.GetExecution().GetWorkflowId() != workflowID || info.GetFirstRunId() == "" {
		return errors.New("missing admitted execution identity")
	}
	var memoEvent string
	payload := info.GetMemo().GetFields()["trigger_event_id"]
	if payload == nil || converter.GetDefaultDataConverter().FromPayload(payload, &memoEvent) != nil || memoEvent != eventID {
		return errAdmissionSuperseded
	}
	return spec.RecordExecution(ctx, eventID, workflowID, info.GetFirstRunId())
}

var _ ExecutionDescriber = (client.Client)(nil)
