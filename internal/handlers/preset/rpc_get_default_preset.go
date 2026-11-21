package preset

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// GetDefaultPreset retrieves all default presets for a workflow.
// Returns a map of group name to preset name.
// Empty string key "" represents top-level/workflow-level inputs.
// Merges user settings with system defaults (user settings take precedence).
func (s *Service) GetDefaultPreset(
	ctx context.Context,
	req *connect.Request[reliantv1.GetDefaultPresetRequest],
) (*connect.Response[reliantv1.GetDefaultPresetResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if req.Msg.WorkflowName == "" {
		return nil, svcerr.InvalidArgument("workflow_name is required")
	}

	userIDStr := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userIDStr); err != nil {
		return nil, err
	}

	defaults := s.resolveDefaultPresets(ctx, userIDStr, req.Msg.ProjectId, req.Msg.WorkflowName, nil,
		func(ref string) (*reliantv1.Workflow, error) { return s.loadWorkflow(ctx, ref, req.Msg.ProjectId) })

	return connect.NewResponse(&reliantv1.GetDefaultPresetResponse{
		Presets: defaults,
	}), nil
}
