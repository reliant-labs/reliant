// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// PresetService implements the gRPC PresetService
type PresetService struct {
	reliantv1connect.UnimplementedPresetServiceHandler
	database db.Repository
}

// NewPresetService creates a new PresetService
func NewPresetService(database db.Repository) *PresetService {
	return &PresetService{
		database: database,
	}
}

// ListPresets returns all presets for a project.
// Priority: user (database) > project (stored config) > builtin
func (s *PresetService) ListPresets(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPresetsRequest],
) (*connect.Response[reliantv1.ListPresetsResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userID := auth.MustGetUserID(ctx)

	// Use a map to deduplicate by slug (user presets take priority)
	presetsBySlug := make(map[string]*reliantv1.PresetInfo)
	var invalidPresets []*reliantv1.InvalidPreset

	// 1. Load builtin + stored project presets from DB
	loadResult := s.loadAllPresetsFromDB(ctx, req.Msg.ProjectId)
	logging.Debug("Loaded presets from DB", "project_id", req.Msg.ProjectId, "valid", len(loadResult.Valid), "invalid", len(loadResult.Invalid))
	for _, p := range loadResult.Valid {
		logging.Debug("Preset", "name", p.Name, "source", p.Source)
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
	dbPresets, err := s.database.ListUserPresetsByProject(ctx, userID, req.Msg.ProjectId)
	if err != nil {
		logging.Warn("Failed to load user presets from database", "error", err)
	} else {
		for _, dbPreset := range dbPresets {
			protoPreset := dbPresetToProto(dbPreset)
			presetsBySlug[dbPreset.Slug] = protoPreset
		}
	}

	// Batch-fetch visibility state (2 queries instead of 2×N)
	presetItemType := int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET)
	overrides, err := s.database.ListVisibilityOverrides(ctx, userID, presetItemType)
	if err != nil {
		logging.Warn("Failed to batch-load visibility overrides for presets", "error", err)
		overrides = nil
	}
	hiddenDefaults, err := s.database.ListHiddenItemDefaults(ctx, presetItemType)
	if err != nil {
		logging.Warn("Failed to batch-load hidden defaults for presets", "error", err)
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

// GetPreset returns a specific preset by name.
// Priority: user (database) > project (stored config) > builtin
func (s *PresetService) GetPreset(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPresetRequest],
) (*connect.Response[reliantv1.GetPresetResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("preset name is required"))
	}

	userID := auth.MustGetUserID(ctx)

	// Generate slug from name for database lookup
	slug := strings.ToLower(strings.ReplaceAll(req.Msg.Name, " ", "-"))

	// 1. First check database for user preset (highest priority)
	dbPreset, err := s.database.GetPresetBySlug(ctx, userID, slug)
	if err == nil && dbPreset != nil {
		return connect.NewResponse(&reliantv1.GetPresetResponse{
			Preset: dbPresetToProto(dbPreset),
		}), nil
	}

	// 2. Fall back to stored project presets / builtins
	p, err := s.loadPresetByNameFromDB(ctx, req.Msg.ProjectId, req.Msg.Name)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("preset not found: %w", err))
	}

	protoPreset, err := presetToProto(p)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to convert preset: %w", err))
	}

	return connect.NewResponse(&reliantv1.GetPresetResponse{
		Preset: protoPreset,
	}), nil
}

