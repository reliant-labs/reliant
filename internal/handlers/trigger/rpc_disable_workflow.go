package trigger

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// DisableWorkflow pauses every activation of a workflow.
func (s *Service) DisableWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.DisableWorkflowRequest],
) (*connect.Response[reliantv1.DisableWorkflowResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if strings.TrimSpace(req.Msg.Workflow) == "" {
		return nil, svcerr.InvalidArgument("workflow is required")
	}
	mine, err := s.deps.Triggers.ListTriggersForUser(ctx, core.TriggerFilters{UserID: userID, ProjectID: req.Msg.ProjectId})
	if err != nil {
		return nil, triggerDBError("list triggers", err)
	}
	resp := &reliantv1.DisableWorkflowResponse{}
	// With no project named this is every project's: only activations the
	// caller may still change are paused or reported.
	for _, t := range authz.Visible(ctx, userID, mine, authz.CanWriteTrigger) {
		if t.WorkflowTrigger == nil || !sameWorkflowRef(t.Workflow, req.Msg.Workflow) {
			continue
		}
		if !t.Enabled {
			resp.Paused = append(resp.Paused, s.render(ctx, t))
			continue
		}
		paused, err := s.SetTriggerEnabled(ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: t.ID, Enabled: false}))
		if err != nil {
			resp.Failed = append(resp.Failed, &reliantv1.TriggerFailure{TriggerId: t.ID, Name: t.Name, Message: connectMessage(err)})
			continue
		}
		resp.Paused = append(resp.Paused, paused.Msg.GetTrigger())
	}
	return connect.NewResponse(resp), nil
}
