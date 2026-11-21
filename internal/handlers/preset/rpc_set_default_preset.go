package preset

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// SetDefaultPreset marks a preset as the default for a specific group within a workflow.
// Defaults are stored as a JSON blob per workflow: preset.defaults.<workflowName>
// The blob maps group names to preset names: {"": "general", "Proposer": "researcher"}
// Empty string key "" represents top-level/workflow-level inputs.
func (s *Service) SetDefaultPreset(
	ctx context.Context,
	req *connect.Request[reliantv1.SetDefaultPresetRequest],
) (*connect.Response[reliantv1.SetDefaultPresetResponse], error) {
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

	// Get group name (empty string = top-level)
	groupName := ""
	if req.Msg.GroupName != nil {
		groupName = *req.Msg.GroupName
	}

	presetName := ""
	if req.Msg.PresetName != nil {
		presetName = *req.Msg.PresetName
	}

	// Settings key for this workflow's defaults blob
	settingKey := fmt.Sprintf("preset.defaults.%s", req.Msg.WorkflowName)

	// Load existing defaults blob
	defaults := make(map[string]string)
	setting, err := s.deps.Repo.GetSetting(ctx, userIDStr, nil, settingKey)
	if err == nil && setting.Value != "" {
		// Parse existing JSON blob
		if err := json.Unmarshal([]byte(setting.Value), &defaults); err != nil {
			logging.WarnContext(ctx, "Failed to parse existing defaults blob", "key", settingKey, "error", err)
			// Start fresh on parse error
			defaults = make(map[string]string)
		}
	}

	// Update the entry for this group
	if presetName == "" {
		delete(defaults, groupName)
	} else {
		defaults[groupName] = presetName
	}

	// Serialize back to JSON
	blobBytes, err := json.Marshal(defaults)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to serialize defaults"))
	}

	// Upsert the setting
	if err := s.upsertSetting(ctx, userIDStr, settingKey, string(blobBytes)); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to set default preset"))
	}

	logging.InfoContext(ctx, "Set default preset", "workflow", req.Msg.WorkflowName, "group", groupName, "preset", presetName)

	return connect.NewResponse(&reliantv1.SetDefaultPresetResponse{
		Success: true,
	}), nil
}
