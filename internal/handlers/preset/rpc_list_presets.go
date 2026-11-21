package preset

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ListPresets returns all presets for a project.
// Priority: user (database) > project (stored config) > builtin
func (s *Service) ListPresets(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPresetsRequest],
) (*connect.Response[reliantv1.ListPresetsResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// Use a map to deduplicate by slug (user presets take priority)
	presetsBySlug := make(map[string]*reliantv1.PresetInfo)
	var invalidPresets []*reliantv1.InvalidPreset

	// 1. Load builtin + stored project presets from DB
	loadResult := s.loadAllPresetsFromDB(ctx, req.Msg.ProjectId)
	logging.DebugContext(ctx, "Loaded presets from DB", "project_id", req.Msg.ProjectId, "valid", len(loadResult.Valid), "invalid", len(loadResult.Invalid))
	for _, p := range loadResult.Valid {
		logging.DebugContext(ctx, "Preset", "name", p.Name, "source", p.Source)
		protoPreset, err := presetToProto(p)
		if err != nil {
			continue
		}
		presetsBySlug[p.Name] = protoPreset
	}

	// Convert invalid presets to proto format
	for _, inv := range loadResult.Invalid {
		invalidPresets = append(invalidPresets, &reliantv1.InvalidPreset{
			Name:   inv.Name,
			Source: inv.Source,
			Path:   inv.Path,
			Errors: inv.Errors,
		})
	}

	// 2. Load user presets from database (highest priority, override file-based)
	dbPresets, err := s.deps.Repo.ListUserPresetsByProject(ctx, userID, req.Msg.ProjectId)
	if err != nil {
		logging.WarnContext(ctx, "Failed to load user presets from database", "error", err)
	} else {
		for _, dbPreset := range dbPresets {
			protoPreset := dbPresetToProto(dbPreset)
			presetsBySlug[dbPreset.Slug] = protoPreset
		}
	}

	// Batch-fetch visibility state (2 queries instead of 2×N)
	presetItemType := int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET)
	overrides, err := s.deps.Repo.ListVisibilityOverrides(ctx, userID, presetItemType)
	if err != nil {
		logging.WarnContext(ctx, "Failed to batch-load visibility overrides for presets", "error", err)
		overrides = nil
	}
	hiddenDefaults, err := s.deps.Repo.ListHiddenItemDefaults(ctx, presetItemType)
	if err != nil {
		logging.WarnContext(ctx, "Failed to batch-load hidden defaults for presets", "error", err)
		hiddenDefaults = nil
	}
	hiddenDefaultSet := make(map[string]bool, len(hiddenDefaults))
	for _, slug := range hiddenDefaults {
		hiddenDefaultSet[slug] = true
	}

	// Set is_hidden and filter by visibility (unless include_hidden is set)
	protoPresets := make([]*reliantv1.PresetInfo, 0, len(presetsBySlug))
	for _, p := range presetsBySlug {
		visible, ok := overrides[p.Slug]
		if !ok {
			visible = !hiddenDefaultSet[p.Slug]
		}
		p.IsHidden = !visible

		if req.Msg.IncludeHidden || visible {
			protoPresets = append(protoPresets, p)
		}
	}

	return connect.NewResponse(&reliantv1.ListPresetsResponse{
		Presets:        protoPresets,
		InvalidPresets: invalidPresets,
	}), nil
}
