// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/copypath"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/gitutil"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

func init() {
	RegisterCommand("worktree.generate_repo_id", handleGenerateRepoID)
	RegisterCommand("worktree.validate_path", handleValidatePath)
	RegisterCommand("worktree.create", handleWorktreeCreate)
	RegisterCommand("worktree.copy_paths", handleWorktreeCopyPaths)
	RegisterCommand("worktree.force_cleanup", handleWorktreeForceCleanup)
	RegisterCommand("worktree.delete_directory", handleWorktreeDeleteDirectory)
	RegisterCommand("worktree.remove_workspace_dir", handleWorktreeRemoveWorkspaceDir)
	RegisterCommand("worktree.delete_branch", handleWorktreeDeleteBranch)
	RegisterCommand("worktree.import_validate", handleWorktreeImportValidate)
	RegisterCommand("worktree.discover", handleWorktreeDiscover)
	RegisterCommand("worktree.recreate", handleWorktreeRecreate)
	RegisterCommand("worktree.git_changes", handleWorktreeGitChanges)
	RegisterCommand("worktree.git_status", handleWorktreeGitStatus)
	RegisterCommand("worktree.git_commits", handleWorktreeGitCommits)
	RegisterCommand("worktree.stage", handleWorktreeStage)
	RegisterCommand("worktree.unstage", handleWorktreeUnstage)
	RegisterCommand("worktree.commit", handleWorktreeCommit)
	RegisterCommand("worktree.push", handleWorktreePush)
	RegisterCommand("worktree.pull", handleWorktreePull)
	RegisterCommand("worktree.get_pr", handleWorktreeGetPR)
	RegisterCommand("worktree.create_pr", handleWorktreeCreatePR)
	RegisterCommand("worktree.revert", handleWorktreeRevert)
	RegisterCommand("worktree.get_default_branch", handleWorktreeGetDefaultBranch)
	RegisterCommand("worktree.reconcile", handleWorktreeReconcile)
	RegisterCommand("worktree.snapshot_remove", handleWorktreeSnapshotRemove)
}

// =============================================================================
// worktree.generate_repo_id
// =============================================================================

type generateRepoIDRequest struct {
	ProjectPath string `json:"project_path"`
}

type generateRepoIDResponse struct {
	RepoID string `json:"repo_id"`
}

func handleGenerateRepoID(ctx context.Context, payload []byte) ([]byte, error) {
	var req generateRepoIDRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
	cmd.Dir = req.ProjectPath
	output, err := cmd.Output()

	var input string
	if err == nil && len(output) > 0 {
		input = strings.TrimSpace(string(output))
	} else {
		abs, err := filepath.Abs(req.ProjectPath)
		if err != nil {
			return nil, fmt.Errorf("failed to get absolute path: %w", err)
		}
		input = abs
	}

	hash := sha256.Sum256([]byte(input))
	resp := generateRepoIDResponse{
		RepoID: hex.EncodeToString(hash[:])[:12],
	}
	return json.Marshal(resp)
}

// =============================================================================
// worktree.validate_path
// =============================================================================

type validatePathRequest struct {
	Path string `json:"path"`
}

type validatePathResponse struct {
	Exists bool   `json:"exists"`
	Error  string `json:"error,omitempty"`
}

func handleValidatePath(_ context.Context, payload []byte) ([]byte, error) {
	var req validatePathRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	resp := validatePathResponse{}
	if _, err := os.Stat(req.Path); err != nil {
		if os.IsNotExist(err) {
			resp.Exists = false
			resp.Error = "not_found"
		} else {
			resp.Exists = false
			resp.Error = err.Error()
		}
	} else {
		resp.Exists = true
	}
	return json.Marshal(resp)
}

// =============================================================================
// worktree.create
// =============================================================================

type worktreeCreateRequest struct {
	ProjectPath string `json:"project_path"`
	// RepoID is a daemon-side hash used to construct the legacy default
	// worktree path when WorkspaceID is not provided. Kept for callers that
	// still rely on the old <HOME>/.reliant/worktrees/<repo_id>/<name> layout.
	RepoID string `json:"repo_id"`
	Name   string `json:"name"`
	Branch string `json:"branch"`
	// WorkspaceID, when set, switches to the multi-repo layout:
	//   <HOME>/.reliant/worktrees/<workspace_id>/<sub_path>
	// where sub_path is the nested repo's relative path within the project
	// (or just <name> for single-repo workspaces). This is how the workspace
	// service fans N repos into one workspace dir.
	WorkspaceID string `json:"workspace_id,omitempty"`
	// SubPath is the path component under the workspace dir for this repo's
	// checkout. Empty means the workspace root itself (single-repo project).
	SubPath    string `json:"sub_path,omitempty"`
	BaseBranch string `json:"base_branch"`
	Force      bool   `json:"force"`
	// WorktreeID is the id of the worktree row this checkout belongs to; it is
	// written into the git lock reason. Empty skips locking.
	WorktreeID string `json:"worktree_id,omitempty"`
}

type worktreeCreateResponse struct {
	Success      bool   `json:"success"`
	WorktreePath string `json:"worktree_path,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"` // actually resolved base branch (after auto-detect)
	Output       string `json:"output,omitempty"`
	Error        string `json:"error,omitempty"`
}

