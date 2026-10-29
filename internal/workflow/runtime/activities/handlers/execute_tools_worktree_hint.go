// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"strings"

	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// worktreeAddSuggestion is the one line a shell call that ran `git worktree
// add` gets appended to its result, or "" for any other call
// (tools/shell_worktree_hint.go). It is decided here rather than in the shell
// tool, which runs on the daemon: only the worker has the turn's capability
// set — the suggestion names the worktree tool, so it is made only when
// load_tool would grant it — and the project's memory as the run sees it.
func (a *ExecuteToolsActivity) worktreeAddSuggestion(ctx context.Context, caps *tools.Capabilities, tec *toolExecutionContext) string {
	if tec == nil || tec.toolName != tools.ShellToolName || tec.project == nil {
		return ""
	}
	var params tools.ShellParams
	if err := json.Unmarshal([]byte(tec.dispatchInput), &params); err != nil || !strings.Contains(params.Command, "worktree") {
		return ""
	}
	if caps == nil {
		caps = tools.LegacyCapabilities(tec.chat != nil && tec.chat.NoMachine)
	}
	if !caps.CanLoad(tools.ToolWorktree) {
		return ""
	}

	// Relative targets resolve against the directory the command ran in.
	req := tec.buildToolRequest()
	toolCtx := rctx.NewToolContext(ctx, tec.chatID, tec.thread, tec.project, &rctx.WorktreeInfo{Path: req.WorktreePath})
	toolCtx.Repos = tec.repos
	workingDir, err := tools.ResolveRepoPath(toolCtx, params.Repo)
	if err != nil {
		workingDir = req.WorktreePath
	}
	targets := tools.WorktreeAddTargets(params.Command, workingDir)
	if len(targets) == 0 {
		return ""
	}
	return tools.WorktreeAddSuggestion(targets, a.memoryWorktreeRoots(ctx, tec.project.ID, configMachine(tec.daemonSelector)))
}

// memoryWorktreeRoots are the worktree directories the user's and project's
// memory files name — the same memory call_llm puts in front of the model.
// A rule there wins over the suggestion. Read only for a call that made a
// worktree, through the worker's provider, which is cached per config version.
func (a *ExecuteToolsActivity) memoryWorktreeRoots(ctx context.Context, projectID, daemonID string) []string {
	if a.configProvider == nil {
		return nil
	}
	cfg, err := a.configProvider.GetProjectConfig(ctx, cfgpkg.ProjectRef{ProjectID: projectID, DaemonID: daemonID})
	if err != nil {
		logging.WarnContext(ctx, "[ExecuteTools] Project memory could not be loaded; worktree rules in it are not consulted", "projectID", projectID, "error", err)
		return nil
	}
	if cfg == nil {
		return nil
	}
	docs := []string{cfg.GlobalMemoryMD, cfg.ProjectMemoryMD}
	for _, memory := range cfg.RepoMemories {
		docs = append(docs, memory)
	}
	return tools.WorktreeRootsFromMemory(docs...)
}
