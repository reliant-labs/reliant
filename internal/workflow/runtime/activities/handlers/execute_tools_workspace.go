// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// workspaceEnsurer is what a batch needs from the tool executor to recover the
// directory its calls run in: a way to ask the daemon that runs them (see
// toolexec.WorkspaceEnsureCommand). toolexec.RemoteExecutor has it; an
// executor without it skips recovery and runs the batch as it always did.
type workspaceEnsurer interface {
	EnsureWorkspace(ctx context.Context, userID string, selector *toolexec.DaemonSelector, req toolexec.WorkspaceEnsureRequest) (*toolexec.WorkspaceEnsureResponse, error)
}

// batchWorkspace is where a batch's calls run, as the daemon answered, and
// what the model has to be told about it.
type batchWorkspace struct {
	// path replaces the run's working directory for every call of the batch;
	// "" leaves it as it was.
	path string
	// note goes in front of the result of the batch's first call that touches
	// the machine (noteIndex); "" says nothing.
	note      string
	noteIndex int
}

// ensureBatchWorkspace checks — and, when it is gone, recovers — the directory
// a batch's calls are about to run in, ONCE, before any of them runs.
//
// This is the point a chat's working directory is resolved for its tools, and
// the only one that both knows what the directory should hold (the worktree's
// branch and repos are in the database) and can reach the machine it lives on.
// A worktree that vanished — deleted, GC'd, a detached volume — used to make
// every call fail before running ("working directory does not exist"), turn
// after turn, with nothing able to break the loop. Now the owning daemon
// recreates it from its branch, or names another directory that exists, and
// the batch runs there.
//
// Once per batch rather than per call: one round trip a turn, and the batch's
// parallel calls cannot race each other into two repairs. A batch that never
// touches the machine asks it nothing — that round trip could wake nothing,
// but would still be paid for no reason. Every failure (a machine that is
// asleep or too old to know the command) leaves the batch exactly as it was.
func (a *ExecuteToolsActivity) ensureBatchWorkspace(
	ctx context.Context,
	rtx *types.RuntimeContext,
	calls []message.ToolCall,
	skip func(message.ToolCall) bool,
) batchWorkspace {
	out := batchWorkspace{noteIndex: -1}
	ensurer, ok := a.toolExecutor.(workspaceEnsurer)
	if !ok || rtx == nil || rtx.ChatID == "" {
		return out
	}
	for i, call := range calls {
		if !skip(call) && tools.NeedsMachine(call.Name) {
			out.noteIndex = i
			break
		}
	}
	if out.noteIndex < 0 {
		return out
	}

	chat, err := a.repo.GetChat(ctx, rtx.ChatID)
	if err != nil || chat.NoMachine || chat.ProjectID == "" {
		return out
	}
	project, err := a.repo.GetProject(ctx, chat.ProjectID)
	if err != nil || project == nil {
		return out
	}
	var worktree *db.Worktree
	if chat.WorktreeID != nil && *chat.WorktreeID != "" {
		if wt, err := a.repo.GetWorktree(ctx, *chat.WorktreeID); err == nil {
			worktree = wt
		}
	}

	// The directory the calls would run in: buildToolRequest's own priority.
	path := rtx.ProjectPath
	if path == "" {
		path = project.Path
		if worktree != nil && worktree.Path != "" {
			path = worktree.Path
		}
	}
	if path == "" {
		return out
	}

	req := toolexec.WorkspaceEnsureRequest{Path: path, Audience: rtx.Thread}
	if req.Audience == "" {
		req.Audience = rtx.ChatID
	}
	if !samePath(path, project.Path) {
		req.FallbackPath = project.Path
	}
	// Only the chat's own worktree is recreated — a directory reliant made for
	// it, whose branch and repos are on record. The main checkout, and a run
	// pointed somewhere else, only fall back.
	// A repo list that cannot be read leaves the request without a repair:
	// rebuilding a workspace from a guess at its layout is worse than
	// falling back.
	if worktree != nil && !worktree.IsMain && worktree.DeletedAt == nil && worktree.Path != "" && samePath(path, worktree.Path) {
		if repos, err := a.repo.ListReposByProject(ctx, project.ID); err == nil {
			req.Repair = true
			req.WorktreeID = worktree.ID
			req.Branch = worktree.Branch
			req.Repos = toolexec.WorkspaceReposOf(project.Path, repos)
		}
	}

	// Routed exactly as the calls it precedes (toolDaemonSelector): the check
	// must look at the disk they will run on.
	worktreeDaemon := ""
	if worktree != nil && worktree.DaemonID != nil {
		worktreeDaemon = *worktree.DaemonID
	}
	resp, err := ensureWorkspace(ctx, ensurer, project.UserID, toolDaemonSelector(worktreeDaemon, rtx.DaemonSelector), req)
	if err != nil {
		logging.Warn("[ExecuteTools] Could not check the batch's working directory; running it as is",
			"chatID", rtx.ChatID, "path", path, "error", err)
		return out
	}
	if resp.Path != "" && !samePath(resp.Path, path) {
		out.path = resp.Path
	}
	out.note = workspaceNoticeText(resp.Notice, project)
	if resp.Status != toolexec.WorkspacePresent {
		logging.Info("[ExecuteTools] Batch working directory was missing",
			"chatID", rtx.ChatID, "status", resp.Status, "path", path, "runningIn", resp.Path, "detail", resp.Detail)
	}
	return out
}