// ListPresetsForWorkflow returns presets compatible with a specific workflow.
// Priority: user (database) > project (stored config) > builtin
// Presets are matched by tag - a preset with tag "agent" will match any
// workflow/group with tag "agent".
func (s *PresetService) ListPresetsForWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPresetsForWorkflowRequest],
) (*connect.Response[reliantv1.ListPresetsForWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if req.Msg.WorkflowName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow_name is required"))
	}

	userID := auth.MustGetUserID(ctx)

	// Load workflow to check compatibility
	wf, err := s.loadWorkflow(ctx, req.Msg.WorkflowName, req.Msg.ProjectId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %w", err))
	}

	// Use a map to deduplicate by slug (user presets take priority)
	presetsBySlug := make(map[string]*reliantv1.PresetInfo)
	var invalidPresets []*reliantv1.InvalidPreset

	// 1. Load all builtin + stored project presets, then filter by workflow compatibility
	loadResult := s.loadAllPresetsFromDB(ctx, req.Msg.ProjectId)

	// Convert invalid presets to proto format
	for _, inv := range loadResult.Invalid {
		invalidPresets = append(invalidPresets, &reliantv1.InvalidPreset{
			Name:   inv.Name,
			Source: inv.Source,
			Path:   inv.Path,
			Errors: inv.Errors,
		})
	}

	// Filter valid presets by workflow compatibility
	for _, p := range loadResult.Valid {
		result := preset.ValidatePreset(p, wf)
		if !result.Valid {
			continue // Not compatible with this workflow
		}
		protoPreset, err := presetToProto(p)
		if err != nil {
			logging.Warn("Failed to convert preset to proto", "preset", p.Name, "error", err)
			continue
		}
		presetsBySlug[p.Name] = protoPreset
	}

	// 2. Load user presets from database that match this workflow's tag (highest priority)
	// Get the workflow's tag for matching
	workflowTag := wf.GetPresets().GetTag()
	if workflowTag != "" {
		dbPresets, err := s.database.ListPresetsByTag(ctx, userID, workflowTag, req.Msg.ProjectId)
		if err != nil {
			logging.Warn("Failed to load user presets from database", "tag", workflowTag, "error", err)
		} else {
			for _, dbPreset := range dbPresets {
				presetsBySlug[dbPreset.Slug] = dbPresetToProto(dbPreset)
			}
		}
	}

	// Batch-fetch visibility state (2 queries instead of 2×N)
	presetItemType := int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET)
	overrides, err := s.database.ListVisibilityOverrides(ctx, userID, presetItemType)
	if err != nil {
		logging.Warn("Failed to batch-load visibility overrides for presets", "error", err)
		overrides = nil
	}
	hiddenDefaults, err := s.database.ListHiddenItemDefaults(ctx, presetItemType)
	if err != nil {
		logging.Warn("Failed to batch-load hidden defaults for presets", "error", err)
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

	return connect.NewResponse(&reliantv1.ListPresetsForWorkflowResponse{
		Presets:        protoPresets,
		InvalidPresets: invalidPresets,
	}), nil
}

// loadAllPresetsFromDB loads all builtin presets then overlays stored project presets from the DB.
// Project presets override builtins by name (same layering as the old filesystem loader).
func (s *PresetService) loadAllPresetsFromDB(ctx context.Context, projectID string) *preset.LoadResult {
	presetMap := make(map[string]*preset.Preset)
	invalidMap := make(map[string]*preset.InvalidPreset)

	// 1. Load builtin presets
	entries, err := builtin.BuiltinPresetsFS.ReadDir("presets")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			data, readErr := builtin.BuiltinPresetsFS.ReadFile("presets/" + entry.Name())
			if readErr != nil {
				invalidMap[name] = &preset.InvalidPreset{Name: name, Source: "builtin", Path: "presets/" + entry.Name(), Errors: []string{fmt.Sprintf("failed to read file: %v", readErr)}}
				continue
			}
			p, parseErr := preset.ParsePreset(data, name)
			if parseErr != nil {
				invalidMap[name] = &preset.InvalidPreset{Name: name, Source: "builtin", Path: "presets/" + entry.Name(), Errors: []string{parseErr.Error()}}
				continue
			}
			p.Source = "builtin"
			presetMap[name] = p
		}
	}

	// 2. Overlay stored project presets from DB
	if projectID != "" {
		record, err := s.database.GetProjectConfigRecord(ctx, projectID)
		if err == nil {
			storedPresets, err := cfg.ParseStoredPresets(record.ProjectPresetsJSON)
			if err == nil {
				for _, sp := range storedPresets {
					p, parseErr := preset.ParsePreset([]byte(sp.YAMLContent), sp.Name)
					if parseErr != nil {
						invalidMap[sp.Name] = &preset.InvalidPreset{Name: sp.Name, Source: "project", Errors: []string{parseErr.Error()}}
						continue
					}
					p.Source = "project"
					presetMap[sp.Name] = p
					delete(invalidMap, sp.Name) // valid project preset overrides broken builtin
				}
			}
		}
	}

	valid := make([]*preset.Preset, 0, len(presetMap))
	for _, p := range presetMap {
		valid = append(valid, p)
	}
	invalid := make([]*preset.InvalidPreset, 0, len(invalidMap))
	for _, inv := range invalidMap {
		invalid = append(invalid, inv)
	}
	return &preset.LoadResult{Valid: valid, Invalid: invalid}
}