func handleWorktreeCreate(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeCreateRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// Default branch name if not provided
	if req.Branch == "" {
		req.Branch = fmt.Sprintf("worktree/%s-%d", req.Name, time.Now().Unix())
	}

	// Default base branch if not provided. Detect per-repo so projects with
	// repos on master/develop/etc. don't fail with "invalid reference 'main'".
	if req.BaseBranch == "" {
		req.BaseBranch = getRepositoryDefaultBranch(ctx, req.ProjectPath)
	}

	// A repo with an unborn HEAD (created by `git init` with no commit — the
	// new-project auto-init path, or a user's manual `git init`) has .git but
	// no ref for the base branch to resolve, so `git worktree add` below fails
	// with "invalid reference". Give it a root commit first. This is a no-op
	// once the repo has any commit. Base the worktree off the branch that now
	// carries the commit so we don't reintroduce the same unresolved-ref error
	// if auto-detect guessed a name that differs from the unborn branch.
	if !gitutil.HasCommits(ctx, req.ProjectPath) {
		if err := gitutil.EnsureInitialCommit(ctx, req.ProjectPath); err != nil {
			return json.Marshal(worktreeCreateResponse{Error: fmt.Sprintf("failed to seed initial commit: %v", err)})
		}
		if current, err := gitutil.GetCurrentBranch(ctx, req.ProjectPath); err == nil && current != "" {
			req.BaseBranch = current
		}
	}

	// Resolve the on-disk worktree path.
	//
	// Multi-repo (workspace) layout: WorkspaceID set →
	//   <HOME>/.reliant/worktrees/<workspace_id>/<sub_path>
	// Legacy single-repo layout: WorkspaceID empty →
	//   <HOME>/.reliant/worktrees/<repo_id>/<name>
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return json.Marshal(worktreeCreateResponse{Error: fmt.Sprintf("failed to get home directory: %v", err)})
	}
	var worktreePath string
	if req.WorkspaceID != "" {
		// Sub-path may be empty for single-repo workspaces — checkout lands
		// at the workspace root itself.
		if req.SubPath == "" {
			worktreePath = filepath.Join(homeDir, ".reliant", "worktrees", req.WorkspaceID)
		} else {
			worktreePath = filepath.Join(homeDir, ".reliant", "worktrees", req.WorkspaceID, req.SubPath)
		}
	} else {
		worktreePath = filepath.Join(homeDir, ".reliant", "worktrees", req.RepoID, req.Name)
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return json.Marshal(worktreeCreateResponse{Error: fmt.Sprintf("failed to create worktree directory: %v", err)})
	}

	// Sanity check for the multi-repo workspace layout: the SAME workspace_id
	// shared across N daemon calls is by design (each call targets a different
	// sub_path). Two calls with the same (workspace_id, sub_path) WOULD
	// collide — git worktree add into a non-empty dir errors out. Force=true
	// wipes via -B so we don't pre-empt.

	// Check if branch exists
	branchCheckCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", req.Branch)
	branchCheckCmd.Dir = req.ProjectPath
	branchExists := branchCheckCmd.Run() == nil

	// Create the git worktree
	var worktreeCmd *exec.Cmd
	if branchExists || req.Force {
		worktreeCmd = exec.CommandContext(ctx, "git", "worktree", "add", "-B", req.Branch, worktreePath, req.BaseBranch)
	} else {
		worktreeCmd = exec.CommandContext(ctx, "git", "worktree", "add", "-b", req.Branch, worktreePath, req.BaseBranch)
	}
	worktreeCmd.Dir = req.ProjectPath

	checkoutStart := time.Now()
	if output, err := worktreeCmd.CombinedOutput(); err != nil {
		// Roll back the directory this call created.
		//
		// MkdirAll above runs BEFORE git, so a failing `git worktree add`
		// leaves an empty directory that looks like a real workspace to
		// everything downstream. That is not hypothetical: a branch named
		// `release` in a repo that already has `release/compute-tier-redesign`
		// cannot be created (git stores refs as files, so `refs/heads/release`
		// cannot be both a file and a directory), and the failure left
		// `~/.reliant/worktrees/<id>/` on disk with no .git inside it, beside
		// a DB row whose path was never filled in. Every filesystem RPC scoped
		// to that worktree then failed with "no workspace path to scope this
		// request to", which reads as a database problem and is not one.
		//
		// Removed only when EMPTY. git may have written a partial checkout
		// before failing, and in a --force flow the directory can predate this
		// call entirely; deleting a non-empty directory here could destroy
		// work this function did not create. An empty directory is
		// unambiguously ours and unambiguously useless.
		//
		// BOTH levels are swept, because MkdirAll above creates
		// filepath.Dir(worktreePath), not worktreePath itself:
		//
		//   single-repo (sub_path "")  -> parent is .../worktrees, shared;
		//                                 the leaf is git's to create.
		//   multi-repo  (sub_path set) -> parent IS the workspace root
		//                                 .../worktrees/<workspace_id>, and
		//                                 that is the directory this call
		//                                 invented and must take back.
		//
		// The reported orphan was the multi-repo shape: the workspace root
		// survived as an empty directory. Sweeping the leaf first and then the
		// parent covers both without a special case, and os.Remove refuses a
		// non-empty directory, so the shared .../worktrees parent is never at
		// risk.
		removeIfEmpty := func(dir string) {
			if entries, rerr := os.ReadDir(dir); rerr == nil && len(entries) == 0 {
				_ = os.Remove(dir)
			}
		}
		removeIfEmpty(worktreePath)
		if parent := filepath.Dir(worktreePath); parent != worktreePath {
			removeIfEmpty(parent)
		}
		return json.Marshal(worktreeCreateResponse{
			Error: string(output),
		})
	}

	// Mark the checkout reliant-owned. forge's storage reaper skips locked
	// worktrees, so this lock is what protects a clean, idle, pushed checkout
	// from being removed under an active chat. Failing to lock is not fatal to
	// creation: the reconcile pass locks every active worktree on its next run.
	if req.WorktreeID != "" {
		if err := worktreereclaim.LockCheckout(ctx, worktreePath, req.WorktreeID); err != nil {
			logging.Warn("worktree created but not locked; reconcile will retry",
				"worktree_path", worktreePath, "error", err)
		}
	}

	// A new workspace waits on this whole command; say how long git took, so
	// a slow create can be told apart from a slow copy (worktree.copy_paths
	// logs its own time).
	logging.Info("worktree checked out",
		"worktree_path", worktreePath,
		"elapsed", time.Since(checkoutStart).Round(time.Millisecond).String())

	return json.Marshal(worktreeCreateResponse{Success: true, WorktreePath: worktreePath, BaseBranch: req.BaseBranch})
}

// =============================================================================
// worktree.copy_paths
// =============================================================================
//
// Copies named paths from a source tree into a freshly created workspace —
// the gitignored pieces (.env, node_modules, local config) a checkout does
// not bring. It runs ONCE per workspace, after every repo is checked out,
// between the two workspace ROOTS: the project root (or the workspace being
// branched from) and the new workspace root. The workspace mirrors the
// project's layout, so an entry like `reliant/.env` lands inside the reliant
// checkout and a root-level `.env` lands at the workspace root, with no
// per-repo routing.
//
// Entries are exact paths; nothing is searched for. See copypath.

type worktreeCopyPathsRequest struct {
	SourceRoot string   `json:"source_root"`
	DestRoot   string   `json:"dest_root"`
	Paths      []string `json:"paths"`
}

type worktreeCopyPathsResponse struct {
	Copied  []string `json:"copied,omitempty"`
	Missing []string `json:"missing,omitempty"`
	// Failed maps an entry that exists but could not be copied to the reason.
	Failed map[string]string `json:"failed,omitempty"`
	Error  string            `json:"error,omitempty"`
}

func handleWorktreeCopyPaths(_ context.Context, payload []byte) ([]byte, error) {
	var req worktreeCopyPathsRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.SourceRoot == "" || req.DestRoot == "" {
		return json.Marshal(worktreeCopyPathsResponse{Error: "source_root and dest_root are required"})
	}
	// The server validates too; this is the boundary that touches the disk,
	// so it does not trust that it did.
	paths, err := copypath.CleanAll(req.Paths)
	if err != nil {
		return json.Marshal(worktreeCopyPathsResponse{Error: err.Error()})
	}

	start := time.Now()
	result := copypath.Copy(req.SourceRoot, req.DestRoot, paths)
	resp := worktreeCopyPathsResponse{Copied: result.Copied, Missing: result.Missing}
	for _, failure := range result.Failed {
		if resp.Failed == nil {
			resp.Failed = make(map[string]string, len(result.Failed))
		}
		resp.Failed[failure.Path] = failure.Err.Error()
	}
	logging.Info("worktree paths copied",
		"dest_root", req.DestRoot,
		"elapsed", time.Since(start).Round(time.Millisecond).String(),
		"copied", resp.Copied, "missing", resp.Missing, "failed", resp.Failed)
	return json.Marshal(resp)
}

// =============================================================================
// worktree.force_cleanup
// =============================================================================

