// Copyright (c) 2025 Reliant Labs
package runtime

import "path/filepath"

// Runtime-injected inputs that describe the chat's checkout. The launcher sets
// project_path for every chat with a project, and worktree_path /
// worktree_branch when the chat is bound to a worktree (launch.Checkout).
const (
	inputKeyProjectPath    = "project_path"
	inputKeyWorktreePath   = "worktree_path"
	inputKeyWorktreeBranch = "worktree_branch"
)

// addScopeEnvironment fills the workflow.* fields that say WHERE a scope runs:
//
//   - path: the scope's working directory. That is execCtx.ProjectPath — the
//     run's checkout at the root, replaced by a sub-workflow's project.path —
//     so it names the same directory the scope's tools and presets use.
//   - worktree_path, branch: the chat's worktree and its branch, reported only
//     in a scope whose directory IS that worktree. A scope that moved elsewhere
//     (parallel-compete's candidate worktrees, any project.path override)
//     reports neither, rather than describing a checkout it is not in.
//   - run_id: the Temporal run executing the scope.
//
// These used to be declared, documented, and never set, so workflow.path was
// always "" — and parallel-compete's `rsync --delete <winner>/ "{{workflow.path}}/"`
// synced onto "/". An empty path is still possible (a run with no project
// directory); wfcel.CheckWorkflowPathReference makes referencing it an error.
func addScopeEnvironment(context map[string]interface{}, execCtx *ExecutionContext, inputs map[string]interface{}) {
	path := ""
	runID := ""
	if execCtx != nil {
		path = execCtx.ProjectPath
		runID = execCtx.RunID
	}
	if path == "" {
		path, _ = inputs[inputKeyProjectPath].(string)
	}
	context[workflowContextKeyPath] = path
	context[workflowContextKeyRunID] = runID

	worktreePath, _ := inputs[inputKeyWorktreePath].(string)
	if path == "" || worktreePath == "" || filepath.Clean(worktreePath) != filepath.Clean(path) {
		return
	}
	context[workflowContextKeyWorktreePath] = worktreePath
	if branch, ok := inputs[inputKeyWorktreeBranch].(string); ok {
		context[workflowContextKeyBranch] = branch
	}
}