// loadPresetByNameFromDB loads a single preset by name from stored project config or builtins.
func (s *PresetService) loadPresetByNameFromDB(ctx context.Context, projectID, name string) (*preset.Preset, error) {
	// Try stored project presets first
	if projectID != "" {
		record, err := s.database.GetProjectConfigRecord(ctx, projectID)
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			if err == nil {
				storedPresets, parseErr := cfg.ParseStoredPresets(record.ProjectPresetsJSON)
				if parseErr == nil {
					sp := cfg.FindStoredPresetByName(storedPresets, name)
					if sp != nil {
						p, err := preset.ParsePreset([]byte(sp.YAMLContent), name)
						if err == nil {
							p.Source = "project"
							return p, nil
						}
					}
				}
			}
		}
	}

	// Fall back to builtin presets
	builtinPath := "presets/" + name + ".yaml"
	data, err := builtin.BuiltinPresetsFS.ReadFile(builtinPath)
	if err == nil {
		p, err := preset.ParsePreset(data, name)
		if err == nil {
			p.Source = "builtin"
			return p, nil
		}
	}

	return nil, fmt.Errorf("preset not found: %s", name)
}

// loadWorkflow loads a workflow by name using the normal resolution path:
// builtin -> user DB draft -> DB-stored project workflow.
// No filesystem access — the API server may run in the cloud without disk access.
func (s *PresetService) loadWorkflow(ctx context.Context, workflowName, projectID string) (*reliantv1.Workflow, error) {
	parseYAML := func(data []byte) (*reliantv1.Workflow, error) {
		return wfyaml.ParseWorkflow(data)
	}

	// 1. Handle builtin:// protocol
	if strings.HasPrefix(workflowName, "builtin://") {
		name := workflowName[10:]
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
		if err != nil {
			return nil, fmt.Errorf("builtin workflow not found: %s", workflowName)
		}
		return parseYAML(data)
	}

	// 2. Try builtin workflows (without the builtin:// prefix)
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(workflowName + ".yaml")
	if err == nil {
		return parseYAML(data)
	}

	// 3. Try usable user DB drafts.
	userID := auth.MustGetUserID(ctx)
	slug := strings.ToLower(strings.ReplaceAll(workflowName, " ", "-"))
	draft, dbErr := s.database.GetUsableWorkflowBySlug(ctx, userID, slug)
	if dbErr != nil {
		return nil, fmt.Errorf("failed to load user workflow draft %q: %w", workflowName, dbErr)
	}
	if draft != nil {
		return parseDraftDefinitionV2([]byte(draft.Definition))
	}

	// 4. Try DB-stored project workflows (synced by daemon from .reliant/workflows/ on disk).
	// The workflow name inside the YAML may differ from the filename (e.g., file "blog.yaml"
	// with name "blog-content-pipeline"), so we look up by slug in the stored config.
	if projectID != "" {
		record, dbErr := s.database.GetProjectConfigRecord(ctx, projectID)
		if dbErr == nil {
			workflows, parseErr := cfg.ParseStoredWorkflows(record.ProjectWorkflowsJSON)
			if parseErr == nil {
				sw := cfg.FindStoredWorkflowBySlug(workflows, slug)
				if sw != nil {
					return parseYAML([]byte(sw.YAMLContent))
				}
			}
		}
	}

	return nil, fmt.Errorf("workflow not found: %s", workflowName)
}