type worktreeForceCleanupRequest struct {
	ProjectPath  string `json:"project_path"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
}

type worktreeForceCleanupResponse struct {
	Success bool `json:"success"`
}

func handleWorktreeForceCleanup(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeForceCleanupRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// Check if git worktree path exists and remove it
	if _, err := os.Stat(req.WorktreePath); err == nil {
		worktreereclaim.UnlockForRemoval(ctx, req.WorktreePath)
		removeCmd := exec.CommandContext(ctx, "git", "worktree", "remove", req.WorktreePath, "--force")
		removeCmd.Dir = req.ProjectPath
		_, _ = removeCmd.CombinedOutput()
		_ = os.RemoveAll(req.WorktreePath)
	}

	// Prune stale worktrees
	pruneCmd := exec.CommandContext(ctx, "git", "worktree", "prune")
	pruneCmd.Dir = req.ProjectPath
	_, _ = pruneCmd.CombinedOutput()

	// Check if branch exists and delete it
	branchCheckCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", req.Branch)
	branchCheckCmd.Dir = req.ProjectPath
	if branchCheckCmd.Run() == nil {
		delBranchCmd := exec.CommandContext(ctx, "git", "branch", "-D", req.Branch)
		delBranchCmd.Dir = req.ProjectPath
		_, _ = delBranchCmd.CombinedOutput()
	}

	return json.Marshal(worktreeForceCleanupResponse{Success: true})
}

// =============================================================================
// worktree.delete_directory
// =============================================================================

type worktreeDeleteDirectoryRequest struct {
	ProjectPath  string `json:"project_path"`
	WorktreePath string `json:"worktree_path"`
}

type worktreeDeleteDirectoryResponse struct {
	Deleted bool `json:"deleted"`
}

func handleWorktreeDeleteDirectory(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeDeleteDirectoryRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	worktreereclaim.UnlockForRemoval(ctx, req.WorktreePath)
	removeCmd := exec.CommandContext(ctx, "git", "worktree", "remove", req.WorktreePath, "--force")
	removeCmd.Dir = req.ProjectPath
	if _, err := removeCmd.CombinedOutput(); err != nil {
		if err := os.RemoveAll(req.WorktreePath); err != nil {
			return json.Marshal(worktreeDeleteDirectoryResponse{Deleted: false})
		}
	}

	return json.Marshal(worktreeDeleteDirectoryResponse{Deleted: true})
}

// =============================================================================
// worktree.remove_workspace_dir
// =============================================================================
//
// Removes the workspace root directory itself after all per-repo
// `worktree.delete_directory` calls have unregistered the nested checkouts
// from their parent repos. Pure os.RemoveAll — no git interaction.

type worktreeRemoveWorkspaceDirRequest struct {
	WorkspacePath string `json:"workspace_path"`
}

type worktreeRemoveWorkspaceDirResponse struct {
	Deleted bool   `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

func handleWorktreeRemoveWorkspaceDir(_ context.Context, payload []byte) ([]byte, error) {
	var req worktreeRemoveWorkspaceDirRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	if req.WorkspacePath == "" {
		return json.Marshal(worktreeRemoveWorkspaceDirResponse{Deleted: false, Error: "workspace_path is required"})
	}

	if err := os.RemoveAll(req.WorkspacePath); err != nil {
		return json.Marshal(worktreeRemoveWorkspaceDirResponse{Deleted: false, Error: err.Error()})
	}
	return json.Marshal(worktreeRemoveWorkspaceDirResponse{Deleted: true})
}

// =============================================================================
// worktree.delete_branch
// =============================================================================

type worktreeDeleteBranchRequest struct {
	ProjectPath string `json:"project_path"`
	Branch      string `json:"branch"`
}

type worktreeDeleteBranchResponse struct {
	Deleted bool `json:"deleted"`
}

func handleWorktreeDeleteBranch(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeDeleteBranchRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	delBranchCmd := exec.CommandContext(ctx, "git", "branch", "-D", req.Branch)
	delBranchCmd.Dir = req.ProjectPath
	if _, err := delBranchCmd.CombinedOutput(); err != nil {
		return json.Marshal(worktreeDeleteBranchResponse{Deleted: false})
	}

	return json.Marshal(worktreeDeleteBranchResponse{Deleted: true})
}

// =============================================================================
// worktree.import_validate
// =============================================================================

type worktreeImportValidateRequest struct {
	Path string `json:"path"`
}

type worktreeImportValidateResponse struct {
	Valid      bool   `json:"valid"`
	AbsPath    string `json:"abs_path"`
	Branch     string `json:"branch"`
	BaseBranch string `json:"base_branch"`
	Error      string `json:"error,omitempty"`
}

func handleWorktreeImportValidate(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeImportValidateRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	resp := worktreeImportValidateResponse{}

	absPath, err := filepath.Abs(req.Path)
	if err != nil {
		resp.Error = fmt.Sprintf("invalid path: %v", err)
		return json.Marshal(resp)
	}
	resp.AbsPath = absPath

	if _, err := os.Stat(absPath); err != nil {
		resp.Error = fmt.Sprintf("path does not exist: %v", err)
		return json.Marshal(resp)
	}

	gitDirPath := filepath.Join(absPath, ".git")
	if _, err := os.Stat(gitDirPath); err != nil {
		resp.Error = "path is not a git worktree (missing .git)"
		return json.Marshal(resp)
	}

	// Get current branch
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = absPath
	branchOutput, err := branchCmd.Output()
	if err != nil {
		resp.Error = fmt.Sprintf("failed to get current branch: %v", err)
		return json.Marshal(resp)
	}
	resp.Branch = strings.TrimSpace(string(branchOutput))

	// Try to determine base branch from remote tracking
	trackingCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	trackingCmd.Dir = absPath
	if trackingOutput, err := trackingCmd.Output(); err == nil {
		tracking := strings.TrimSpace(string(trackingOutput))
		parts := strings.Split(tracking, "/")
		if len(parts) > 1 {
			resp.BaseBranch = strings.Join(parts[1:], "/")
		}
	}
	if resp.BaseBranch == "" {
		resp.BaseBranch = "main"
	}

	resp.Valid = true
	return json.Marshal(resp)
}

// =============================================================================
// worktree.discover
// =============================================================================

type worktreeDiscoverRequest struct {
	ProjectPath string `json:"project_path"`
}

type discoveredWorktreeEntry struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Branch     string `json:"branch"`
	IsPrunable bool   `json:"is_prunable"`
}

type worktreeDiscoverResponse struct {
	Worktrees []discoveredWorktreeEntry `json:"worktrees"`
	Error     string                    `json:"error,omitempty"`
}

func handleWorktreeDiscover(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeDiscoverRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	cmd.Dir = req.ProjectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		errOutput := strings.TrimSpace(string(output))
		if strings.Contains(errOutput, "not a git repository") {
			return json.Marshal(worktreeDiscoverResponse{Worktrees: []discoveredWorktreeEntry{}})
		}
		return json.Marshal(worktreeDiscoverResponse{Error: errOutput})
	}

	var worktrees []discoveredWorktreeEntry
	lines := strings.Split(string(output), "\n")

	var current *discoveredWorktreeEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if current != nil {
				worktrees = append(worktrees, *current)
				current = nil
			}
			continue
		}

		if strings.HasPrefix(line, "worktree ") {
			path := strings.TrimPrefix(line, "worktree ")
			current = &discoveredWorktreeEntry{
				Path: path,
				Name: filepath.Base(path),
			}
		} else if strings.HasPrefix(line, "branch ") && current != nil {
			branchRef := strings.TrimPrefix(line, "branch ")
			parts := strings.Split(branchRef, "/")
			if len(parts) >= 3 {
				current.Branch = strings.Join(parts[2:], "/")
			} else {
				current.Branch = branchRef
			}
		} else if strings.HasPrefix(line, "prunable") && current != nil {
			current.IsPrunable = true
		}
	}
	if current != nil {
		worktrees = append(worktrees, *current)
	}

	return json.Marshal(worktreeDiscoverResponse{Worktrees: worktrees})
}

// =============================================================================
// worktree.recreate
// =============================================================================

// RecreateRepo is one repository of a workspace to rebuild.
type worktreeRecreateRepo struct {
	// RepoPath is the parent repository the checkout was created from.
	RepoPath string `json:"repo_path"`
	// Rel is the checkout's path under the workspace root ("" = the root itself).
	Rel string `json:"rel"`
	// Branch is the branch to check out. Empty falls back to the request's.
	Branch string `json:"branch,omitempty"`
	// SnapshotRefs are the refs an earlier clean-up saved this checkout's work
	// to (the newest is applied); empty means look them up by worktree id.
	SnapshotRefs []string `json:"snapshot_refs,omitempty"`
}

type worktreeRecreateRequest struct {
	// Single-repo form, kept for the simple case.
	ProjectPath  string   `json:"project_path"`
	WorktreePath string   `json:"worktree_path"`
	Branch       string   `json:"branch"`
	WorktreeID   string   `json:"worktree_id,omitempty"`
	SnapshotRefs []string `json:"snapshot_refs,omitempty"`
	// Fence is the archive being undone; the new lock retires it.
	Fence string `json:"fence,omitempty"`
	// Repos is the multi-repo form: every repository of the workspace, each
	// rebuilt at WorktreePath/<rel>. When set it replaces ProjectPath.
	Repos []worktreeRecreateRepo `json:"repos,omitempty"`
}

type worktreeRecreateResponse struct {
	Success      bool   `json:"success"`
	BranchExists bool   `json:"branch_exists"`
	PathExists   bool   `json:"path_exists"`
	Output       string `json:"output,omitempty"`
	Error        string `json:"error,omitempty"`
	// SnapshotApplied: saved work from an earlier clean-up was re-applied to
	// EVERY checkout that had some. False when any apply failed, in which case
	// the refs must be kept.
	SnapshotApplied bool `json:"snapshot_applied,omitempty"`
	// SnapshotFailed names the checkouts whose saved work could not be applied.
	SnapshotFailed []string `json:"snapshot_failed,omitempty"`
	// SnapshotBranchMoved names the checkouts whose saved work was NOT applied
	// because the branch moved since it was saved. The work stays on its ref.
	SnapshotBranchMoved []string `json:"snapshot_branch_moved,omitempty"`
	// RestoredFromQuarantine names checkouts that were parked and moved back
	// instead of being recreated.
	RestoredFromQuarantine []string `json:"restored_from_quarantine,omitempty"`
}

