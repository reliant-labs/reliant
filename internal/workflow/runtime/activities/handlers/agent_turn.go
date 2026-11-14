// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// startAgentTurn opens the span of the agent turn a CallLLM runs
// (telemetry.StartAgentTurn), naming the agent by its preset. The runtime
// names a sub-agent's (RunTelemetry.Preset); the chat's own agent runs as the
// presets the chat has selected, which only the chat row records.
func startAgentTurn(ctx context.Context, rtx RuntimeContext, chat *db.Chat) (context.Context, *telemetry.AgentTurn) {
	if preset := turnPreset(rtx, chat); preset != "" {
		ctx = telemetry.WithRunScope(ctx, telemetry.RunScope{Preset: preset})
	}
	return telemetry.StartAgentTurn(ctx)
}

// turnPreset is the preset the turn's agent runs as: the runtime's, else, on
// the chat's own run (not a spawned sub-agent's), the chat's selection — one
// name, or each group's sorted and joined when it selects several.
func turnPreset(rtx RuntimeContext, chat *db.Chat) string {
	if rtx.Telemetry != nil {
		if rtx.Telemetry.Preset != "" {
			return rtx.Telemetry.Preset
		}
		if rtx.Telemetry.ParentRunID != "" {
			return ""
		}
	}
	if chat == nil || len(chat.SelectedPresets) == 0 {
		return ""
	}
	names := make([]string, 0, len(chat.SelectedPresets))
	for _, name := range chat.SelectedPresets {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// turnTraceParent is the traceparent of the turn that asked for an
// execute_tools batch's calls, "" when it was not recorded.
func turnTraceParent(rtx RuntimeContext) string {
	if rtx.Telemetry == nil {
		return ""
	}
	return rtx.Telemetry.TurnTraceParent
}

// toolLocation is where a tool call runs: on the user's machine, through the
// gateway, or here on the worker.
func toolLocation(toolName string) string {
	if tools.NeedsMachine(toolName) {
		return telemetry.ToolLocationDaemon
	}
	return telemetry.ToolLocationLocal
}