// CreatePreset saves a new user preset to the database.
func (s *PresetService) CreatePreset(
	ctx context.Context,
	req *connect.Request[reliantv1.CreatePresetRequest],
) (*connect.Response[reliantv1.CreatePresetResponse], error) {
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("preset name is required"))
	}
	if req.Msg.Tag == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("preset tag is required"))
	}

	userID := auth.MustGetUserID(ctx)

	// Generate slug from name
	slug := strings.ToLower(strings.ReplaceAll(req.Msg.Name, " ", "-"))

	// Validate slug (alphanumeric, dashes, underscores only)
	validSlug := regexp.MustCompile(`^[a-z0-9_-]+$`)
	if !validSlug.MatchString(slug) {
		return connect.NewResponse(&reliantv1.CreatePresetResponse{
			Success: false,
			Error:   "preset name can only contain letters, numbers, dashes, and underscores",
		}), nil
	}

	// Check if preset already exists with this slug
	// Use project-scoped lookup if project_id is provided, otherwise global
	var existing *db.Preset
	if req.Msg.ProjectId != "" {
		existing, _ = s.database.GetPresetBySlugAndProject(ctx, userID, slug, req.Msg.ProjectId)
	} else {
		existing, _ = s.database.GetPresetBySlug(ctx, userID, slug)
	}
	if existing != nil {
		return connect.NewResponse(&reliantv1.CreatePresetResponse{
			Success: false,
			Error:   fmt.Sprintf("preset with slug '%s' already exists", slug),
		}), nil
	}

	// Check against builtin presets
	builtinPath := "presets/" + slug + ".yaml"
	if _, err := builtin.BuiltinPresetsFS.ReadFile(builtinPath); err == nil {
		return connect.NewResponse(&reliantv1.CreatePresetResponse{
			Success: false,
			Error:   fmt.Sprintf("preset name '%s' conflicts with a built-in preset; please choose a different name", req.Msg.Name),
		}), nil
	}

	// Check against stored project presets - project not found is ok, just skip the check
	if req.Msg.ProjectId != "" {
		record, err := s.database.GetProjectConfigRecord(ctx, req.Msg.ProjectId)
		if err == nil {
			storedPresets, err := cfg.ParseStoredPresets(record.ProjectPresetsJSON)
			if err == nil && cfg.FindStoredPresetByName(storedPresets, slug) != nil {
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

	saved, err := s.database.CreatePreset(ctx, newPreset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create preset: %w", err))
	}

	logging.Info("Created preset", "name", req.Msg.Name, "slug", slug, "user_id", userID)

	return connect.NewResponse(&reliantv1.CreatePresetResponse{
		Success: true,
		Preset:  dbPresetToProto(saved),
	}), nil
}

// structValueToGo converts a protobuf Value to a Go interface{}
func structValueToGo(v *structpb.Value) interface{} {
	if v == nil {
		return nil
	}

	switch k := v.Kind.(type) {
	case *structpb.Value_NullValue:
		return nil
	case *structpb.Value_NumberValue:
		return k.NumberValue
	case *structpb.Value_StringValue:
		return k.StringValue
	case *structpb.Value_BoolValue:
		return k.BoolValue
	case *structpb.Value_StructValue:
		result := make(map[string]interface{})
		for key, val := range k.StructValue.Fields {
			result[key] = structValueToGo(val)
		}
		return result
	case *structpb.Value_ListValue:
		result := make([]interface{}, len(k.ListValue.Values))
		for i, val := range k.ListValue.Values {
			result[i] = structValueToGo(val)
		}
		return result
	default:
		return nil
	}
}

// presetToProto converts a file-based preset to its proto representation.
func presetToProto(p *preset.Preset) (*reliantv1.PresetInfo, error) {
	params := make(map[string]*structpb.Value)
	for k, v := range p.Params {
		// Convert interface{} to structpb.Value
		protoValue, err := structpb.NewValue(v)
		if err != nil {
			return nil, fmt.Errorf("failed to convert param %s: %w", k, err)
		}
		params[k] = protoValue
	}

	return &reliantv1.PresetInfo{
		Name:              p.Name,
		Description:       p.Description,
		Params:            params,
		Source:            p.Source,
		Tag:               p.Tag,
		Slug:              strings.ToLower(strings.ReplaceAll(p.Name, " ", "-")),
		RecommendedSkills: p.RecommendedSkills,
	}, nil
}

// dbPresetToProto converts a database preset to its proto representation.
func dbPresetToProto(p *db.Preset) *reliantv1.PresetInfo {
	params := make(map[string]*structpb.Value)
	for k, v := range p.Params {
		// Normalize model params: convert legacy string model values to {id: string} objects
		if k == "model" {
			if s, ok := v.(string); ok && s != "" {
				v = map[string]interface{}{"id": s}
			}
		}
		protoValue, err := structpb.NewValue(v)
		if err != nil {
			continue
		}
		params[k] = protoValue
	}

	description := ""
	if p.Description != nil {
		description = *p.Description
	}

	return &reliantv1.PresetInfo{
		Id:          p.ID,
		Name:        p.Name,
		Slug:        p.Slug,
		Description: description,
		Params:      params,
		Source:      "user",
		Tag:         p.Tag,
	}
}

// UpdatePreset updates an existing preset.
// For user presets (source=user): updates in database
// For project presets (source=project): updates the file in place
// Builtin presets cannot be edited.
func (s *PresetService) UpdatePreset(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdatePresetRequest],
) (*connect.Response[reliantv1.UpdatePresetResponse], error) {
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("preset name/slug is required"))
	}

	userID := auth.MustGetUserID(ctx)
	slug := strings.ToLower(strings.ReplaceAll(req.Msg.Name, " ", "-"))

	// 1. First check if this is a user preset (database)
	var dbPreset *db.Preset
	var err error
	if req.Msg.ProjectId != "" {
		dbPreset, err = s.database.GetPresetBySlugAndProject(ctx, userID, slug, req.Msg.ProjectId)
	} else {
		dbPreset, err = s.database.GetPresetBySlug(ctx, userID, slug)
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

		updated, err := s.database.UpdatePreset(ctx, dbPreset)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preset: %w", err))
		}

		logging.Info("Updated user preset", "slug", dbPreset.Slug, "name", dbPreset.Name, "user_id", userID)
		return connect.NewResponse(&reliantv1.UpdatePresetResponse{
			Success: true,
			Preset:  dbPresetToProto(updated),
		}), nil
	}

	// 2. Not in database - check if it's a stored project preset or builtin
	filePreset, err := s.loadPresetByNameFromDB(ctx, req.Msg.ProjectId, req.Msg.Name)
	if err != nil {
		return connect.NewResponse(&reliantv1.UpdatePresetResponse{
			Success: false,
			Error:   fmt.Sprintf("preset '%s' not found", req.Msg.Name),
		}), nil
	}

	// Builtin and project presets cannot be edited directly (they're read-only on the server)
	if filePreset.Source == "builtin" {
		return connect.NewResponse(&reliantv1.UpdatePresetResponse{
			Success: false,
			Error:   "builtin presets cannot be edited",
		}), nil
	}
	if filePreset.Source == "project" {
		return connect.NewResponse(&reliantv1.UpdatePresetResponse{
			Success: false,
			Error:   "project presets cannot be edited from the server; edit the YAML file directly",
		}), nil
	}

	return connect.NewResponse(&reliantv1.UpdatePresetResponse{
		Success: false,
		Error:   fmt.Sprintf("preset '%s' not found", req.Msg.Name),
	}), nil
}