func handleWorktreeRecreate(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeRecreateRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	repos := req.Repos
	if len(repos) == 0 {
		repos = []worktreeRecreateRepo{{RepoPath: req.ProjectPath, Rel: "", Branch: req.Branch, SnapshotRefs: req.SnapshotRefs}}
	}
	resp := worktreeRecreateResponse{}

	// Check everything BEFORE creating anything, so a missing branch in the
	// third repo does not leave two checkouts behind.
	for _, repo := range repos {
		branch := firstNonEmptyStr(repo.Branch, req.Branch)
		check := exec.CommandContext(ctx, "git", "rev-parse", "--verify", branch)
		check.Dir = repo.RepoPath
		if err := check.Run(); err != nil {
			resp.BranchExists = false
			resp.Error = fmt.Sprintf("branch '%s' no longer exists in %s", branch, repo.RepoPath)
			return json.Marshal(resp)
		}
	}
	resp.BranchExists = true
	// A checkout parked by an interrupted clean-up is moved back, not rebuilt:
	// `git worktree add` would refuse (the branch is still checked out there).
	recovered := map[string]bool{}
	if req.WorktreeID != "" {
		if rc, err := sharedWorktreeReclaimer(); err == nil {
			back, _ := rc.RecoverQuarantined(ctx, req.WorktreeID)
			for _, dest := range back {
				recovered[dest] = true
				resp.RestoredFromQuarantine = append(resp.RestoredFromQuarantine, dest)
			}
		}
	}
	for _, repo := range repos {
		if recovered[filepath.Join(req.WorktreePath, filepath.FromSlash(repo.Rel))] {
			continue
		}
		if _, err := os.Stat(filepath.Join(req.WorktreePath, filepath.FromSlash(repo.Rel))); err == nil {
			resp.PathExists = true
			resp.Error = fmt.Sprintf("worktree directory '%s' already exists", filepath.Join(req.WorktreePath, filepath.FromSlash(repo.Rel)))
			return json.Marshal(resp)
		}
	}

	// created is parallel to repos; "" marks a checkout that was moved back
	// from quarantine rather than created here.
	created := make([]string, len(repos))
	rollback := func() {
		for i, dir := range created {
			if dir == "" {
				continue
			}
			worktreereclaim.UnlockForRemoval(ctx, dir)
			// Run from the checkout's own repository: its parent directory is
			// not a git repository.
			rm := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", dir)
			rm.Dir = repos[i].RepoPath
			_, _ = rm.CombinedOutput()
		}
	}
	for i, repo := range repos {
		dest := filepath.Join(req.WorktreePath, filepath.FromSlash(repo.Rel))
		if recovered[dest] {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			rollback()
			resp.Error = fmt.Sprintf("failed to create parent directory: %v", err)
			return json.Marshal(resp)
		}
		add := exec.CommandContext(ctx, "git", "worktree", "add", dest, firstNonEmptyStr(repo.Branch, req.Branch))
		add.Dir = repo.RepoPath
		if output, err := add.CombinedOutput(); err != nil {
			rollback()
			resp.Output = string(output)
			resp.Error = fmt.Sprintf("failed to recreate worktree: %s", strings.TrimSpace(string(output)))
			return json.Marshal(resp)
		}
		created[i] = dest
	}

	if req.WorktreeID != "" {
		for _, dest := range created {
			if dest == "" {
				continue
			}
			if err := worktreereclaim.RestoreCheckout(ctx, dest, req.WorktreeID, req.Fence); err != nil {
				logging.Warn("worktree recreated but not locked; reconcile will retry", "worktree_path", dest, "error", err)
			}
		}
	}

	// Put back the work each checkout had. Applied per checkout, from that
	// checkout's own repository, and reported honestly: the refs are only safe
	// to forget once EVERY one of them was re-applied.
	resp.SnapshotApplied = true
	anySnapshot := false
	if req.WorktreeID != "" {
		for i, repo := range repos {
			if created[i] == "" {
				continue
			}
			ref := worktreereclaim.LatestSnapshot(ctx, repo.RepoPath, req.WorktreeID, snapshotRefsFor(req.WorktreeID, repo, req.SnapshotRefs))
			if ref == "" {
				continue
			}
			anySnapshot = true
			if err := worktreereclaim.ApplySnapshot(ctx, created[i], ref); err != nil {
				resp.SnapshotApplied = false
				if errors.Is(err, worktreereclaim.ErrBranchMoved) {
					resp.SnapshotBranchMoved = append(resp.SnapshotBranchMoved, repo.Rel)
					resp.Output += fmt.Sprintf("work saved at %s was not applied because the branch moved; it is still on that ref\n", ref)
				} else {
					resp.SnapshotFailed = append(resp.SnapshotFailed, repo.Rel)
					resp.Output += fmt.Sprintf("saved work for %q was not re-applied: %v\n", repo.Rel, err)
				}
				logging.Warn("worktree recreated but its snapshot was not applied", "ref", ref, "error", err)
				continue
			}
			if err := worktreereclaim.RetireAppliedRef(ctx, repo.RepoPath, ref); err != nil {
				logging.Warn("snapshot applied but its ref could not be retired", "ref", ref, "error", err)
			}
		}
	}
	if !anySnapshot {
		resp.SnapshotApplied = false
	}
	resp.Success = true
	return json.Marshal(resp)
}

// snapshotRefsFor picks, from the refs recorded on the row, those that hold
// this repo's checkout's work. It never looks at refs the row does not name.
func snapshotRefsFor(worktreeID string, repo worktreeRecreateRepo, shared []string) []string {
	refs := shared
	if len(repo.SnapshotRefs) > 0 {
		refs = repo.SnapshotRefs
	}
	return worktreereclaim.RefsForCheckout(worktreeID, repo.Rel, refs)
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// =============================================================================
// worktree.git_changes
// =============================================================================

type worktreeGitChangesRequest struct {
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	BaseBranch   string `json:"base_branch"`
}

type worktreeFileChange struct {
	Path     string `json:"path"`
	Status   string `json:"status"` // "untracked", "staged", "modified"
	IsNew    bool   `json:"is_new"`
	Diff     string `json:"diff"`
	IsBinary bool   `json:"is_binary"`
}

type worktreeGitChangesResponse struct {
	Branch        string               `json:"branch"`
	Files         []worktreeFileChange `json:"files"`
	TotalFiles    int32                `json:"total_files"`
	Ahead         int32                `json:"ahead"`
	Behind        int32                `json:"behind"`
	DefaultBranch string               `json:"default_branch"`
	Error         string               `json:"error,omitempty"`
}

func handleWorktreeGitChanges(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeGitChangesRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// An absent worktree path leaves cmd.Dir empty, which runs git in the
	// DAEMON's working directory — a different repository entirely. For a
	// confined caller that is an escape, and it is the default path: this
	// tool's path argument is optional, so a model that simply omits it would
	// otherwise read whatever repo the daemon happens to sit in. Resolving
	// maps empty to the allowed root; a no-op for unconfined callers.
	resolvedPath, err := daemonpolicy.ResolveDir(ctx, req.WorktreePath)
	if err != nil {
		return nil, err
	}
	req.WorktreePath = resolvedPath

	resp := worktreeGitChangesResponse{Branch: req.Branch}

	// Get current branch
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = req.WorktreePath
	branchOutput, err := branchCmd.Output()
	if err == nil && len(branchOutput) > 0 {
		resp.Branch = strings.TrimSpace(string(branchOutput))
	}

	// Get ahead/behind
	ahead, behind := getAheadBehind(ctx, req.WorktreePath, resp.Branch)
	resp.Ahead = int32(ahead)
	resp.Behind = int32(behind)

	// Get git status
	statusCmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "status", "--porcelain", "--untracked-files=all")
	statusCmd.Dir = req.WorktreePath
	statusOutput, err := statusCmd.Output()
	if err != nil {
		resp.Error = fmt.Sprintf("failed to get git status: %v", err)
		return json.Marshal(resp)
	}

	// Parse git status with diffs
	files := parseGitStatusWithDiffs(ctx, req.WorktreePath, string(statusOutput))
	files = capWorktreeChangesDiffs(req.WorktreePath, files)
	resp.Files = files
	resp.TotalFiles = int32(len(files))

	// Get default branch
	resp.DefaultBranch = getRepositoryDefaultBranch(ctx, req.WorktreePath)

	return json.Marshal(resp)
}

// =============================================================================
// worktree.git_status
// =============================================================================

