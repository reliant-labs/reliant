// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"go.temporal.io/sdk/activity"
)

// LoadPresetParamsInput is the input for loading preset params
type LoadPresetParamsInput struct {
	ProjectPath string `json:"project_path"`
	ProjectID   string `json:"project_id"`
	PresetName  string `json:"preset_name"`
}

// LoadPresetParamsActivity loads preset params for use in spawned workflows.
// This activity exists to avoid import cycles between v2 and preset packages.
type LoadPresetParamsActivity struct {
	repo db.Repository
}

// NewLoadPresetParamsActivity creates a new LoadPresetParamsActivity
func NewLoadPresetParamsActivity(repo db.Repository) *LoadPresetParamsActivity {
	return &LoadPresetParamsActivity{repo: repo}
}

// Name returns the activity name for registration
func (a *LoadPresetParamsActivity) Name() string {
	return "LoadPresetParams"
}

// Execute loads a preset by name and returns its params.
// Loads from stored project config (synced by daemon), then falls back to builtins.
func (a *LoadPresetParamsActivity) Execute(ctx context.Context, input LoadPresetParamsInput) (map[string]interface{}, error) {
	logger := activity.GetLogger(ctx)

	if input.PresetName == "" {
		return nil, fmt.Errorf("preset_name is required")
	}

	// Try stored project presets from daemon config sync
	if input.ProjectID != "" && a.repo != nil {
		record, err := a.repo.GetProjectConfigRecord(ctx, input.ProjectID)
		if err == nil {
			presets, err := cfg.ParseStoredPresets(record.ProjectPresetsJSON)
			if err == nil {
				sp := cfg.FindStoredPresetByName(presets, input.PresetName)
				if sp != nil {
					p, err := preset.ParsePreset([]byte(sp.YAMLContent), input.PresetName)
					if err == nil {
						logger.Info("[LoadPresetParams] Loaded preset from stored config",
							"preset", input.PresetName,
							"param_count", len(p.Params))
						return p.Params, nil
					}
				}
			}
		}
	}

	// Fall back to builtin presets
	builtinPath := "presets/" + input.PresetName + ".yaml"
	data, err := builtin.BuiltinPresetsFS.ReadFile(builtinPath)
	if err == nil {
		p, err := preset.ParsePreset(data, input.PresetName)
		if err == nil {
			logger.Info("[LoadPresetParams] Loaded builtin preset",
				"preset", input.PresetName,
				"param_count", len(p.Params))
			return p.Params, nil
		}
	}

	logger.Warn("[LoadPresetParams] Preset not found",
		"preset", input.PresetName,
		"project_id", input.ProjectID)
	return nil, fmt.Errorf("preset not found: %s", input.PresetName)
}