// DeletePreset deletes a preset.
// For user presets (source=user): deletes from database
// For project presets (source=project): deletes the file
// Builtin presets cannot be deleted.
func (s *PresetService) DeletePreset(
	ctx context.Context,
	req *connect.Request[reliantv1.DeletePresetRequest],
) (*connect.Response[reliantv1.DeletePresetResponse], error) {
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("preset name/slug is required"))
	}

	userID := auth.MustGetUserID(ctx)
	slug := strings.ToLower(strings.ReplaceAll(req.Msg.Name, " ", "-"))

	// 1. First check if this is a user preset (database)
	var dbPreset *db.Preset
	if req.Msg.ProjectId != "" {
		dbPreset, _ = s.database.GetPresetBySlugAndProject(ctx, userID, slug, req.Msg.ProjectId)
	} else {
		dbPreset, _ = s.database.GetPresetBySlug(ctx, userID, slug)
	}

	if dbPreset != nil {
		// Found in database - delete there
		if err := s.database.DeletePreset(ctx, dbPreset.ID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete preset: %w", err))
		}
		logging.Info("Deleted user preset", "slug", slug, "user_id", userID)
		return connect.NewResponse(&reliantv1.DeletePresetResponse{
			Success: true,
		}), nil
	}

	// 2. Not in database - check if it's a stored project preset or builtin
	filePreset, err := s.loadPresetByNameFromDB(ctx, req.Msg.ProjectId, req.Msg.Name)
	if err != nil {
		return connect.NewResponse(&reliantv1.DeletePresetResponse{
			Success: false,
			Error:   fmt.Sprintf("preset '%s' not found", req.Msg.Name),
		}), nil
	}

	// Builtin presets cannot be deleted
	if filePreset.Source == "builtin" {
		return connect.NewResponse(&reliantv1.DeletePresetResponse{
			Success: false,
			Error:   "builtin presets cannot be deleted",
		}), nil
	}

	// Project presets cannot be deleted from the server (they're synced from filesystem by daemon)
	if filePreset.Source == "project" {
		return connect.NewResponse(&reliantv1.DeletePresetResponse{
			Success: false,
			Error:   "project presets cannot be deleted from the server; delete the YAML file directly",
		}), nil
	}

	return connect.NewResponse(&reliantv1.DeletePresetResponse{
		Success: false,
		Error:   fmt.Sprintf("preset '%s' not found", req.Msg.Name),
	}), nil
}