type worktreeGitStatusRequest struct {
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
}

type worktreeGitStatusResponse struct {
	Branch         string   `json:"branch"`
	HasChanges     bool     `json:"has_changes"`
	Status         string   `json:"status"`
	StagedFiles    []string `json:"staged_files"`
	UnstagedFiles  []string `json:"unstaged_files"`
	UntrackedFiles []string `json:"untracked_files"`
	Ahead          int32    `json:"ahead"`
	Behind         int32    `json:"behind"`
	Error          string   `json:"error,omitempty"`
}

func handleWorktreeGitStatus(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeGitStatusRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// An absent worktree path leaves cmd.Dir empty, which runs git in the
	// DAEMON's working directory — a different repository entirely. For a
	// confined caller that is an escape, and it is the default path: this
	// tool's path argument is optional, so a model that simply omits it would
	// otherwise read whatever repo the daemon happens to sit in. Resolving
	// maps empty to the allowed root; a no-op for unconfined callers.
	resolvedPath, err := daemonpolicy.ResolveDir(ctx, req.WorktreePath)
	if err != nil {
		return nil, err
	}
	req.WorktreePath = resolvedPath

	resp := worktreeGitStatusResponse{Branch: req.Branch, Status: "clean"}

	// Get current branch
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = req.WorktreePath
	if branchOutput, err := branchCmd.Output(); err == nil && len(branchOutput) > 0 {
		resp.Branch = strings.TrimSpace(string(branchOutput))
	}

	// Get git status
	statusCmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "status", "--porcelain", "--untracked-files=all")
	statusCmd.Dir = req.WorktreePath
	statusOutput, err := statusCmd.Output()
	if err != nil {
		resp.Error = fmt.Sprintf("failed to get git status: %v", err)
		return json.Marshal(resp)
	}

	lines := strings.Split(strings.TrimRight(string(statusOutput), "\n\r "), "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}
		resp.HasChanges = true
		statusCode := line[:2]
		filePath := worktreeParseGitStatusPath(line[3:])
		if strings.Contains(statusCode, "??") {
			resp.UntrackedFiles = append(resp.UntrackedFiles, filePath)
		} else if statusCode[0] != ' ' && statusCode[0] != '?' {
			resp.StagedFiles = append(resp.StagedFiles, filePath)
		} else {
			resp.UnstagedFiles = append(resp.UnstagedFiles, filePath)
		}
	}
	if resp.HasChanges {
		resp.Status = "dirty"
	}

	// Get ahead/behind
	ahead, behind := getAheadBehind(ctx, req.WorktreePath, resp.Branch)
	resp.Ahead = int32(ahead)
	resp.Behind = int32(behind)

	return json.Marshal(resp)
}

// =============================================================================
// worktree.git_commits
// =============================================================================

type worktreeGitCommitsRequest struct {
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	BaseBranch   string `json:"base_branch"`
	Limit        int32  `json:"limit"`
}

