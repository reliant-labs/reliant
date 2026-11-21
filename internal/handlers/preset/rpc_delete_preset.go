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

// DeletePreset deletes a preset.
// For user presets (source=user): deletes from database
// For project presets (source=project): deletes the file
// Builtin presets cannot be deleted.
func (s *Service) DeletePreset(
	ctx context.Context,
	req *connect.Request[reliantv1.DeletePresetRequest],
) (*connect.Response[reliantv1.DeletePresetResponse], error) {
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
	if req.Msg.ProjectId != "" {
		dbPreset, _ = s.deps.Repo.GetPresetBySlugAndProject(ctx, userID, slug, req.Msg.ProjectId)
	} else {
		dbPreset, _ = s.deps.Repo.GetPresetBySlug(ctx, userID, slug)
	}

	if dbPreset != nil {
		// Found in database - delete there
		if err := s.deps.Repo.DeletePreset(ctx, dbPreset.ID); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete preset"))
		}
		logging.InfoContext(ctx, "Deleted user preset", "slug", slug, "user_id", userID)
		return connect.NewResponse(&reliantv1.DeletePresetResponse{
			Success: true,
			Id:      dbPreset.ID,
		}), nil
	}

	// 2. Not in database - check if it's a stored project preset or builtin
	filePreset, err := s.loadPresetByNameFromDB(ctx, req.Msg.ProjectId, req.Msg.Name)
	if err != nil {
		activity.Skip(ctx) // refused: nothing changed, so no "delete preset" event
		return connect.NewResponse(&reliantv1.DeletePresetResponse{
			Success: false,
			Error:   fmt.Sprintf("preset '%s' not found", req.Msg.Name),
		}), nil
	}

	// Builtin presets cannot be deleted
	if filePreset.Source == "builtin" {
		activity.Skip(ctx) // refused: nothing changed, so no "delete preset" event
		return connect.NewResponse(&reliantv1.DeletePresetResponse{
			Success: false,
			Error:   "builtin presets cannot be deleted",
		}), nil
	}

	// Project presets cannot be deleted from the server (they're synced from filesystem by daemon)
	if filePreset.Source == "project" {
		activity.Skip(ctx) // refused: nothing changed, so no "delete preset" event
		return connect.NewResponse(&reliantv1.DeletePresetResponse{
			Success: false,
			Error:   "project presets cannot be deleted from the server; delete the YAML file directly",
		}), nil
	}

	activity.Skip(ctx) // refused: nothing changed, so no "delete preset" event
	return connect.NewResponse(&reliantv1.DeletePresetResponse{
		Success: false,
		Error:   fmt.Sprintf("preset '%s' not found", req.Msg.Name),
	}), nil
}
