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
	// ChatID is the run's chat: its project, and the machine it runs on,
	// whose synced presets a project preset is read from. It wins over
	// ProjectID.
	ChatID     string `json:"chat_id" reliant:"-"`
	PresetName string `json:"preset_name"`
	// CatalogPin is the project catalog the run started with
	// (LoadWorkflowInput.CatalogPin): a project preset is read from it.
	CatalogPin string `json:"catalog_pin,omitempty" reliant:"-"`
}

// LoadPresetParamsActivity loads preset params for use in spawned workflows.
// This activity exists to avoid import cycles between v2 and preset packages.
type LoadPresetParamsActivity struct {
	repo LoadPresetParamsStore
}

// NewLoadPresetParamsActivity creates a new LoadPresetParamsActivity
func NewLoadPresetParamsActivity(repo LoadPresetParamsStore) *LoadPresetParamsActivity {
	return &LoadPresetParamsActivity{repo: repo}
}

// Name returns the activity name for registration
func (a *LoadPresetParamsActivity) Name() string {
	return "LoadPresetParams"
}

// Execute loads a preset by name and returns its params: the run's project
// preset, read from the presets its machine synced (the union when it has
// never synced the project, see workflowsource.ProjectPresets), else a
// builtin. A project preset that does not parse fails the load rather than
// silently running a builtin of the same name.
func (a *LoadPresetParamsActivity) Execute(ctx context.Context, input LoadPresetParamsInput) (map[string]interface{}, error) {
	logger := activity.GetLogger(ctx)

	if input.PresetName == "" {
		return nil, fmt.Errorf("preset_name is required")
	}

	projectID, daemonID := input.ProjectID, ""
	if input.ChatID != "" && a.repo != nil {
		chat, err := a.repo.GetChat(ctx, input.ChatID)
		if err != nil {
			return nil, fmt.Errorf("load preset %s: read the run's chat: %w", input.PresetName, err)
		}
		projectID, daemonID = chat.ProjectID, launchRunMachine(chat)
	}

	// One column, not the whole config row: the row carries every indexed
	// skill and can run to tens of MB.
	if projectID != "" && a.repo != nil {
		presets, err := projectPresets(ctx, a.repo, projectID, daemonID, input.CatalogPin)
		if err != nil {
			return nil, fmt.Errorf("load preset %s: %w", input.PresetName, err)
		}
		if sp := cfg.FindStoredPresetByName(presets, input.PresetName); sp != nil {
			p, err := preset.ParsePreset([]byte(sp.YAMLContent), input.PresetName)
			if err != nil {
				return nil, fmt.Errorf("preset %s (%s): %w", input.PresetName, sp.RelativePath, err)
			}
			logger.Info("[LoadPresetParams] Loaded preset from stored config",
				"preset", input.PresetName,
				"daemon_id", daemonID,
				"param_count", len(p.Params))
			return p.InputParams(), nil
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
			return p.InputParams(), nil
		}
	}

	logger.Warn("[LoadPresetParams] Preset not found",
		"preset", input.PresetName,
		"project_id", input.ProjectID)
	return nil, fmt.Errorf("preset not found: %s", input.PresetName)
}

// launchRunMachine is the machine a chat's runs execute on (launch.RunMachine,
// which this package cannot import): its pin, or "" for none.
func launchRunMachine(chat *db.Chat) string {
	if chat == nil || chat.NoMachine || chat.ActiveDaemonID == nil {
		return ""
	}
	return *chat.ActiveDaemonID
}
