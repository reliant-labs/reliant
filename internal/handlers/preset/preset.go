// Copyright (c) 2025 Reliant Labs
package preset

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// projectBelongsToUser verifies the authenticated user owns projectID: the
// store's NotFound (core.ErrProjectNotFound) if the project doesn't exist or
// belongs to someone else, Internal if it could not be read.
func (s *Service) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	_, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	return svcerr.WrapOr(err, svcerr.Internal("database error"))
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
func (s *Service) loadAllPresetsFromDB(ctx context.Context, projectID string) *preset.LoadResult {
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
		presetsJSON, err := s.deps.Repo.GetProjectPresetsJSON(ctx, projectID)
		if err == nil {
			storedPresets, err := cfg.ParseStoredPresets(presetsJSON)
			if err == nil {
				for _, sp := range storedPresets {
					name := storedPresetListName(storedPresets, sp)
					p, parseErr := preset.ParsePreset([]byte(sp.YAMLContent), name)
					if parseErr != nil {
						invalidMap[name] = &preset.InvalidPreset{Name: name, Source: "project", Errors: []string{parseErr.Error()}}
						continue
					}
					p.Source = "project"
					presetMap[name] = p
					delete(invalidMap, name) // valid project preset overrides broken builtin
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

// storedPresetListName is the name a synced preset is listed under: its bare
// name when that name resolves to it (cfg.FindStoredPresetByName: the root
// shadows nested repos, then repository path order), otherwise the
// "<repo>/<name>" that names it alone. So every synced preset is listed once,
// under a name that loads it.
func storedPresetListName(all []cfg.StoredPreset, sp cfg.StoredPreset) string {
	if winner := cfg.FindStoredPresetByName(all, sp.Name); winner != nil && winner.RelativePath == sp.RelativePath {
		return sp.Name
	}
	return sp.QualifiedName()
}

// loadPresetByNameFromDB loads a single preset by name from stored project config or builtins.
func (s *Service) loadPresetByNameFromDB(ctx context.Context, projectID, name string) (*preset.Preset, error) {
	// Try stored project presets first
	if projectID != "" {
		presetsJSON, err := s.deps.Repo.GetProjectPresetsJSON(ctx, projectID)
		if err == nil || !svcerr.IsNotFound(err) {
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

	return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("preset not found: %s", name))
}

// loadWorkflow resolves a workflow ref the way a run does (workflowsource):
// builtin:// from the embedded builtins; anything else from the user's own
// complete workflows, then the project's synced workflows by name:.
// No filesystem access — the API server may run in the cloud without disk access.
func (s *Service) loadWorkflow(ctx context.Context, workflowRef, projectID string) (*reliantv1.Workflow, error) {
	resolved, err := workflowsource.Resolve(ctx, s.deps.Repo, workflowsource.Options{
		UserID: auth.MustGetUserID(ctx), ProjectID: projectID,
	}, workflowRef)
	if err != nil {
		return nil, fmt.Errorf("workflow %q: %w", workflowRef, err)
	}
	return resolved.Workflow, nil
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

// resolveDefaultPresets computes one workflow's group-to-preset defaults:
// workflow-defined defaults from YAML, overlaid with the user's saved
// overrides. It never fails — an unloadable workflow yields an empty map,
// matching the graceful-degradation contract both RPCs expose.
//
// userOverrides, when non-nil, supplies the already-loaded
// `preset.defaults.<workflow>` setting values so a batch caller can read every
// override in one query instead of one GetSetting per workflow. Passing nil
// makes the function fetch the single setting it needs itself.
func (s *Service) resolveDefaultPresets(
	ctx context.Context,
	userID, projectID, workflowName string,
	userOverrides map[string]string,
	load func(ref string) (*reliantv1.Workflow, error),
) map[string]string {
	defaults := make(map[string]string)

	wf, err := load(workflowName)
	if err != nil {
		logging.WarnContext(ctx, "resolveDefaultPresets: workflow not found", "workflowName", workflowName, "error", err)
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
	} else if setting, err := s.deps.Repo.GetSetting(ctx, userID, nil, settingKey); err == nil {
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

// upsertSetting creates or updates a setting (similar to SettingsService.upsertSetting)
func (s *Service) upsertSetting(ctx context.Context, userID, key, value string) error {
	setting, err := s.deps.Repo.GetSetting(ctx, userID, nil, key)
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
		return s.deps.Repo.CreateSetting(ctx, newSetting)
	}

	// Setting exists, update it
	setting.Value = value
	return s.deps.Repo.UpdateSetting(ctx, setting)
}