// SetDefaultPreset marks a preset as the default for a specific group within a workflow.
// Defaults are stored as a JSON blob per workflow: preset.defaults.<workflowName>
// The blob maps group names to preset names: {"": "general", "Proposer": "researcher"}
// Empty string key "" represents top-level/workflow-level inputs.
func (s *PresetService) SetDefaultPreset(
	ctx context.Context,
	req *connect.Request[reliantv1.SetDefaultPresetRequest],
) (*connect.Response[reliantv1.SetDefaultPresetResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if req.Msg.WorkflowName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow_name is required"))
	}

	userIDStr := auth.MustGetUserID(ctx)

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
	setting, err := s.database.GetSetting(ctx, userIDStr, nil, settingKey)
	if err == nil && setting.Value != "" {
		// Parse existing JSON blob
		if err := json.Unmarshal([]byte(setting.Value), &defaults); err != nil {
			logging.Warn("Failed to parse existing defaults blob", "key", settingKey, "error", err)
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
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to serialize defaults: %w", err))
	}

	// Upsert the setting
	if err := s.upsertSetting(ctx, userIDStr, settingKey, string(blobBytes)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to set default preset: %w", err))
	}

	logging.Info("Set default preset", "workflow", req.Msg.WorkflowName, "group", groupName, "preset", presetName)

	return connect.NewResponse(&reliantv1.SetDefaultPresetResponse{
		Success: true,
	}), nil
}

// GetDefaultPreset retrieves all default presets for a workflow.
// Returns a map of group name to preset name.
// Empty string key "" represents top-level/workflow-level inputs.
// Merges user settings with system defaults (user settings take precedence).
func (s *PresetService) GetDefaultPreset(
	ctx context.Context,
	req *connect.Request[reliantv1.GetDefaultPresetRequest],
) (*connect.Response[reliantv1.GetDefaultPresetResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if req.Msg.WorkflowName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow_name is required"))
	}

	userIDStr := auth.MustGetUserID(ctx)

	// Load workflow to get workflow-defined defaults
	wf, err := s.loadWorkflow(ctx, req.Msg.WorkflowName, req.Msg.ProjectId)
	if err != nil {
		// Fail gracefully - return empty map
		logging.Warn("GetDefaultPreset: workflow not found", "workflowName", req.Msg.WorkflowName, "error", err)
		return connect.NewResponse(&reliantv1.GetDefaultPresetResponse{
			Presets: make(map[string]string),
		}), nil
	}

	// Start with workflow-defined defaults from YAML
	defaults := make(map[string]string)

	// Workflow-level default (for top-level inputs)
	if wf.GetPresets().GetDefault() != "" {
		defaults[""] = wf.GetPresets().GetDefault()
	}

	// Group-level defaults
	for groupName, input := range wf.GetInputs() {
		if !model.IsGroupInput(input) {
			continue
		}
		cfg, ok := input.GetConfig().(*reliantv1.Input_GroupInput)
		if !ok || cfg.GroupInput == nil {
			continue
		}
		if cfg.GroupInput.GetPresets().GetDefault() != "" {
			defaults[groupName] = cfg.GroupInput.GetPresets().GetDefault()
		}
	}

	// Merge user overrides (take precedence over workflow-defined defaults)
	settingKey := fmt.Sprintf("preset.defaults.%s", req.Msg.WorkflowName)
	setting, err := s.database.GetSetting(ctx, userIDStr, nil, settingKey)
	if err == nil && setting.Value != "" {
		var userDefaults map[string]string
		if err := json.Unmarshal([]byte(setting.Value), &userDefaults); err == nil {
			for group, preset := range userDefaults {
				defaults[group] = preset
			}
		}
	}

	return connect.NewResponse(&reliantv1.GetDefaultPresetResponse{
		Presets: defaults,
	}), nil
}

// upsertSetting creates or updates a setting (similar to SettingsService.upsertSetting)
func (s *PresetService) upsertSetting(ctx context.Context, userID, key, value string) error {
	setting, err := s.database.GetSetting(ctx, userID, nil, key)
	if err != nil {
		// Setting doesn't exist, create it
		newSetting := &db.Setting{
			ID:        fmt.Sprintf("%s-%s", userID, key), // Simple ID for now
			UserID:    userID,
			ProjectID: nil,
			Key:       key,
			Value:     value,
			ValueType: "string",
		}
		return s.database.CreateSetting(ctx, newSetting)
	}

	// Setting exists, update it
	setting.Value = value
	return s.database.UpdateSetting(ctx, setting)
}
