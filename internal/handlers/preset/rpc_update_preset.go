package preset

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// UpdatePreset updates an existing preset.
// For user presets (source=user): updates in database
// For project presets (source=project): updates the file in place
// Builtin presets cannot be edited.
func (s *Service) UpdatePreset(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdatePresetRequest],
) (*connect.Response[reliantv1.UpdatePresetResponse], error) {
	if req.Msg.Name == "" {
		return nil, svcerr.InvalidArgument("preset name/slug is required")
	}

	userID := auth.MustGetUserID(ctx)
	slug := strings.ToLower(strings.ReplaceAll(req.Msg.Name, " ", "-"))

	if req.Msg.ProjectId != "" {
		if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
			return nil, err
		}
	}

	// 1. First check if this is a user preset (database)
	var dbPreset *db.Preset
	var err error
	if req.Msg.ProjectId != "" {
		dbPreset, err = s.deps.Repo.GetPresetBySlugAndProject(ctx, userID, slug, req.Msg.ProjectId)
	} else {
		dbPreset, err = s.deps.Repo.GetPresetBySlug(ctx, userID, slug)
	}

	if err == nil && dbPreset != nil {
		// Found in database - update there
		if req.Msg.NewName != nil && *req.Msg.NewName != "" {
			dbPreset.Name = *req.Msg.NewName
		}
		if req.Msg.NewDescription != nil {
			dbPreset.Description = req.Msg.NewDescription
		}
		if req.Msg.NewTag != nil {
			dbPreset.Tag = *req.Msg.NewTag
		}
		if len(req.Msg.NewParams) > 0 {
			params := make(map[string]interface{})
			for k, v := range req.Msg.NewParams {
				params[k] = structValueToGo(v)
			}
			dbPreset.Params = params
		}

		updated, err := s.deps.Repo.UpdatePreset(ctx, dbPreset)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update preset"))
		}

		logging.InfoContext(ctx, "Updated user preset", "slug", dbPreset.Slug, "name", dbPreset.Name, "user_id", userID)
		return connect.NewResponse(&reliantv1.UpdatePresetResponse{
			Success: true,
			Preset:  dbPresetToProto(updated),
		}), nil
	}

	// 2. Not in database - check if it's a stored project preset or builtin
	filePreset, err := s.loadPresetByNameFromDB(ctx, req.Msg.ProjectId, req.Msg.Name)
	if err != nil {
		activity.Skip(ctx) // refused: nothing changed, so no "update preset" event
		return connect.NewResponse(&reliantv1.UpdatePresetResponse{
			Success: false,
			Error:   fmt.Sprintf("preset '%s' not found", req.Msg.Name),
		}), nil
	}

	// Builtin and project presets cannot be edited directly (they're read-only on the server)
	if filePreset.Source == "builtin" {
		activity.Skip(ctx) // refused: nothing changed, so no "update preset" event
		return connect.NewResponse(&reliantv1.UpdatePresetResponse{
			Success: false,
			Error:   "builtin presets cannot be edited",
		}), nil
	}
	if filePreset.Source == "project" {
		activity.Skip(ctx) // refused: nothing changed, so no "update preset" event
		return connect.NewResponse(&reliantv1.UpdatePresetResponse{
			Success: false,
			Error:   "project presets cannot be edited from the server; edit the YAML file directly",
		}), nil
	}

	activity.Skip(ctx) // refused: nothing changed, so no "update preset" event
	return connect.NewResponse(&reliantv1.UpdatePresetResponse{
		Success: false,
		Error:   fmt.Sprintf("preset '%s' not found", req.Msg.Name),
	}), nil
}
