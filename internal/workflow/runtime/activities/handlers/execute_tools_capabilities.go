// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// capabilityRefusal is why a call may not run under its turn's capability set,
// or "" when it may.
//
// caps is nil only for a batch whose call_llm recorded no set — a run that
// predates it. That batch keeps exactly what a worker restart already
// produced: no offered check, and the base tier. The next call_llm records a
// set, so a run spends at most one batch here (research/TOOL_CAPABILITIES.md
// §4.1).
func capabilityRefusal(caps *tools.Capabilities, toolCall message.ToolCall) string {
	name := toolCall.Name
	if caps == nil {
		if required := tools.MinimumPermissionForTool(name); !tools.PermissionAtLeast(tools.PermissionMutating, required) {
			return fmt.Sprintf("Tool '%s' requires '%s' permission, but the current permission level is '%s'.",
				name, required, tools.PermissionMutating)
		}
		return ""
	}
	if !caps.Offers(name) {
		return caps.Explain(name)
	}
	// spawn runs workflow-side. One reaches this activity only because the
	// workflow declined to dispatch it (executeToolsWithSpawnSupport), and the
	// only reason left once it was offered is the preset.
	if name == tools.ToolSpawn {
		if preset := spawnPreset(toolCall.Input); preset != "" && !caps.AllowsPreset(preset) {
			return caps.ExplainPreset(preset)
		}
		return "The spawn call could not be dispatched, so it was not run."
	}
	return ""
}

// spawnPreset is the preset a spawn call names, or "".
func spawnPreset(input string) string {
	var params struct {
		Preset string `json:"preset"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return ""
	}
	return params.Preset
}

// refuseToolCall answers a call the capability set does not allow: an error
// tool_result the model reads, recorded FAILED with the reason like every
// other refusal (the no-machine one included), so the UI shows a refused call
// rather than a spinner.
func (a *ExecuteToolsActivity) refuseToolCall(ctx context.Context, rtx RuntimeContext, toolCall message.ToolCall, reason string) message.ToolResult {
	result := a.buildToolResult(toolCall.ID, toolCall.Name, reason, "", true, nil, nil)
	tec, errMsg := a.loadToolExecutionContext(ctx, rtx.ChatID, rtx.Thread, toolCall.Name, toolCall.Input, toolCall.ID, rtx.ProjectPath)
	if errMsg != "" {
		return result
	}
	completedAt := time.Now()
	a.upsertTerminalToolCall(ctx, tec, core.ToolCallStatusFailed, toolCallUpsertOpts{
		completedAt:  &completedAt,
		errorMessage: reason,
	}, &toolCallResultWrite{content: result.Content, isError: true})
	return result
}

// grantedTools is every tool this batch's load_tool calls granted, for the
// workflow to record in its per-thread state.
func grantedTools(results []message.ToolResult) []string {
	seen := make(map[string]bool)
	var granted []string
	for _, r := range results {
		for _, name := range grantedByResult(r.Name, r.Metadata, r.IsError) {
			if !seen[name] {
				seen[name] = true
				granted = append(granted, name)
			}
		}
	}
	return granted
}

// grantedByResult is what one tool result granted its thread: a successful
// load_tool result's loaded names, read from its metadata. It is the one rule
// for a grant, so the batch's report to the workflow (grantedTools) and the
// durable result row a coarse restart rebuilds grants from cannot disagree.
func grantedByResult(toolName, metadata string, isError bool) []string {
	if toolName != tools.ToolLoadTool || isError || metadata == "" {
		return nil
	}
	var loaded tools.LoadToolMetadata
	if err := json.Unmarshal([]byte(metadata), &loaded); err != nil {
		return nil
	}
	granted := make([]string, 0, len(loaded.LoadedTools))
	for _, name := range loaded.LoadedTools {
		if name != "" {
			granted = append(granted, name)
		}
	}
	return granted
}

// WithConfigProvider gives the activity the worker's project config provider,
// the one call_llm resolves the turn's skill catalog from. The skill tool
// reads its skills through it.
func (a *ExecuteToolsActivity) WithConfigProvider(provider cfgpkg.ConfigProvider) *ExecuteToolsActivity {
	a.configProvider = provider
	return a
}

// projectSkills is the project's skill catalog, from the worker's config
// provider — the same source, and so the same catalog, call_llm offered the
// turn from. A project with no synced config, or one that cannot be read,
// has no skills, which the skill tool reports itself.
//
// It used to read the whole config row and re-parse every skill on each skill
// call: 18 MB per call in prod. The worker's provider is cached per config
// version, so a call costs one version read.
func (a *ExecuteToolsActivity) projectSkills(ctx context.Context, projectID string) []cfgpkg.StoredSkill {
	if projectID == "" {
		return nil
	}
	if a.configProvider == nil {
		logging.Warn("[ExecuteTools] No config provider wired; the skill tool sees no project skills", "projectID", projectID)
		return nil
	}
	cfg, err := a.configProvider.GetProjectConfig(ctx, cfgpkg.ProjectRef{ProjectID: projectID})
	if err != nil {
		logging.Warn("[ExecuteTools] Project skills could not be loaded", "projectID", projectID, "error", err)
		return nil
	}
	if cfg == nil {
		return nil
	}
	return cfg.Skills
}