type gitCommitEntry struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`
	Author    string `json:"author"`
	Email     string `json:"email"`
	Date      string `json:"date"`
	Message   string `json:"message"`
}

type worktreeGitCommitsResponse struct {
	Commits        []gitCommitEntry `json:"commits"`
	Total          int32            `json:"total"`
	Branch         string           `json:"branch"`
	BaseBranch     string           `json:"base_branch"`
	ComparisonMode bool             `json:"comparison_mode"`
	ComparisonRef  string           `json:"comparison_ref"`
	CurrentBranch  string           `json:"current_branch"`
	Error          string           `json:"error,omitempty"`
}

func handleWorktreeGitCommits(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeGitCommitsRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}

	resp := worktreeGitCommitsResponse{
		Branch:     req.Branch,
		BaseBranch: req.BaseBranch,
	}

	// Get current branch
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = req.WorktreePath
	if branchOutput, err := branchCmd.Output(); err == nil {
		resp.CurrentBranch = strings.TrimSpace(string(branchOutput))
	}

	baseBranch := req.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	useComparisonMode := false
	comparisonRef := ""

	// Check if base branch exists
	checkRemoteCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", fmt.Sprintf("origin/%s", baseBranch))
	checkRemoteCmd.Dir = req.WorktreePath
	if err := checkRemoteCmd.Run(); err == nil {
		comparisonRef = fmt.Sprintf("origin/%s", baseBranch)
		useComparisonMode = true
	} else {
		checkLocalCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", baseBranch)
		checkLocalCmd.Dir = req.WorktreePath
		if err := checkLocalCmd.Run(); err == nil {
			comparisonRef = baseBranch
			useComparisonMode = true
		}
	}

	resp.ComparisonMode = useComparisonMode
	resp.ComparisonRef = comparisonRef

	// Execute git log
	formatStr := "%H|%an|%ae|%ad|%s"
	var cmd *exec.Cmd
	if useComparisonMode {
		revRange := fmt.Sprintf("%s..HEAD", comparisonRef)
		cmd = exec.CommandContext(ctx, "git", "log", revRange, fmt.Sprintf("--format=%s", formatStr), "--date=iso", fmt.Sprintf("-n%d", limit))
	} else {
		cmd = exec.CommandContext(ctx, "git", "log", fmt.Sprintf("--format=%s", formatStr), "--date=iso", fmt.Sprintf("-n%d", limit))
	}
	cmd.Dir = req.WorktreePath
	output, err := cmd.Output()
	if err != nil {
		return json.Marshal(resp)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 5)
		if len(parts) != 5 {
			continue
		}
		shortHash := parts[0]
		if len(shortHash) > 7 {
			shortHash = shortHash[:7]
		}
		resp.Commits = append(resp.Commits, gitCommitEntry{
			Hash:      parts[0],
			ShortHash: shortHash,
			Author:    parts[1],
			Email:     parts[2],
			Date:      parts[3],
			Message:   parts[4],
		})
	}
	resp.Total = int32(len(resp.Commits))

	return json.Marshal(resp)
}

// =============================================================================
// worktree.stage
// =============================================================================

type worktreeStageRequest struct {
	WorktreePath string   `json:"worktree_path"`
	Files        []string `json:"files"`
}

type worktreeStageResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

func handleWorktreeStage(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeStageRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	var args []string
	if len(req.Files) == 0 || (len(req.Files) == 1 && req.Files[0] == ".") {
		args = []string{"add", "."}
	} else {
		args = append([]string{"add", "--"}, req.Files...)
	}

	// Index-writing: the shared helper gives it graceful cancellation (so a
	// cancelled stage removes its own index.lock) and clears a lock stranded
	// by an earlier death.
	if output, err := gitutil.RunIndexCommand(ctx, req.WorktreePath, args...); err != nil {
		return json.Marshal(worktreeStageResponse{Error: strings.TrimSpace(string(output))})
	}

	return json.Marshal(worktreeStageResponse{Success: true})
}

// =============================================================================
// worktree.unstage
// =============================================================================

type worktreeUnstageRequest struct {
	WorktreePath string   `json:"worktree_path"`
	Files        []string `json:"files"`
}

type worktreeUnstageResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

func handleWorktreeUnstage(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeUnstageRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	var args []string
	if len(req.Files) == 0 || (len(req.Files) == 1 && req.Files[0] == ".") {
		args = []string{"reset", "HEAD"}
	} else {
		args = append([]string{"restore", "--staged", "--"}, req.Files...)
	}

	// Index-writing (`reset HEAD` / `restore --staged`): same policy as stage.
	if output, err := gitutil.RunIndexCommand(ctx, req.WorktreePath, args...); err != nil {
		return json.Marshal(worktreeUnstageResponse{Error: strings.TrimSpace(string(output))})
	}

	return json.Marshal(worktreeUnstageResponse{Success: true})
}

// =============================================================================
// worktree.commit
// =============================================================================

type worktreeCommitRequest struct {
	WorktreePath string `json:"worktree_path"`
	Message      string `json:"message"`
}

type worktreeCommitResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

func handleWorktreeCommit(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeCommitRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// Index-writing: a cancelled commit under SIGKILL is the classic way to
	// strand index.lock, so it goes through the shared helper.
	output, err := gitutil.RunIndexCommand(ctx, req.WorktreePath, "commit", "-m", req.Message)
	if err != nil {
		return json.Marshal(worktreeCommitResponse{
			Error:  strings.TrimSpace(string(output)),
			Output: string(output),
		})
	}

	return json.Marshal(worktreeCommitResponse{
		Success: true,
		Output:  string(output),
	})
}

// =============================================================================
// worktree.push
// =============================================================================

type worktreePushRequest struct {
	WorktreePath string `json:"worktree_path"`
}

type worktreePushResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

func handleWorktreePush(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreePushRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	branch, err := resolveCurrentBranch(ctx, req.WorktreePath)
	if err != nil {
		return json.Marshal(worktreePushResponse{Error: err.Error()})
	}

	pushCmd := exec.CommandContext(ctx, "git", "push", "--set-upstream", "origin", branch)
	pushCmd.Dir = req.WorktreePath
	output, err := pushCmd.CombinedOutput()
	if err != nil {
		return json.Marshal(worktreePushResponse{
			Error:  strings.TrimSpace(string(output)),
			Output: string(output),
		})
	}

	return json.Marshal(worktreePushResponse{
		Success: true,
		Output:  string(output),
	})
}

// =============================================================================
// worktree.pull
// =============================================================================

type worktreePullRequest struct {
	WorktreePath string `json:"worktree_path"`
}

type worktreePullResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

func handleWorktreePull(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreePullRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	branch, err := resolveCurrentBranch(ctx, req.WorktreePath)
	if err != nil {
		return json.Marshal(worktreePullResponse{Error: err.Error()})
	}

	pullCmd := exec.CommandContext(ctx, "git", "pull", "origin", branch)
	pullCmd.Dir = req.WorktreePath
	output, err := pullCmd.CombinedOutput()
	if err != nil {
		return json.Marshal(worktreePullResponse{
			Error:  strings.TrimSpace(string(output)),
			Output: string(output),
		})
	}

	return json.Marshal(worktreePullResponse{
		Success: true,
		Output:  string(output),
	})
}

// =============================================================================
// worktree.get_pr
// =============================================================================

type worktreeGetPRRequest struct {
	WorktreePath string `json:"worktree_path"`
}

type worktreeGetPRResponse struct {
	Exists     bool   `json:"exists"`
	URL        string `json:"url,omitempty"`
	Number     int32  `json:"number,omitempty"`
	Title      string `json:"title,omitempty"`
	State      string `json:"state,omitempty"`
	LocalHead  string `json:"local_head,omitempty"`
	HeadRefOid string `json:"head_ref_oid,omitempty"`
	Error      string `json:"error,omitempty"`
}

func handleWorktreeGetPR(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeGetPRRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	resp := worktreeGetPRResponse{}

	branch, err := resolveCurrentBranch(ctx, req.WorktreePath)
	if err != nil {
		// No detected branch -> no PR (mirrors original "no PR exists" path).
		resp.Error = err.Error()
		return json.Marshal(resp)
	}

	// Use gh pr view to check if a PR exists
	prCmd := exec.CommandContext(ctx, "gh", "pr", "view", branch, "--json", "url,number,title,state,headRefOid")
	prCmd.Dir = req.WorktreePath
	output, err := prCmd.Output()
	if err != nil {
		// No PR exists
		return json.Marshal(resp)
	}

	var prInfo struct {
		URL        string `json:"url"`
		Number     int    `json:"number"`
		Title      string `json:"title"`
		State      string `json:"state"`
		HeadRefOid string `json:"headRefOid"`
	}
	if err := json.Unmarshal(output, &prInfo); err != nil {
		return json.Marshal(resp)
	}

	resp.Exists = true
	resp.URL = prInfo.URL
	resp.Number = int32(prInfo.Number)
	resp.Title = prInfo.Title
	resp.State = prInfo.State
	resp.HeadRefOid = prInfo.HeadRefOid

	// Get local HEAD
	localHeadCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	localHeadCmd.Dir = req.WorktreePath
	if localHeadOutput, err := localHeadCmd.Output(); err == nil {
		resp.LocalHead = strings.TrimSpace(string(localHeadOutput))
	}

	return json.Marshal(resp)
}

// =============================================================================
// worktree.create_pr
// =============================================================================

type worktreeCreatePRRequest struct {
	WorktreePath string `json:"worktree_path"`
	Title        string `json:"title"`
	Body         string `json:"body,omitempty"`
	// BaseBranch overrides the auto-detected default branch when set
	// (e.g. per-repo override looked up from worktree.BaseBranches[repo_id]).
	// Empty falls back to repo default-branch detection.
	BaseBranch string `json:"base_branch,omitempty"`
}

type worktreeCreatePRResponse struct {
	Success       bool   `json:"success"`
	PRURL         string `json:"pr_url,omitempty"`
	Output        string `json:"output,omitempty"`
	AutoCommitted bool   `json:"auto_committed"`
	AutoPushed    bool   `json:"auto_pushed"`
	Error         string `json:"error,omitempty"`
}

func handleWorktreeCreatePR(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeCreatePRRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	resp := worktreeCreatePRResponse{}

	branch, err := resolveCurrentBranch(ctx, req.WorktreePath)
	if err != nil {
		resp.Error = err.Error()
		return json.Marshal(resp)
	}

	// Step 1: Check for uncommitted changes and auto-commit if needed
	hasChanges := false
	statusCmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "status", "--porcelain")
	statusCmd.Dir = req.WorktreePath
	if statusOutput, err := statusCmd.Output(); err == nil {
		hasChanges = len(strings.TrimSpace(string(statusOutput))) > 0
	}

	if hasChanges {
		// Stage all changes. Index-writing, so it goes through the shared
		// helper for graceful cancellation and stranded-lock recovery.
		if output, err := gitutil.RunIndexCommand(ctx, req.WorktreePath, "add", "."); err != nil {
			resp.Error = fmt.Sprintf("failed to stage changes: %s", strings.TrimSpace(string(output)))
			return json.Marshal(resp)
		}

		// Commit. The shared helper supplies an ephemeral Reliant identity only
		// for user.name/user.email git can't already resolve, without persisting
		// it into repo config — so a real identity the user configures later
		// still authors their own commits.
		if output, err := gitutil.CommitWithFallbackIdentity(ctx, req.WorktreePath, req.Title, false); err != nil {
			if !strings.Contains(string(output), "nothing to commit") {
				resp.Error = fmt.Sprintf("failed to commit changes: %s", strings.TrimSpace(string(output)))
				return json.Marshal(resp)
			}
		} else {
			resp.AutoCommitted = true
		}
	}

	// Step 2: Check if branch needs push
	needsPush := false
	remoteRef := fmt.Sprintf("origin/%s", branch)
	checkRemoteCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", remoteRef)
	checkRemoteCmd.Dir = req.WorktreePath
	if err := checkRemoteCmd.Run(); err != nil {
		needsPush = true
	} else {
		revListCmd := exec.CommandContext(ctx, "git", "rev-list", "--count", fmt.Sprintf("%s..HEAD", remoteRef))
		revListCmd.Dir = req.WorktreePath
		if output, err := revListCmd.Output(); err == nil {
			if count, err := strconv.Atoi(strings.TrimSpace(string(output))); err == nil && count > 0 {
				needsPush = true
			}
		}
	}

	if needsPush {
		pushCmd := exec.CommandContext(ctx, "git", "push", "--set-upstream", "origin", branch)
		pushCmd.Dir = req.WorktreePath
		if output, err := pushCmd.CombinedOutput(); err != nil {
			resp.Error = fmt.Sprintf("failed to push branch: %s", strings.TrimSpace(string(output)))
			return json.Marshal(resp)
		}
		resp.AutoPushed = true
	}

	// Step 3: Resolve the PR base branch. Honor the caller-provided override
	// (per-repo base from worktree.BaseBranches[repo_id]) when present;
	// otherwise auto-detect the repo default branch.
	baseBranch := req.BaseBranch
	if baseBranch == "" {
		baseBranch = getRepositoryDefaultBranch(ctx, req.WorktreePath)
	}
	if branch == baseBranch {
		resp.Error = "cannot create a pull request from the default branch"
		return json.Marshal(resp)
	}

	// Step 4: Create PR
	args := []string{
		"pr", "create",
		"--title", req.Title,
		"--base", baseBranch,
		"--head", branch,
	}

	body := " "
	if req.Body != "" {
		body = req.Body
	}
	args = append(args, "--body", body)

	prCmd := exec.CommandContext(ctx, "gh", args...)
	prCmd.Dir = req.WorktreePath
	output, err := prCmd.CombinedOutput()
	if err != nil {
		errorMsg := string(output)
		if idx := strings.Index(errorMsg, "\n\nUsage:"); idx > 0 {
			errorMsg = errorMsg[:idx]
		}
		resp.Error = fmt.Sprintf("failed to create PR: %s", strings.TrimSpace(errorMsg))
		return json.Marshal(resp)
	}

	resp.Success = true
	resp.PRURL = strings.TrimSpace(string(output))
	resp.Output = string(output)
	return json.Marshal(resp)
}

// =============================================================================
// worktree.revert
// =============================================================================

type worktreeRevertRequest struct {
	WorktreePath string   `json:"worktree_path"`
	Files        []string `json:"files"`
}

type revertResult struct {
	File    string `json:"file"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type worktreeRevertResponse struct {
	Results []revertResult `json:"results"`
	Error   string         `json:"error,omitempty"`
}

