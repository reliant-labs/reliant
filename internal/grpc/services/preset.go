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
	"sync"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
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

// projectBelongsToUser verifies the authenticated user owns projectID, returning
// a Connect error if the project doesn't exist or belongs to someone else.
func (s *PresetService) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	_, err := s.database.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "access denied") {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("database error"))
	}
	return nil
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

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

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

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

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

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// The reads below are independent, so they overlap: the workflow (then the
	// user's presets for its tag), the builtin + stored project presets, and
	// the two visibility lookups.
	presetItemType := int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET)
	var (
		wf             *reliantv1.Workflow
		wfErr          error
		dbPresets      []*db.Preset
		dbPresetsErr   error
		loadResult     *preset.LoadResult
		overrides      map[string]bool
		overridesErr   error
		hiddenDefaults []string
		hiddenErr      error
		wg             sync.WaitGroup
	)
	wg.Add(4)
	go func() {
		defer wg.Done()
		wf, wfErr = s.loadWorkflow(ctx, req.Msg.WorkflowName, req.Msg.ProjectId)
		if wfErr != nil {
			return
		}
		if tag := wf.GetPresets().GetTag(); tag != "" {
			dbPresets, dbPresetsErr = s.database.ListPresetsByTag(ctx, userID, tag, req.Msg.ProjectId)
		}
	}()
	go func() {
		defer wg.Done()
		loadResult = s.loadAllPresetsFromDB(ctx, req.Msg.ProjectId)
	}()
	go func() {
		defer wg.Done()
		overrides, overridesErr = s.database.ListVisibilityOverrides(ctx, userID, presetItemType)
	}()
	go func() {
		defer wg.Done()
		hiddenDefaults, hiddenErr = s.database.ListHiddenItemDefaults(ctx, presetItemType)
	}()
	wg.Wait()

	if wfErr != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %w", wfErr))
	}

	// Use a map to deduplicate by slug (user presets take priority)
	presetsBySlug := make(map[string]*reliantv1.PresetInfo)
	var invalidPresets []*reliantv1.InvalidPreset

	// 1. Builtin + stored project presets, filtered by workflow compatibility
	for _, inv := range loadResult.Invalid {
		invalidPresets = append(invalidPresets, &reliantv1.InvalidPreset{
			Name:   inv.Name,
			Source: inv.Source,
			Path:   inv.Path,
			Errors: inv.Errors,
		})
	}
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

	// 2. User presets from the database that match this workflow's tag (highest priority)
	if workflowTag := wf.GetPresets().GetTag(); workflowTag != "" {
		if dbPresetsErr != nil {
			logging.Warn("Failed to load user presets from database", "tag", workflowTag, "error", dbPresetsErr)
		} else {
			for _, dbPreset := range dbPresets {
				presetsBySlug[dbPreset.Slug] = dbPresetToProto(dbPreset)
			}
		}
	}

	if overridesErr != nil {
		logging.Warn("Failed to batch-load visibility overrides for presets", "error", overridesErr)
		overrides = nil
	}
	if hiddenErr != nil {
		logging.Warn("Failed to batch-load hidden defaults for presets", "error", hiddenErr)
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

// builtinPreset is one embedded preset file, parsed: exactly one of preset and
// invalid is set.
type builtinPreset struct {
	name    string
	preset  *preset.Preset
	invalid *preset.InvalidPreset
}

var (
	builtinPresetsOnce   sync.Once
	builtinPresetsParsed []builtinPreset
)

// builtinPresets parses the embedded presets once per process. They are
// compiled into the binary yet were re-parsed on every ListPresetsForWorkflow
// (~4ms each). Callers copy what they take; Params is shared and read-only.
func builtinPresets() []builtinPreset {
	builtinPresetsOnce.Do(func() {
		entries, err := builtin.BuiltinPresetsFS.ReadDir("presets")
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			path := "presets/" + entry.Name()
			data, readErr := builtin.BuiltinPresetsFS.ReadFile(path)
			if readErr != nil {
				builtinPresetsParsed = append(builtinPresetsParsed, builtinPreset{name: name, invalid: &preset.InvalidPreset{Name: name, Source: "builtin", Path: path, Errors: []string{fmt.Sprintf("failed to read file: %v", readErr)}}})
				continue
			}
			p, parseErr := preset.ParsePreset(data, name)
			if parseErr != nil {
				builtinPresetsParsed = append(builtinPresetsParsed, builtinPreset{name: name, invalid: &preset.InvalidPreset{Name: name, Source: "builtin", Path: path, Errors: []string{parseErr.Error()}}})
				continue
			}
			p.Source = "builtin"
			builtinPresetsParsed = append(builtinPresetsParsed, builtinPreset{name: name, preset: p})
		}
	})
	return builtinPresetsParsed
}