// ensureWorkspace is one bounded ensure. The check is a pre-flight the batch
// can do without, so even a panic in the transport is turned into an error
// here rather than taking every call of the batch down with it.
func ensureWorkspace(ctx context.Context, ensurer workspaceEnsurer, userID string, selector *toolexec.DaemonSelector, req toolexec.WorkspaceEnsureRequest) (resp *toolexec.WorkspaceEnsureResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("[ExecuteTools] Workspace check panicked", "path", req.Path, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			resp, err = nil, fmt.Errorf("workspace check panicked: %v", r)
		}
	}()
	ensureCtx, cancel := context.WithTimeout(ctx, time.Duration(toolexec.WorkspaceEnsureTimeoutMs)*time.Millisecond)
	defer cancel()
	return ensurer.EnsureWorkspace(ensureCtx, userID, selector, req)
}

func samePath(a, b string) bool {
	return a != "" && b != "" && filepath.Clean(a) == filepath.Clean(b)
}

// workspaceNoticeText words a workspace change for the model. It is what lets
// a conversation continue after its directory went away: the model learns
// where it is now, what was lost, and how the branch comes back — instead of
// a wall of identical errors it cannot act on.
func workspaceNoticeText(n *toolexec.WorkspaceNotice, project *db.Project) string {
	if n == nil {
		return ""
	}
	subject := "This chat's working directory"
	branch := ""
	if n.Branch != "" {
		subject = "This chat's workspace"
		branch = fmt.Sprintf(" (branch `%s`)", n.Branch)
	}
	var b strings.Builder
	switch n.Kind {
	case toolexec.NoticeRepaired:
		fmt.Fprintf(&b, "[Workspace recreated] %s %s had disappeared from disk, so it was recreated from branch `%s`%s. "+
			"Committed work on the branch is intact. Uncommitted changes, untracked files (.env and the like) and build outputs "+
			"that were in the old directory are gone: check `git status` and `git log` before relying on earlier results.",
			subject, n.WorkspacePath, n.Branch, checkoutList(n.Checkouts))
	case toolexec.NoticeFallback:
		fmt.Fprintf(&b, "[Workspace missing] %s %s%s no longer exists on this machine and could not be recreated", subject, n.WorkspacePath, branch)
		if n.Reason != "" {
			fmt.Fprintf(&b, ": %s", strings.TrimSpace(n.Reason))
		}
		fmt.Fprintf(&b, ". Instead, this call ran in %s.", describeFallback(n, project))
		b.WriteString(" Uncommitted work that was in the missing directory is lost unless it comes back.")
		inMainCheckout := project != nil && samePath(n.RunningIn, project.Path)
		switch {
		case n.Branch != "" && inMainCheckout:
			fmt.Fprintf(&b, " To get the branch back: if `%s` was pushed, fetch it in the project's repositories; once it exists, "+
				"the user can choose \"Recreate workspace\" in the chat's menu to rebuild the workspace from it, "+
				"or \"Move to main checkout\" to keep working where this call ran.", n.Branch)
		case project != nil && project.Path != "" && !samePath(n.WorkspacePath, project.Path):
			fmt.Fprintf(&b, " The project's checkout has to be back on this machine (cloned again to %s if it does not reappear) "+
				"before the branch can be checked out again; then the user can choose \"Recreate workspace\" in the chat's menu.", project.Path)
		case project != nil && project.Path != "":
			fmt.Fprintf(&b, " The project's checkout has to be back on this machine (cloned again to %s if it does not reappear).", project.Path)
		}
		fmt.Fprintf(&b, " Until then every call runs in %s.", n.RunningIn)
	case toolexec.NoticeIncomplete:
		fmt.Fprintf(&b, "[Workspace incomplete] %s %s%s is missing %s, which could not be recreated", subject, n.WorkspacePath, branch, checkoutNames(n.Checkouts))
		if n.Reason != "" {
			fmt.Fprintf(&b, ": %s", strings.TrimSpace(n.Reason))
		}
		b.WriteString(". Calls still run in the workspace; anything in the missing checkouts is unavailable.")
	case toolexec.NoticeRestored:
		fmt.Fprintf(&b, "[Workspace restored] %s %s is available again, and calls run there again.", subject, n.WorkspacePath)
		if n.PreviouslyRunningIn != "" {
			fmt.Fprintf(&b, " Changes made while it was missing were made in %s and are not in this workspace.", n.PreviouslyRunningIn)
		}
	default:
		return ""
	}
	b.WriteString("\n\n")
	return b.String()
}

// describeFallback says what the directory a call fell back to is.
func describeFallback(n *toolexec.WorkspaceNotice, project *db.Project) string {
	dir := n.RunningIn
	if project == nil || project.Path == "" {
		return dir
	}
	if samePath(dir, project.Path) {
		s := fmt.Sprintf("the project's main checkout, %s, which other chats share", dir)
		if n.Branch != "" {
			s += fmt.Sprintf(" and which is NOT on branch `%s` (check `git status` before changing anything there)", n.Branch)
		}
		return s
	}
	if samePath(n.WorkspacePath, project.Path) {
		return dir
	}
	return fmt.Sprintf("%s, because the project's checkout %s is missing too", dir, project.Path)
}

func checkoutList(rels []string) string {
	if len(rels) <= 1 && (len(rels) == 0 || rels[0] == "") {
		return ""
	}
	return " (" + checkoutNames(rels) + ")"
}

func checkoutNames(rels []string) string {
	names := make([]string, 0, len(rels))
	for _, r := range rels {
		if r == "" {
			r = "the root checkout"
		}
		names = append(names, r)
	}
	return strings.Join(names, ", ")
}