func handleWorktreeRevert(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeRevertRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// Get current status
	statusCmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "status", "--porcelain", "--untracked-files=all")
	statusCmd.Dir = req.WorktreePath
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return json.Marshal(worktreeRevertResponse{Error: fmt.Sprintf("failed to get git status: %v", err)})
	}

	// Parse status
	fileStatus := make(map[string]struct {
		indexStatus    byte
		worktreeStatus byte
	})
	lines := strings.Split(string(statusOutput), "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}
		filePath := worktreeParseGitStatusPath(line[3:])
		fileStatus[filePath] = struct {
			indexStatus    byte
			worktreeStatus byte
		}{
			indexStatus:    line[0],
			worktreeStatus: line[1],
		}
	}

	var results []revertResult
	for _, filePath := range req.Files {
		status, exists := fileStatus[filePath]
		if !exists {
			results = append(results, revertResult{File: filePath, Error: "file not in changed state"})
			continue
		}

		isUntracked := status.indexStatus == '?' && status.worktreeStatus == '?'
		isStaged := status.indexStatus != ' ' && status.indexStatus != '?'
		isModified := status.worktreeStatus == 'M' || status.worktreeStatus == 'D'

		if isUntracked {
			fullPath := filepath.Join(req.WorktreePath, filePath)
			if err := os.RemoveAll(fullPath); err != nil {
				results = append(results, revertResult{File: filePath, Error: "failed to delete"})
				continue
			}
			results = append(results, revertResult{File: filePath, Success: true})
		} else if isStaged {
			// Index-writing: same policy as worktree.unstage.
			if _, err := gitutil.RunIndexCommand(ctx, req.WorktreePath, "restore", "--staged", "--", filePath); err != nil {
				results = append(results, revertResult{File: filePath, Error: "failed to unstage"})
				continue
			}
			results = append(results, revertResult{File: filePath, Success: true})
		} else if isModified {
			// `git restore` takes the index lock to read the index, so it can
			// strand one too.
			if _, err := gitutil.RunIndexCommand(ctx, req.WorktreePath, "restore", "--", filePath); err != nil {
				results = append(results, revertResult{File: filePath, Error: "failed to restore"})
				continue
			}
			results = append(results, revertResult{File: filePath, Success: true})
		} else {
			results = append(results, revertResult{File: filePath, Error: "unknown status"})
		}
	}

	return json.Marshal(worktreeRevertResponse{Results: results})
}

// =============================================================================
// worktree.get_default_branch
// =============================================================================

type worktreeGetDefaultBranchRequest struct {
	RepoPath string `json:"repo_path"`
}

type worktreeGetDefaultBranchResponse struct {
	DefaultBranch string `json:"default_branch"`
}

func handleWorktreeGetDefaultBranch(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeGetDefaultBranchRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	return json.Marshal(worktreeGetDefaultBranchResponse{
		DefaultBranch: getRepositoryDefaultBranch(ctx, req.RepoPath),
	})
}

// =============================================================================
// Shared Helpers
// =============================================================================

func getAheadBehind(ctx context.Context, repoPath, branch string) (ahead, behind int) {
	// Check if remote tracking branch exists
	checkRemoteCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", fmt.Sprintf("origin/%s", branch))
	checkRemoteCmd.Dir = repoPath
	if err := checkRemoteCmd.Run(); err != nil {
		// Try merge-base approach
		for _, defaultBranch := range []string{"origin/main", "origin/master"} {
			mergeBaseCmd := exec.CommandContext(ctx, "git", "merge-base", "HEAD", defaultBranch)
			mergeBaseCmd.Dir = repoPath
			mergeBaseOutput, err := mergeBaseCmd.Output()
			if err != nil {
				continue
			}
			mergeBase := strings.TrimSpace(string(mergeBaseOutput))
			if mergeBase == "" {
				continue
			}
			countCmd := exec.CommandContext(ctx, "git", "rev-list", "--count", fmt.Sprintf("%s..HEAD", mergeBase))
			countCmd.Dir = repoPath
			countOutput, err := countCmd.Output()
			if err != nil {
				continue
			}
			if a, err := strconv.Atoi(strings.TrimSpace(string(countOutput))); err == nil {
				ahead = a
			}
			return ahead, 0
		}
		return 0, 0
	}

	revListCmd := exec.CommandContext(ctx, "git", "rev-list", "--left-right", "--count", fmt.Sprintf("HEAD...origin/%s", branch))
	revListCmd.Dir = repoPath
	output, err := revListCmd.Output()
	if err != nil {
		return 0, 0
	}

	parts := strings.Fields(strings.TrimSpace(string(output)))
	if len(parts) == 2 {
		if a, err := strconv.Atoi(parts[0]); err == nil {
			ahead = a
		}
		if b, err := strconv.Atoi(parts[1]); err == nil {
			behind = b
		}
	}
	return
}

// resolveCurrentBranch returns the current branch name from HEAD in repoPath.
// Errors out if HEAD is detached (rev-parse returns "HEAD") — pushing,
// pulling, or creating a PR from a detached HEAD is a footgun, not a feature.
func resolveCurrentBranch(ctx context.Context, repoPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to resolve current branch: %w", err)
	}
	branch := strings.TrimSpace(string(output))
	if branch == "" {
		return "", fmt.Errorf("failed to resolve current branch: empty result from git rev-parse")
	}
	if branch == "HEAD" {
		return "", fmt.Errorf("HEAD is detached; check out a branch before performing this operation")
	}
	return branch, nil
}

// defaultBranchCache memoizes getRepositoryDefaultBranch per repo path for
// the daemon's lifetime. The default branch effectively never changes, yet
// this helper sits inside worktree.git_changes — a read the UI polls every
// few seconds — and its network fallbacks (`gh repo view`, `git remote show
// origin`) each cost ~1s on a good day and hang the whole poll pipeline on a
// bad one (GitHub slowness, credential-helper prompt in the daemon's
// non-interactive env). Resolve once, serve from memory after.
var defaultBranchCache sync.Map // repoPath -> string

// defaultBranchNetworkTimeout bounds the two network fallbacks. They are
// decorative for the polled read paths — better to fall through to the local
// heuristics than to stall a poll behind a slow GitHub round-trip.
const defaultBranchNetworkTimeout = 5 * time.Second

func getRepositoryDefaultBranch(ctx context.Context, repoPath string) string {
	if cached, ok := defaultBranchCache.Load(repoPath); ok {
		return cached.(string)
	}
	branch := resolveRepositoryDefaultBranch(ctx, repoPath)
	defaultBranchCache.Store(repoPath, branch)
	return branch
}