// loadAllPresetsFromDB loads all builtin presets then overlays stored project presets from the DB.
// Project presets override builtins by name (same layering as the old filesystem loader).
func (s *PresetService) loadAllPresetsFromDB(ctx context.Context, projectID string) *preset.LoadResult {
	presetMap := make(map[string]*preset.Preset)
	invalidMap := make(map[string]*preset.InvalidPreset)

	// 1. Builtin presets (parsed once per process)
	for _, bp := range builtinPresets() {
		if bp.invalid != nil {
			inv := *bp.invalid
			invalidMap[bp.name] = &inv
			continue
		}
		p := *bp.preset
		presetMap[bp.name] = &p
	}

	// 2. Overlay stored project presets from DB
	if projectID != "" {
		presetsJSON, err := s.database.GetProjectPresetsJSON(ctx, projectID)
		if err == nil {
			storedPresets, err := cfg.ParseStoredPresets(presetsJSON)
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
		presetsJSON, err := s.database.GetProjectPresetsJSON(ctx, projectID)
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			if err == nil {
				storedPresets, parseErr := cfg.ParseStoredPresets(presetsJSON)
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

// loadWorkflow resolves a workflow ref the way a run does (workflowsource):
// builtin:// from the embedded builtins; anything else from the user's own
// complete workflows, then the project's synced workflows by name:.
// No filesystem access — the API server may run in the cloud without disk access.
func (s *PresetService) loadWorkflow(ctx context.Context, workflowRef, projectID string) (*reliantv1.Workflow, error) {
	resolved, err := workflowsource.Resolve(ctx, s.database, workflowsource.Options{
		UserID: auth.MustGetUserID(ctx), ProjectID: projectID,
	}, workflowRef)
	if err != nil {
		return nil, fmt.Errorf("workflow %q: %w", workflowRef, err)
	}
	return resolved.Workflow, nil
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
		if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
			return nil, err
		}
		presetsJSON, err := s.database.GetProjectPresetsJSON(ctx, req.Msg.ProjectId)
		if err == nil {
			storedPresets, err := cfg.ParseStoredPresets(presetsJSON)
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
		Name:        p.Name,
		Description: p.Description,
		Params:      params,
		Source:      p.Source,
		Tag:         p.Tag,
		Slug:        strings.ToLower(strings.ReplaceAll(p.Name, " ", "-")),
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

	if req.Msg.ProjectId != "" {
		if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
			return nil, err
		}
	}

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

	if req.Msg.ProjectId != "" {
		if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
			return nil, err
		}
	}

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

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userIDStr); err != nil {
		return nil, err
	}

	defaults := s.resolveDefaultPresets(ctx, userIDStr, req.Msg.ProjectId, req.Msg.WorkflowName, nil,
		func(ref string) (*reliantv1.Workflow, error) { return s.loadWorkflow(ctx, ref, req.Msg.ProjectId) })

	return connect.NewResponse(&reliantv1.GetDefaultPresetResponse{
		Presets: defaults,
	}), nil
}

// resolveDefaultPresets computes one workflow's group-to-preset defaults:
// workflow-defined defaults from YAML, overlaid with the user's saved
// overrides. It never fails — an unloadable workflow yields an empty map,
// matching the graceful-degradation contract both RPCs expose.
//
// userOverrides, when non-nil, supplies the already-loaded
// `preset.defaults.<workflow>` setting values so a batch caller can read every
// override in one query instead of one GetSetting per workflow. Passing nil
// makes the function fetch the single setting it needs itself.
func (s *PresetService) resolveDefaultPresets(
	ctx context.Context,
	userID, projectID, workflowName string,
	userOverrides map[string]string,
	load func(ref string) (*reliantv1.Workflow, error),
) map[string]string {
	defaults := make(map[string]string)

	wf, err := load(workflowName)
	if err != nil {
		logging.Warn("resolveDefaultPresets: workflow not found", "workflowName", workflowName, "error", err)
		return defaults
	}

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
	settingKey := fmt.Sprintf("preset.defaults.%s", workflowName)
	rawOverride := ""
	if userOverrides != nil {
		rawOverride = userOverrides[settingKey]
	} else if setting, err := s.database.GetSetting(ctx, userID, nil, settingKey); err == nil {
		rawOverride = setting.Value
	}

	if rawOverride != "" {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(rawOverride), &parsed); err == nil {
			for group, preset := range parsed {
				defaults[group] = preset
			}
		}
	}

	return defaults
}

