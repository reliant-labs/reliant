package preset

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// CreatePreset saves a new user preset to the database.
func (s *Service) CreatePreset(
	ctx context.Context,
	req *connect.Request[reliantv1.CreatePresetRequest],
) (*connect.Response[reliantv1.CreatePresetResponse], error) {
	if req.Msg.Name == "" {
		return nil, svcerr.InvalidArgument("preset name is required")
	}
	if req.Msg.Tag == "" {
		return nil, svcerr.InvalidArgument("preset tag is required")
	}

	userID := auth.MustGetUserID(ctx)

	// Generate slug from name
	slug := strings.ToLower(strings.ReplaceAll(req.Msg.Name, " ", "-"))

	// Validate slug (alphanumeric, dashes, underscores only)
	validSlug := regexp.MustCompile(`^[a-z0-9_-]+$`)
	if !validSlug.MatchString(slug) {
		activity.Skip(ctx) // refused: nothing changed, so no "create preset" event
		return connect.NewResponse(&reliantv1.CreatePresetResponse{
			Success: false,
			Error:   "preset name can only contain letters, numbers, dashes, and underscores",
		}), nil
	}

	// Check if preset already exists with this slug
	// Use project-scoped lookup if project_id is provided, otherwise global
	var existing *db.Preset
	if req.Msg.ProjectId != "" {
		existing, _ = s.deps.Repo.GetPresetBySlugAndProject(ctx, userID, slug, req.Msg.ProjectId)
	} else {
		existing, _ = s.deps.Repo.GetPresetBySlug(ctx, userID, slug)
	}
	if existing != nil {
		activity.Skip(ctx) // refused: nothing changed, so no "create preset" event
		return connect.NewResponse(&reliantv1.CreatePresetResponse{
			Success: false,
			Error:   fmt.Sprintf("preset with slug '%s' already exists", slug),
		}), nil
	}

	// Check against builtin presets
	builtinPath := "presets/" + slug + ".yaml"
	if _, err := builtin.BuiltinPresetsFS.ReadFile(builtinPath); err == nil {
		activity.Skip(ctx) // refused: nothing changed, so no "create preset" event
		return connect.NewResponse(&reliantv1.CreatePresetResponse{
			Success: false,
			Error:   fmt.Sprintf("preset name '%s' conflicts with a built-in preset; please choose a different name", req.Msg.Name),
		}), nil
	}

	// Check against stored project presets - project not found is ok, just skip the check
	if req.Msg.ProjectId != "" {
		if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
			return nil, err
		}
		presetsJSON, err := s.deps.Repo.GetProjectPresetsJSON(ctx, req.Msg.ProjectId)
		if err == nil {
			storedPresets, err := cfg.ParseStoredPresets(presetsJSON)
			if err == nil && cfg.FindStoredPresetByName(storedPresets, slug) != nil {
				activity.Skip(ctx) // refused: nothing changed, so no "create preset" event
				return connect.NewResponse(&reliantv1.CreatePresetResponse{
					Success: false,
					Error:   fmt.Sprintf("preset name '%s' conflicts with a project preset; please choose a different name", req.Msg.Name),
				}), nil
			}
		}
	}

	// Convert proto params to Go map
	params := make(map[string]interface{})
	for k, v := range req.Msg.Params {
		params[k] = structValueToGo(v)
	}

	// Determine project scope (nil = global)
	var projectID *string
	if req.Msg.ProjectId != "" {
		projectID = &req.Msg.ProjectId
	}

	// Create preset in database
	description := req.Msg.Description
	newPreset := &db.Preset{
		ID:          uuid.New().String(),
		UserID:      userID,
		ProjectID:   projectID,
		Name:        req.Msg.Name,
		Slug:        slug,
		Description: &description,
		Tag:         req.Msg.Tag,
		Params:      params,
	}

	saved, err := s.deps.Repo.CreatePreset(ctx, newPreset)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create preset"))
	}

	logging.InfoContext(ctx, "Created preset", "name", req.Msg.Name, "slug", slug, "user_id", userID)

	return connect.NewResponse(&reliantv1.CreatePresetResponse{
		Success: true,
		Preset:  dbPresetToProto(saved),
	}), nil
}