func resolveRepositoryDefaultBranch(ctx context.Context, repoPath string) string {
	// LOCAL first: origin/HEAD is set on clone and answers in single-digit
	// milliseconds. Output is "origin/<branch>" with --short.
	symrefCmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	symrefCmd.Dir = repoPath
	if out, err := symrefCmd.Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		if branch := strings.TrimPrefix(ref, "origin/"); branch != "" && branch != ref {
			return branch
		}
	}

	// Network fallbacks, time-boxed. Only reached when origin/HEAD is unset
	// (e.g. a repo added as a remote after init).
	netCtx, cancel := context.WithTimeout(ctx, defaultBranchNetworkTimeout)
	defer cancel()

	ghCmd := exec.CommandContext(netCtx, "gh", "repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name")
	ghCmd.Dir = repoPath
	output, err := ghCmd.Output()
	if err == nil {
		defaultBranch := strings.TrimSpace(string(output))
		if defaultBranch != "" {
			return defaultBranch
		}
	}

	// Fall back to git remote show origin
	remoteCmd := exec.CommandContext(netCtx, "git", "remote", "show", "origin")
	remoteCmd.Dir = repoPath
	remoteOutput, err := remoteCmd.Output()
	if err == nil {
		for _, line := range strings.Split(string(remoteOutput), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "HEAD branch:") {
				defaultBranch := strings.TrimSpace(strings.TrimPrefix(line, "HEAD branch:"))
				if defaultBranch != "" && defaultBranch != "(unknown)" {
					return defaultBranch
				}
			}
		}
	}

	// Final fallback
	for _, branch := range []string{"main", "master"} {
		checkCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", fmt.Sprintf("origin/%s", branch))
		checkCmd.Dir = repoPath
		if err := checkCmd.Run(); err == nil {
			return branch
		}
	}

	return "main"
}

// worktreeChangesDiffBudget bounds the total bytes of diff text in a
// git_changes response. The reply crosses NATS, whose default max_payload is
// 1MB, and JSON string-escaping roughly doubles raw diff bytes — a dirty tree
// with 84 modified files marshaled to 1.7MB and the reply was silently
// undeliverable (2026-07-09 incident). 512KB of raw diff keeps the marshaled
// reply comfortably under the cap while leaving normal working trees exact.
const worktreeChangesDiffBudget = 512 * 1024

// worktreeChangesDiffKeep is how much of an oversized diff is retained when
// the budget forces truncation — enough for a preview, not the whole thing.
const worktreeChangesDiffKeep = 4 * 1024

const worktreeChangesTruncationNote = "\n... [diff truncated: worktree changes exceeded the transport budget]"

// capWorktreeChangesDiffs enforces worktreeChangesDiffBudget by truncating
// the LARGEST diffs first, so the file list and every reasonably-sized diff
// stay exact and only pathological entries lose their tails.
func capWorktreeChangesDiffs(repoPath string, files []worktreeFileChange) []worktreeFileChange {
	total := 0
	for i := range files {
		total += len(files[i].Diff)
	}
	if total <= worktreeChangesDiffBudget {
		return files
	}

	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return len(files[order[a]].Diff) > len(files[order[b]].Diff) })

	truncated := 0
	for _, idx := range order {
		if total <= worktreeChangesDiffBudget {
			break
		}
		d := files[idx].Diff
		if len(d) <= worktreeChangesDiffKeep {
			break // remaining diffs are all small; nothing meaningful left to trim
		}
		total -= len(d) - worktreeChangesDiffKeep - len(worktreeChangesTruncationNote)
		files[idx].Diff = d[:worktreeChangesDiffKeep] + worktreeChangesTruncationNote
		truncated++
	}
	logging.Warn("[worktree.git_changes] diff budget exceeded — truncated largest diffs",
		"path", repoPath, "truncatedFiles", truncated, "remainingBytes", total,
		"budget", worktreeChangesDiffBudget)
	return files
}

func parseGitStatusWithDiffs(ctx context.Context, repoPath, statusOutput string) []worktreeFileChange {
	var files []worktreeFileChange

	lines := strings.Split(statusOutput, "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}

		indexStatus := line[0]
		worktreeStatus := line[1]
		filePath := worktreeParseGitStatusPath(line[3:])

		// Untracked files
		if indexStatus == '?' && worktreeStatus == '?' {
			diff, isBinary := readFileContentForDiff(repoPath, filePath)
			files = append(files, worktreeFileChange{
				Path:     filePath,
				Status:   "untracked",
				IsNew:    true,
				Diff:     diff,
				IsBinary: isBinary,
			})
			continue
		}

		// Staged changes
		if indexStatus != ' ' && indexStatus != '?' {
			isNew := indexStatus == 'A'
			diff, isBinary := getStagedDiffContent(ctx, repoPath, filePath)
			files = append(files, worktreeFileChange{
				Path:     filePath,
				Status:   "staged",
				IsNew:    isNew,
				Diff:     diff,
				IsBinary: isBinary,
			})
		}

		// Unstaged changes
		if worktreeStatus != ' ' {
			diff, isBinary := getUnstagedDiffContent(ctx, repoPath, filePath)
			files = append(files, worktreeFileChange{
				Path:     filePath,
				Status:   "modified",
				IsNew:    false,
				Diff:     diff,
				IsBinary: isBinary,
			})
		}
	}

	return files
}

func readFileContentForDiff(repoPath, filePath string) (string, bool) {
	fullPath := filepath.Join(repoPath, filePath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", false
	}
	content, isBinary := wtSanitizeToValidUTF8(data)
	return content, isBinary
}

func getStagedDiffContent(ctx context.Context, repoPath, filePath string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--unified=999999", "--", filePath)
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	content, isBinary := wtSanitizeToValidUTF8(output)
	return content, isBinary
}

func getUnstagedDiffContent(ctx context.Context, repoPath, filePath string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--unified=999999", "--", filePath)
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	content, isBinary := wtSanitizeToValidUTF8(output)
	return content, isBinary
}

func wtSanitizeToValidUTF8(data []byte) (content string, isBinary bool) {
	checkLen := len(data)
	if checkLen > 8000 {
		checkLen = 8000
	}
	if bytes.IndexByte(data[:checkLen], 0) != -1 {
		return "[Binary file]", true
	}
	if utf8.Valid(data) {
		return string(data), false
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), false
}

// worktreeParseGitStatusPath parses a file path from `git status --porcelain` output.
func worktreeParseGitStatusPath(rawPath string) string {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return ""
	}
	if strings.Contains(path, " -> ") {
		parts := strings.Split(path, " -> ")
		path = strings.TrimSpace(parts[len(parts)-1])
	}
	if strings.HasPrefix(path, "\"") && strings.HasSuffix(path, "\"") {
		if unquoted, err := strconv.Unquote(path); err == nil {
			return unquoted
		}
	}
	return path
}

// =============================================================================
// worktree.reconcile / worktree.snapshot_remove
// =============================================================================
//
// The server sends the worktree rows this daemon owns and records what came
// back; every decision about a directory is made here, because only the daemon
// can see the filesystem. reconcile locks active worktrees and removes archived
// ones that are provably safe; snapshot_remove is the user-confirmed path that
// saves a held worktree's work to a local ref first.

// worktreeReclaimers holds one Reclaimer per worktree root. It is keyed by
// root because a Reclaimer carries per-path locks and caches that must be shared
// by every request for that root, while the root itself follows $HOME.
var (
	worktreeReclaimersMu sync.Mutex
	worktreeReclaimers   = map[string]*worktreereclaim.Reclaimer{}
)

func sharedWorktreeReclaimer() (*worktreereclaim.Reclaimer, error) {
	root, err := worktreereclaim.DefaultRoot()
	if err != nil {
		return nil, err
	}
	worktreeReclaimersMu.Lock()
	defer worktreeReclaimersMu.Unlock()
	r := worktreeReclaimers[root]
	if r == nil {
		r = worktreereclaim.NewReclaimer(root)
		worktreeReclaimers[root] = r
		// Checkouts parked by a clean-up that a crash interrupted go back to
		// their paths before anything else looks at this root.
		r.RecoverQuarantined(context.Background(), "")
	}
	return r, nil
}

func handleWorktreeReconcile(ctx context.Context, payload []byte) ([]byte, error) {
	return handleWorktreeReclaim(ctx, payload, (*worktreereclaim.Reclaimer).Reconcile)
}

func handleWorktreeSnapshotRemove(ctx context.Context, payload []byte) ([]byte, error) {
	return handleWorktreeReclaim(ctx, payload, (*worktreereclaim.Reclaimer).SnapshotRemove)
}

func handleWorktreeReclaim(ctx context.Context, payload []byte, op func(*worktreereclaim.Reclaimer, context.Context, worktreereclaim.Request) worktreereclaim.Response) ([]byte, error) {
	var req worktreereclaim.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	r, err := sharedWorktreeReclaimer()
	if err != nil {
		return nil, fmt.Errorf("worktree root unavailable: %w", err)
	}
	return json.Marshal(op(r, ctx, req))
}