// GetDefaultPresetsBatch resolves default presets for many workflows in one
// round trip. Behavior per workflow is identical to GetDefaultPreset — both go
// through resolveDefaultPresets — but the user's overrides are read with a
// single ListSettingsByKey rather than one GetSetting per workflow, so the
// query count stays flat as the workflow list grows.
func (s *PresetService) GetDefaultPresetsBatch(
	ctx context.Context,
	req *connect.Request[reliantv1.GetDefaultPresetsBatchRequest],
) (*connect.Response[reliantv1.GetDefaultPresetsBatchResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userIDStr := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userIDStr); err != nil {
		return nil, err
	}

	// The user's `preset.defaults.*` overrides and the workflow catalog are
	// independent reads: one query each, whatever the batch size. A settings
	// failure is not fatal — no overrides apply, the same outcome as a user
	// who has never set one.
	var (
		settings    []*db.Setting
		settingsErr error
		catalog     *workflowCatalog
		wg          sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		settings, settingsErr = s.database.ListSettingsByKey(ctx, userIDStr, "preset.defaults.%")
	}()
	go func() {
		defer wg.Done()
		catalog = loadWorkflowCatalog(ctx, s.database, userIDStr, req.Msg.ProjectId)
	}()
	wg.Wait()

	userOverrides := make(map[string]string)
	if settingsErr == nil {
		for _, setting := range settings {
			userOverrides[setting.Key] = setting.Value
		}
	} else {
		logging.Warn("GetDefaultPresetsBatch: failed to load preset default overrides", "error", settingsErr)
	}

	opts := workflowsource.Options{UserID: userIDStr, ProjectID: req.Msg.ProjectId}
	load := memoizeRefLoader(withBuiltinCache(func(ref string) (*reliantv1.Workflow, error) {
		resolved, err := workflowsource.Resolve(ctx, catalog, opts, ref)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: %w", ref, err)
		}
		return resolved.Workflow, nil
	}))

	presetsByWorkflow := make(map[string]*reliantv1.WorkflowDefaultPresets)
	seen := make(map[string]bool, len(req.Msg.WorkflowNames))

	for _, workflowName := range req.Msg.WorkflowNames {
		if workflowName == "" || seen[workflowName] {
			continue
		}
		seen[workflowName] = true

		defaults := s.resolveDefaultPresets(ctx, userIDStr, req.Msg.ProjectId, workflowName, userOverrides, load)
		// Omit empty results so the response mirrors the single RPC's
		// "no defaults" outcome without inventing an entry for it.
		if len(defaults) == 0 {
			continue
		}
		presetsByWorkflow[workflowName] = &reliantv1.WorkflowDefaultPresets{Presets: defaults}
	}

	return connect.NewResponse(&reliantv1.GetDefaultPresetsBatchResponse{
		PresetsByWorkflow: presetsByWorkflow,
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
