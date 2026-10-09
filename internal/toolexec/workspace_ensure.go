// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// WorkspaceEnsureCommand is the daemon command that checks the directory a
// chat's tools are about to run in, and recovers it when it is gone.
//
// A chat is bound to a directory — its worktree, or the project's main
// checkout — and every tool, shell and background process it starts runs
// there. When that directory disappears from under the daemon (a deleted
// worktree, a GC, a crash, a volume that detached), every call used to fail
// before running with "working directory does not exist", forever, and a
// branch of the chat inherited the same dead binding. Only the daemon can see
// the filesystem, so the check and the repair happen there; the caller sends
// what it knows from the database (the worktree's branch, the project's repos)
// and runs where the daemon answers.
const WorkspaceEnsureCommand = "worktree.ensure"

// WorkspaceEnsureTimeoutMs bounds one ensure. A healthy directory answers with
// a stat; a repair is one `git worktree add` per nested repo, which on a large
// repository takes seconds each.
const WorkspaceEnsureTimeoutMs int32 = 120_000

// WorkspaceEnsureRepo is one checkout of a workspace: the repository it is a
// worktree of, and its path under the workspace root ("" = the root itself,
// for a single-repo project).
type WorkspaceEnsureRepo struct {
	RepoPath string `json:"repo_path"`
	Rel      string `json:"rel"`
}

// WorkspaceReposOf describes a project's checkouts as one of its worktree
// workspaces holds them: one per nested repo at its relative path, or — for a
// project with no nested repos recorded — the project itself at the root.
func WorkspaceReposOf(projectPath string, repos []*core.Repo) []WorkspaceEnsureRepo {
	out := make([]WorkspaceEnsureRepo, 0, len(repos))
	for _, r := range repos {
		if r == nil {
			continue
		}
		out = append(out, WorkspaceEnsureRepo{
			RepoPath: filepath.Join(projectPath, r.RelativePath),
			Rel:      filepath.ToSlash(r.RelativePath),
		})
	}
	if len(out) == 0 {
		out = append(out, WorkspaceEnsureRepo{RepoPath: projectPath, Rel: ""})
	}
	return out
}

// WorkspaceEnsureRequest asks the daemon to make Path usable.
type WorkspaceEnsureRequest struct {
	// Path is the directory the caller is about to run in.
	Path string `json:"path"`

	// Repair allows a missing Path to be recreated from Branch, one checkout
	// per entry of Repos. Set only for a worktree reliant created for the
	// chat; anything else (the main checkout, an override) only falls back.
	Repair     bool                  `json:"repair,omitempty"`
	WorktreeID string                `json:"worktree_id,omitempty"`
	Branch     string                `json:"branch,omitempty"`
	Repos      []WorkspaceEnsureRepo `json:"repos,omitempty"`

	// FallbackPath is where to run when Path is gone and cannot be recreated:
	// the project's main checkout. When it is empty or missing too, the daemon
	// falls back to its user's home directory.
	FallbackPath string `json:"fallback_path,omitempty"`

	// Audience names who is asking — the thread a tool batch runs for. A
	// notice is handed to each audience once. Empty never consumes one, so a
	// repair the user asks for from the UI is still reported to every thread.
	Audience string `json:"audience,omitempty"`
}

// Workspace statuses.
const (
	// WorkspacePresent: Path exists; nothing was done.
	WorkspacePresent = "present"
	// WorkspaceRepaired: Path was missing and has been recreated from its branch.
	WorkspaceRepaired = "repaired"
	// WorkspaceFallback: Path is missing and could not be recreated; the
	// response's Path is somewhere else that exists.
	WorkspaceFallback = "fallback"
	// WorkspaceIncomplete: Path exists, but some of its checkouts are missing
	// and could not be recreated. Calls still run in Path.
	WorkspaceIncomplete = "incomplete"
)

// WorkspaceEnsureResponse is the daemon's answer.
type WorkspaceEnsureResponse struct {
	Status string `json:"status"`
	// Path is the directory to run in: the requested one unless Status is
	// WorkspaceFallback.
	Path string `json:"path"`
	// Detail says why a repair was not possible, when it was not.
	Detail string `json:"detail,omitempty"`
	// Notice is what happened to this workspace that the request's Audience
	// has not been told yet. Nil when there is nothing new to say.
	Notice *WorkspaceNotice `json:"notice,omitempty"`
}

// Notice kinds. Each is a change the model has to know about, because the
// directory it believed it was working in is not the one it is working in.
const (
	NoticeRepaired   = WorkspaceRepaired
	NoticeFallback   = WorkspaceFallback
	NoticeIncomplete = WorkspaceIncomplete
	// NoticeRestored: the workspace came back after a fallback (a volume
	// re-attached), and calls run in it again.
	NoticeRestored = "restored"
)

// WorkspaceNotice describes one change to a workspace, as facts. The caller
// words it for whoever reads it.
type WorkspaceNotice struct {
	Kind string `json:"kind"`
	// WorkspacePath is the chat's own directory, the one that went missing.
	WorkspacePath string `json:"workspace_path"`
	// RunningIn is where calls run now.
	RunningIn string `json:"running_in"`
	// PreviouslyRunningIn is, for NoticeRestored, where calls ran while the
	// workspace was missing.
	PreviouslyRunningIn string `json:"previously_running_in,omitempty"`
	Branch              string `json:"branch,omitempty"`
	// Checkouts names the checkouts (relative to WorkspacePath; "" is the
	// root) that were recreated, or for NoticeIncomplete, that are missing.
	Checkouts []string `json:"checkouts,omitempty"`
	// Reason says why the workspace could not be recreated.
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// EnsureWorkspace asks the daemon that runs a chat's tools to make their
// working directory usable before they run — see WorkspaceEnsureCommand. It is
// routed exactly like the tools it precedes: selector nil means default
// resolution. Like every tool-time send it never wakes a machine; a machine
// that is asleep answers with an error, and the caller carries on without it.
func (e *RemoteExecutor) EnsureWorkspace(ctx context.Context, userID string, selector *DaemonSelector, req WorkspaceEnsureRequest) (*WorkspaceEnsureResponse, error) {
	if e.router == nil {
		return nil, fmt.Errorf("daemon router not configured: cannot ensure workspace %q", req.Path)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", WorkspaceEnsureCommand, err)
	}
	raw, err := SendDaemonCommandForSelector(ctx, e.router, userID, selector, WorkspaceEnsureCommand, payload, WorkspaceEnsureTimeoutMs)
	if err != nil {
		return nil, err
	}
	var resp WorkspaceEnsureResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal %s response: %w", WorkspaceEnsureCommand, err)
	}
	return &resp, nil
}
