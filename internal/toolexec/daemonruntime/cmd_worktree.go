// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/copypath"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/gitutil"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/worktreeerr"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

func init() {
	RegisterCommand("worktree.validate_path", handleValidatePath)
	RegisterCommand("worktree.create", handleWorktreeCreate)
	RegisterCommand("worktree.copy_paths", handleWorktreeCopyPaths)
	RegisterCommand("worktree.force_cleanup", handleWorktreeForceCleanup)
	RegisterCommand("worktree.delete_directory", handleWorktreeDeleteDirectory)
	RegisterCommand("worktree.remove_workspace_dir", handleWorktreeRemoveWorkspaceDir)
	RegisterCommand("worktree.delete_branch", handleWorktreeDeleteBranch)
	RegisterCommand("worktree.import_validate", handleWorktreeImportValidate)
	RegisterCommand("worktree.discover_repos", handleWorktreeDiscoverRepos)
	RegisterCommand("worktree.prune", handleWorktreePrune)
	RegisterCommand("worktree.adopt_move", handleWorktreeAdoptMove)
	RegisterCommand("worktree.adopt_restore", handleWorktreeAdoptRestore)
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
	RegisterCommand("worktree.recovery_observe", handleWorktreeRecoveryObserve)
	RegisterCommand("worktree.recovery_capture", handleWorktreeRecoveryCapture)
	RegisterCommand("worktree.recovery_finalize", handleWorktreeRecoveryFinalize)
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
	Name        string `json:"name"`
	Branch      string `json:"branch"`
	// WorkspaceID names the workspace directory:
	//   <HOME>/.reliant/worktrees/<workspace_id>/<sub_path>
	// where sub_path is the nested repo's relative path within the project.
	// Required: every worktree is a workspace (see internal/workspacecreate).
	WorkspaceID string `json:"workspace_id"`
	// SubPath is the path component under the workspace dir for this repo's
	// checkout. Empty means the workspace root itself (single-repo project).
	SubPath      string `json:"sub_path,omitempty"`
	BaseBranch   string `json:"base_branch"`
	FetchBranch  string `json:"fetch_branch,omitempty"`
	ExpectedHead string `json:"expected_head,omitempty"`
	Force        bool   `json:"force"`
	// WorktreeID is the id of the worktree row this checkout belongs to; it is
	// written into the git lock reason. Empty skips locking.
	WorktreeID string `json:"worktree_id,omitempty"`
	// ReuseBranch checks out Branch as it is when it already exists, instead
	// of resetting it to BaseBranch (`add -B`). It ignores Force. Used when
	// adopting a worktree, where the same branch name is looked up in the
	// project's other repos and a branch there is someone's work, never ours
	// to reset.
	ReuseBranch bool `json:"reuse_branch,omitempty"`
}

type worktreeCreateResponse struct {
	Success      bool   `json:"success"`
	WorktreePath string `json:"worktree_path,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"` // actually resolved base branch (after auto-detect)
	Output       string `json:"output,omitempty"`
	Error        string `json:"error,omitempty"`
	// ErrorCode says why git refused, when it is the caller's to fix (see
	// diagnoseWorktreeAdd); empty for every other failure. The server
	// classifies on it, never on Error's text.
	ErrorCode worktreeerr.Reason `json:"error_code,omitempty"`
	// ErrorDetail is the ref or path ErrorCode is about.
	ErrorDetail string `json:"error_detail,omitempty"`
}

// worktreeBaseFetchTimeout bounds fetching a base branch this clone has not
// seen from origin, inside worktree.create's own budget.
const worktreeBaseFetchTimeout = 30 * time.Second

func handleWorktreeCreate(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeCreateRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	if req.WorkspaceID == "" {
		return json.Marshal(worktreeCreateResponse{Error: "workspace_id is required"})
	}

	// Default branch name if not provided
	if req.Branch == "" {
		req.Branch = fmt.Sprintf("worktree/%s-%d", req.Name, time.Now().Unix())
	}

	// Only a base the caller named is theirs to fix if it does not resolve;
	// one detected below that fails is our bug (diagnoseWorktreeAdd).
	baseSupplied := req.BaseBranch != ""

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
			baseSupplied = false
		}
	}

	// Review-responder checkouts must refresh the exact target branch even
	// when this clone already has a stale local or remote-tracking copy.
	// A verified commit must never re-enter the branch-first resolver: a
	// SHA-shaped local branch is a legal ref name and may point elsewhere.
	var startPoint string
	if req.FetchBranch != "" || req.ExpectedHead != "" {
		if req.ReuseBranch {
			return json.Marshal(worktreeCreateResponse{Error: "cannot reuse a branch with a verified fetched head"})
		}
		if err := refreshWorktreeHead(ctx, req.ProjectPath, req.FetchBranch, req.ExpectedHead); err != nil {
			return json.Marshal(worktreeCreateResponse{Error: err.Error()})
		}
		// The explicit object expression also defeats Git's DWIM lookup of
		// a SHA-shaped branch when worktree add consumes this argument.
		startPoint = req.ExpectedHead + "^{commit}"
		req.BaseBranch = req.ExpectedHead
	}

	// Decide what the branch is made from BEFORE anything touches disk, so a
	// base that cannot be found fails with nothing to roll back. Adopt mode
	// checks out an existing local branch as it is and needs no base.
	reuseLocal := false
	if req.ReuseBranch {
		localBranch := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+req.Branch)
		localBranch.Dir = req.ProjectPath
		reuseLocal = localBranch.Run() == nil
	}
	if !reuseLocal && startPoint == "" {
		var found bool
		if startPoint, found = resolveWorktreeStartPoint(ctx, req.ProjectPath, req.BaseBranch); !found {
			// Diagnosed as a refused `git worktree add` would be: a base the
			// caller named is theirs to fix, one detected above is our bug.
			code, detail := diagnoseWorktreeAdd(ctx, req.ProjectPath, req.Branch, req.BaseBranch, baseSupplied)
			return json.Marshal(worktreeCreateResponse{
				Error:       fmt.Sprintf("base branch '%s' does not exist, locally or on origin", req.BaseBranch),
				ErrorCode:   code,
				ErrorDetail: detail,
			})
		}
	}

	// Resolve the on-disk worktree path:
	//   <HOME>/.reliant/worktrees/<workspace_id>[/<sub_path>]
	// A sub_path of "" puts the checkout at the workspace root itself
	// (single-repo project).
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return json.Marshal(worktreeCreateResponse{Error: fmt.Sprintf("failed to get home directory: %v", err)})
	}
	worktreePath := filepath.Join(homeDir, ".reliant", "worktrees", req.WorkspaceID)
	if req.SubPath != "" {
		worktreePath = filepath.Join(worktreePath, req.SubPath)
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

	// Create the git worktree. A new branch starts from the resolved start
	// point with --no-track: a base taken from origin is a starting commit,
	// not the branch's upstream, exactly as a local base is.
	var worktreeCmd *exec.Cmd
	switch {
	case reuseLocal:
		// No -B and no --force: git itself refuses when the branch is
		// checked out in another worktree.
		worktreeCmd = exec.CommandContext(ctx, "git", "worktree", "add", worktreePath, req.Branch)
	case !req.ReuseBranch && (branchExists || req.Force):
		worktreeCmd = exec.CommandContext(ctx, "git", "worktree", "add", "--no-track", "-B", req.Branch, worktreePath, startPoint)
	default:
		worktreeCmd = exec.CommandContext(ctx, "git", "worktree", "add", "--no-track", "-b", req.Branch, worktreePath, startPoint)
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
		// The base resolved above (or was not used), so it is not the reason.
		code, detail := diagnoseWorktreeAdd(ctx, req.ProjectPath, req.Branch, "", false)
		return json.Marshal(worktreeCreateResponse{
			Error:       string(output),
			ErrorCode:   code,
			ErrorDetail: detail,
		})
	}

	if req.ExpectedHead != "" {
		// Verify the postcondition on the actual checkout as well as the
		// immutable starting argument; never report a wrong-head success.
		head := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD")
		head.Dir = worktreePath
		out, err := head.Output()
		if err != nil || strings.TrimSpace(string(out)) != req.ExpectedHead {
			return json.Marshal(worktreeCreateResponse{Error: "created checkout does not match verified expected head", WorktreePath: worktreePath})
		}
	}

	// Mark the checkout reliant-owned. forge's storage reaper skips locked
	// worktrees, so this lock is what protects a clean, idle, pushed checkout
	// from being removed under an active chat. Failing to lock is not fatal to
	// creation: the reconcile pass locks every active worktree on its next run.
	if req.WorktreeID != "" {
		if err := worktreereclaim.LockCheckout(ctx, worktreePath, req.WorktreeID); err != nil {
			logging.WarnContext(ctx, "worktree created but not locked; reconcile will retry",
				"worktree_path", worktreePath, "error", err)
		}
	}

	// A new workspace waits on this whole command; say how long git took, so
	// a slow create can be told apart from a slow copy (worktree.copy_paths
	// logs its own time).
	logging.InfoContext(ctx, "worktree checked out",
		"worktree_path", worktreePath,
		"base_branch", req.BaseBranch,
		"start_point", startPoint,
		"elapsed", time.Since(checkoutStart).Round(time.Millisecond).String())

	return json.Marshal(worktreeCreateResponse{Success: true, WorktreePath: worktreePath, BaseBranch: req.BaseBranch})
}

// diagnoseWorktreeAdd says why a create just failed — its base resolved to
// nothing (resolveWorktreeStartPoint), or `git worktree add` refused — when
// the reason is the caller's to fix, by asking the repository about the
// inputs — never by reading git's output, whose wording changes between git
// versions and is translated under a non-English locale (see
// internal/worktreeerr). It runs only after a failure, so a create that
// succeeds pays nothing.
//
// The first check that holds is the answer; the empty Reason is the answer
// for everything else (git crashed, the disk is full, the destination is
// taken), which the server reports as its own fault. checkBase is false when
// the base was detected rather than named, or already resolved: only a base
// the caller named is theirs to fix.
func diagnoseWorktreeAdd(ctx context.Context, repoPath, branch, base string, checkBase bool) (worktreeerr.Reason, string) {
	git := func(args ...string) ([]byte, int) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repoPath
		out, err := cmd.Output()
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			return out, 0
		case errors.As(err, &exitErr):
			return out, exitErr.ExitCode()
		default:
			return out, -1
		}
	}

	// Every answer below is meaningless for a repository git cannot read.
	if _, code := git("rev-parse", "--git-dir"); code != 0 {
		return "", ""
	}
	// With --quiet, "resolves to nothing" is exit 1; anything worse is 128.
	if checkBase {
		// No ref starts with a dash, and git would read one as an option.
		if strings.HasPrefix(base, "-") {
			return worktreeerr.BaseNotFound, base
		}
		if _, code := git("rev-parse", "--verify", "--quiet", base+"^{commit}"); code == 1 {
			return worktreeerr.BaseNotFound, base
		}
	}
	// The repository is readable, so a refusal here is the verdict on the
	// name itself.
	if _, code := git("check-ref-format", "--branch", branch); code != 0 {
		return worktreeerr.InvalidBranchName, branch
	}
	if out, code := git("worktree", "list", "--porcelain"); code == 0 {
		for _, wt := range parseWorktreePorcelain(string(out)) {
			if wt.Branch == branch {
				return worktreeerr.BranchCheckedOut, wt.Path
			}
		}
	}
	// git stores refs as files, so `release` cannot be created beside
	// `release/x` (it would be a file where a directory is), nor `rel/y`
	// beside `rel`.
	if out, code := git("for-each-ref", "--count=1", "--format=%(refname:short)", "refs/heads/"+branch+"/"); code == 0 {
		if existing := strings.TrimSpace(string(out)); existing != "" {
			return worktreeerr.BranchNameConflict, existing
		}
	}
	for i := strings.LastIndex(branch, "/"); i > 0; i = strings.LastIndex(branch[:i], "/") {
		if _, code := git("show-ref", "--verify", "--quiet", "refs/heads/"+branch[:i]); code == 0 {
			return worktreeerr.BranchNameConflict, branch[:i]
		}
	}
	return "", ""
}

// resolveWorktreeStartPoint returns what `git worktree add` branches from for
// base: a full ref or a commit id, never a name git has to interpret.
//
// git does interpret one. Given a start point that names no commit but
// matches exactly one remote-tracking branch, `git worktree add -b <new>
// <path> feat/x` (git 2.43) creates and checks out a tracking branch named
// feat/x instead of <new>, and succeeds: the worktree row records <new>, a
// branch that was never made. So base is resolved here, in order:
//
//  1. refs/heads/<base>, the local branch;
//  2. refs/remotes/origin/<base>, origin's copy;
//  3. anything else that names a commit — a tag, a sha, an explicit remote
//     ref — as the commit id it names;
//  4. origin's <base>, fetched (that one ref) when this clone has never seen
//     it: an agent or user naming a branch that exists on GitHub expects it
//     to be found.
//
// found is false when none does — the base names no commit here or on origin.
func resolveWorktreeStartPoint(ctx context.Context, repoPath, base string) (startPoint string, found bool) {
	// A leading dash would be read as an option; no ref starts with one.
	if base == "" || strings.HasPrefix(base, "-") {
		return "", false
	}
	// Only a valid branch name is looked up — or put in a refspec — as one.
	isBranch := gitRefFormatValid(ctx, repoPath, "refs/heads/"+base)
	local, remote := "refs/heads/"+base, "refs/remotes/origin/"+base
	if isBranch {
		if gitRefExists(ctx, repoPath, local) {
			return local, true
		}
		if gitRefExists(ctx, repoPath, remote) {
			return remote, true
		}
	}
	commit := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", base+"^{commit}")
	commit.Dir = repoPath
	if out, err := commit.Output(); err == nil {
		if sha := strings.TrimSpace(string(out)); sha != "" {
			return sha, true
		}
	}
	if isBranch && fetchOriginBranch(ctx, repoPath, base) && gitRefExists(ctx, repoPath, remote) {
		return remote, true
	}
	return "", false
}

// gitRefFormatValid reports whether ref is a well-formed full ref name.
func gitRefFormatValid(ctx context.Context, repoPath, ref string) bool {
	cmd := exec.CommandContext(ctx, "git", "check-ref-format", ref)
	cmd.Dir = repoPath
	return cmd.Run() == nil
}

// gitRefExists reports whether the full ref exists.
func gitRefExists(ctx context.Context, repoPath, ref string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = repoPath
	return cmd.Run() == nil
}

// fetchOriginBranch fetches origin's branch into refs/remotes/origin/<branch>,
// bounded by worktreeBaseFetchTimeout. false when the repo has no origin or
// the fetch fails: no such branch there, offline, or no access.
func fetchOriginBranch(ctx context.Context, repoPath, branch string) bool {
	hasOrigin := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
	hasOrigin.Dir = repoPath
	if hasOrigin.Run() != nil {
		return false
	}
	fetchCtx, cancel := context.WithTimeout(ctx, worktreeBaseFetchTimeout)
	defer cancel()
	cmd := exec.CommandContext(fetchCtx, "git", "fetch", "--quiet", "--no-tags", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch)
	cmd.Dir = repoPath
	cmd.Env = gitCredentialEnv(append(os.Environ(), "GIT_TERMINAL_PROMPT=0")) //nolint:forbidigo // passes this process environment to a child process; not configuration
	if out, err := cmd.CombinedOutput(); err != nil {
		logging.InfoContext(ctx, "worktree base branch not found on origin",
			"repo_path", repoPath, "base_branch", branch, "output", strings.TrimSpace(string(out)), "error", err)
		return false
	}
	logging.InfoContext(ctx, "fetched worktree base branch from origin", "repo_path", repoPath, "base_branch", branch)
	return true
}

// =============================================================================
// worktree.adopt_move / worktree.adopt_restore
// =============================================================================
//
// Adopting a hand-made worktree of a multi-repo project moves its checkout to
// the place a Reliant workspace keeps that repo,
// <HOME>/.reliant/worktrees/<workspace_id>/<sub_path>. The move is `git
// worktree move`, which renames the directory and rewrites git's records: the
// branch, HEAD, index and uncommitted changes are untouched. adopt_restore is
// its exact inverse, used when anything later in the adopt fails.

type worktreeAdoptMoveRequest struct {
	// RepoPath is the repo's main checkout, which owns the worktree.
	RepoPath string `json:"repo_path"`
	// Src is the discovered checkout to move.
	Src         string `json:"src"`
	WorkspaceID string `json:"workspace_id"`
	SubPath     string `json:"sub_path,omitempty"`
	WorktreeID  string `json:"worktree_id,omitempty"`
}

type worktreeAdoptMoveResponse struct {
	Success      bool   `json:"success"`
	WorktreePath string `json:"worktree_path,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"`
	// Error is git's own refusal when it has one (locked, submodules, ...).
	Error string `json:"error,omitempty"`
}

func workspaceCheckoutPath(workspaceID, subPath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	p := filepath.Join(home, ".reliant", "worktrees", workspaceID)
	if subPath != "" {
		p = filepath.Join(p, subPath)
	}
	return p, nil
}

func handleWorktreeAdoptMove(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeAdoptMoveRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	fail := func(format string, a ...any) ([]byte, error) {
		return json.Marshal(worktreeAdoptMoveResponse{Error: fmt.Sprintf(format, a...)})
	}
	if req.RepoPath == "" || req.Src == "" || req.WorkspaceID == "" {
		return fail("repo_path, src and workspace_id are required")
	}
	// The server validated src against a discovery, but this is the boundary
	// that touches the disk: it must still be a live linked worktree of THIS
	// repo (never its main checkout).
	entries, err := listRepoLinkedWorktrees(ctx, worktreeRepoRef{Path: req.RepoPath})
	if err != nil {
		return fail("%v", err)
	}
	found := false
	for _, e := range entries {
		if !e.Prunable && comparablePath(e.Path) == comparablePath(req.Src) {
			found = true
			break
		}
	}
	if !found {
		return fail("%s is not a linked worktree of %s", req.Src, req.RepoPath)
	}
	dest, err := workspaceCheckoutPath(req.WorkspaceID, req.SubPath)
	if err != nil {
		return fail("%v", err)
	}
	if _, err := os.Lstat(dest); err == nil {
		return fail("destination %s already exists", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fail("failed to create %s: %v", filepath.Dir(dest), err)
	}
	// No --force: git refuses a locked worktree or one with submodules, and
	// that refusal is the answer.
	out, err := exec.CommandContext(ctx, "git", "-C", req.RepoPath, "worktree", "move", req.Src, dest).CombinedOutput()
	if err != nil {
		// Only the parent we may have just made, and only when empty.
		if parent := filepath.Dir(dest); parent != filepath.Dir(filepath.Dir(dest)) {
			if entries, rerr := os.ReadDir(parent); rerr == nil && len(entries) == 0 {
				_ = os.Remove(parent)
			}
		}
		return fail("%s", strings.TrimSpace(string(out)))
	}
	if req.WorktreeID != "" {
		if err := worktreereclaim.LockCheckout(ctx, dest, req.WorktreeID); err != nil {
			logging.WarnContext(ctx, "adopted worktree moved but not locked; reconcile will retry", "worktree_path", dest, "error", err)
		}
	}
	return json.Marshal(worktreeAdoptMoveResponse{
		Success:      true,
		WorktreePath: dest,
		BaseBranch:   getRepositoryDefaultBranch(ctx, req.RepoPath),
	})
}

type worktreeAdoptRestoreRequest struct {
	RepoPath string `json:"repo_path"`
	// Src is where the checkout was before the move; Dest is where it is now.
	Src  string `json:"src"`
	Dest string `json:"dest"`
}

type worktreeAdoptRestoreResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// handleWorktreeAdoptRestore moves an adopted checkout back to where it came
// from. It never deletes anything.
func handleWorktreeAdoptRestore(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeAdoptRestoreRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	fail := func(format string, a ...any) ([]byte, error) {
		return json.Marshal(worktreeAdoptRestoreResponse{Error: fmt.Sprintf(format, a...)})
	}
	if req.RepoPath == "" || req.Src == "" || req.Dest == "" {
		return fail("repo_path, src and dest are required")
	}
	if _, err := os.Lstat(req.Dest); err != nil {
		if _, serr := os.Lstat(req.Src); serr == nil {
			// Already back (a retry after a partial rollback).
			return json.Marshal(worktreeAdoptRestoreResponse{Success: true})
		}
		return fail("neither %s nor %s exists", req.Dest, req.Src)
	}
	if _, err := os.Lstat(req.Src); err == nil {
		return fail("original path %s is occupied; the checkout stays at %s", req.Src, req.Dest)
	}
	// git will not move a locked worktree; the lock is Reliant's own.
	worktreereclaim.UnlockForRemoval(ctx, req.Dest)
	if out, err := exec.CommandContext(ctx, "git", "-C", req.RepoPath, "worktree", "move", req.Dest, req.Src).CombinedOutput(); err != nil {
		return fail("%s", strings.TrimSpace(string(out)))
	}
	return json.Marshal(worktreeAdoptRestoreResponse{Success: true})
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
// worktree.discover_repos / worktree.prune
// =============================================================================

// worktreeRepoRef names one registered repo of a project and where it is on
// this machine.
type worktreeRepoRef struct {
	RepoID string `json:"repo_id"`
	Path   string `json:"path"`
}

type worktreeDiscoverReposRequest struct {
	Repos []worktreeRepoRef `json:"repos"`
	// ExcludeRoots are paths whose checkouts are already tracked; an entry
	// equal to or under one is dropped. The daemon's own worktrees root is
	// always excluded as well.
	ExcludeRoots []string `json:"exclude_roots,omitempty"`
}

// discoveredWorktreeEntry is one linked worktree of a repo. Prunable entries
// are ones whose directory is gone; git does not mark a locked worktree
// prunable.
type discoveredWorktreeEntry struct {
	RepoID         string `json:"repo_id"`
	Path           string `json:"path"`
	Name           string `json:"name"`
	Branch         string `json:"branch,omitempty"`
	Head           string `json:"head,omitempty"`
	Detached       bool   `json:"detached,omitempty"`
	Locked         bool   `json:"locked,omitempty"`
	LockReason     string `json:"lock_reason,omitempty"`
	Prunable       bool   `json:"prunable,omitempty"`
	PrunableReason string `json:"prunable_reason,omitempty"`
}

type worktreeDiscoverReposResponse struct {
	Worktrees []discoveredWorktreeEntry `json:"worktrees"`
	// WorktreesRoot is where this machine keeps Reliant-made workspaces.
	WorktreesRoot string `json:"worktrees_root,omitempty"`
}

type worktreePruneRequest struct {
	Repos []worktreeRepoRef `json:"repos"`
}

type worktreePruneResponse struct {
	// Pruned are the stale entries that were gone after the prune.
	Pruned []discoveredWorktreeEntry `json:"pruned"`
	// Errors are per-repo failures; other repos are still processed.
	Errors []string `json:"errors,omitempty"`
}

// porcelainWorktree is one block of `git worktree list --porcelain`.
type porcelainWorktree struct {
	Path           string
	Head           string
	Branch         string
	Detached       bool
	Bare           bool
	Locked         bool
	LockReason     string
	Prunable       bool
	PrunableReason string
}

// parseWorktreePorcelain parses `git worktree list --porcelain`. Blocks are
// separated by blank lines and the first is the main checkout.
func parseWorktreePorcelain(out string) []porcelainWorktree {
	var all []porcelainWorktree
	var cur *porcelainWorktree
	flush := func() {
		if cur != nil {
			all = append(all, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		if key == "worktree" {
			flush()
			cur = &porcelainWorktree{Path: val}
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		case "locked":
			cur.Locked = true
			cur.LockReason = val
		case "prunable":
			cur.Prunable = true
			cur.PrunableReason = val
		}
	}
	flush()
	return all
}

// listRepoLinkedWorktrees lists a repo's linked worktrees (never its main
// checkout). A path that is missing or not itself a git checkout yields
// nothing and no error: `git -C` would otherwise walk up into an enclosing
// repository and report ITS worktrees.
func listRepoLinkedWorktrees(ctx context.Context, repo worktreeRepoRef) ([]discoveredWorktreeEntry, error) {
	if repo.Path == "" {
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(repo.Path, ".git")); err != nil {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repo.Path, "worktree", "list", "--porcelain")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "not a git repository") {
			return nil, nil
		}
		return nil, fmt.Errorf("git worktree list in %s: %s", repo.Path, msg)
	}
	self := comparablePath(repo.Path)
	var entries []discoveredWorktreeEntry
	for i, wt := range parseWorktreePorcelain(string(out)) {
		if i == 0 || wt.Bare || comparablePath(wt.Path) == self {
			continue
		}
		entries = append(entries, discoveredWorktreeEntry{
			RepoID:         repo.RepoID,
			Path:           wt.Path,
			Name:           filepath.Base(wt.Path),
			Branch:         wt.Branch,
			Head:           wt.Head,
			Detached:       wt.Detached,
			Locked:         wt.Locked,
			LockReason:     wt.LockReason,
			Prunable:       wt.Prunable,
			PrunableReason: wt.PrunableReason,
		})
	}
	return entries, nil
}

// comparablePath resolves symlinks for comparison. A path that no longer
// exists resolves through its nearest existing parent.
func comparablePath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if dir := filepath.Dir(p); dir != p {
		return filepath.Join(comparablePath(dir), filepath.Base(p))
	}
	return p
}

func pathWithin(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

func handleWorktreeDiscoverRepos(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeDiscoverReposRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	resp := worktreeDiscoverReposResponse{Worktrees: []discoveredWorktreeEntry{}}
	var excluded []string
	if root, err := worktreereclaim.DefaultRoot(); err == nil {
		resp.WorktreesRoot = root
		excluded = append(excluded, comparablePath(root))
	}
	for _, r := range req.ExcludeRoots {
		if r != "" {
			excluded = append(excluded, comparablePath(r))
		}
	}
	for _, repo := range req.Repos {
		entries, err := listRepoLinkedWorktrees(ctx, repo)
		if err != nil {
			logging.WarnContext(ctx, "worktree.discover_repos: list failed", "repo", repo.Path, "error", err)
			continue
		}
	next:
		for _, e := range entries {
			// A stale record has no directory to protect, so exclusions
			// (which guard tracked checkouts) do not apply: pruning it only
			// drops git's bookkeeping.
			if !e.Prunable {
				p := comparablePath(e.Path)
				for _, x := range excluded {
					if pathWithin(p, x) {
						continue next
					}
				}
			}
			resp.Worktrees = append(resp.Worktrees, e)
		}
	}
	return json.Marshal(resp)
}

func handleWorktreePrune(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreePruneRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	resp := worktreePruneResponse{Pruned: []discoveredWorktreeEntry{}}
	for _, repo := range req.Repos {
		before, err := listRepoLinkedWorktrees(ctx, repo)
		if err != nil {
			resp.Errors = append(resp.Errors, err.Error())
			continue
		}
		var stale []discoveredWorktreeEntry
		for _, e := range before {
			if e.Prunable {
				stale = append(stale, e)
			}
		}
		if len(stale) == 0 {
			continue
		}
		// Plain prune only: it removes git's records of directories that are
		// already gone. No --expire, and nothing that touches a directory.
		if out, err := exec.CommandContext(ctx, "git", "-C", repo.Path, "worktree", "prune").CombinedOutput(); err != nil {
			resp.Errors = append(resp.Errors, fmt.Sprintf("git worktree prune in %s: %s", repo.Path, strings.TrimSpace(string(out))))
			continue
		}
		after, err := listRepoLinkedWorktrees(ctx, repo)
		if err != nil {
			resp.Errors = append(resp.Errors, err.Error())
			continue
		}
		remaining := map[string]bool{}
		for _, e := range after {
			remaining[e.Path] = true
		}
		for _, e := range stale {
			if !remaining[e.Path] {
				resp.Pruned = append(resp.Pruned, e)
			}
		}
	}
	return json.Marshal(resp)
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
	return json.Marshal(recreateWorktree(ctx, req))
}

// recreateWorktree rebuilds a workspace's checkouts from their branch: one
// `git worktree add` per repo, all or nothing. It serves both the restore of
// an archived workspace (worktree.recreate) and the repair of an active one
// whose directory disappeared (worktree.ensure), so the two cannot drift.
func recreateWorktree(ctx context.Context, req worktreeRecreateRequest) worktreeRecreateResponse {
	repos := req.Repos
	if len(repos) == 0 {
		repos = []worktreeRecreateRepo{{RepoPath: req.ProjectPath, Rel: "", Branch: req.Branch, SnapshotRefs: req.SnapshotRefs}}
	}
	resp := worktreeRecreateResponse{}

	// Check everything BEFORE creating anything, so a missing branch in the
	// third repo does not leave two checkouts behind.
	for _, repo := range repos {
		branch := firstNonEmptyStr(repo.Branch, req.Branch)
		if !branchAvailable(ctx, repo.RepoPath, branch) {
			resp.BranchExists = false
			resp.Error = fmt.Sprintf("branch '%s' no longer exists in %s", branch, repo.RepoPath)
			return resp
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
		dest := filepath.Join(req.WorktreePath, filepath.FromSlash(repo.Rel))
		if recovered[dest] {
			continue
		}
		if _, err := os.Stat(dest); err == nil {
			resp.PathExists = true
			resp.Error = fmt.Sprintf("worktree directory '%s' already exists", dest)
			return resp
		}
		// Only for a path that is provably absent: a stat that failed for any
		// other reason (permissions) says nothing, and dropping the
		// registration of a checkout that is actually there deletes it.
		if _, err := os.Lstat(dest); errors.Is(err, fs.ErrNotExist) {
			dropStaleRegistration(ctx, repo.RepoPath, dest)
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
			return resp
		}
		// A branch that exists only as a remote-tracking ref is created
		// locally by git's own DWIM, tracking that remote (branchAvailable).
		add := exec.CommandContext(ctx, "git", "worktree", "add", dest, firstNonEmptyStr(repo.Branch, req.Branch))
		add.Dir = repo.RepoPath
		if output, err := add.CombinedOutput(); err != nil {
			rollback()
			resp.Output = string(output)
			resp.Error = fmt.Sprintf("failed to recreate worktree: %s", strings.TrimSpace(string(output)))
			return resp
		}
		created[i] = dest
	}

	if req.WorktreeID != "" {
		for _, dest := range created {
			if dest == "" {
				continue
			}
			if err := worktreereclaim.RestoreCheckout(ctx, dest, req.WorktreeID, req.Fence); err != nil {
				logging.WarnContext(ctx, "worktree recreated but not locked; reconcile will retry", "worktree_path", dest, "error", err)
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
				logging.WarnContext(ctx, "worktree recreated but its snapshot was not applied", "ref", ref, "error", err)
				continue
			}
			if err := worktreereclaim.RetireAppliedRef(ctx, repo.RepoPath, ref); err != nil {
				logging.WarnContext(ctx, "snapshot applied but its ref could not be retired", "ref", ref, "error", err)
			}
		}
	}
	if !anySnapshot {
		resp.SnapshotApplied = false
	}
	resp.Success = true
	return resp
}

// branchAvailable reports whether `git worktree add <path> <branch>` can check
// the branch out: it resolves locally, or exactly one remote has a branch of
// that name, which git's DWIM turns into a local branch tracking it. A branch
// that was pushed is therefore still recoverable after its local ref is gone.
func branchAvailable(ctx context.Context, repoPath, branch string) bool {
	if branch == "" {
		return false
	}
	check := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", branch)
	check.Dir = repoPath
	if check.Run() == nil {
		return true
	}
	refs := exec.CommandContext(ctx, "git", "for-each-ref", "--format=%(refname)", "refs/remotes/*/"+branch)
	refs.Dir = repoPath
	out, err := refs.Output()
	if err != nil {
		return false
	}
	return len(strings.Fields(string(out))) == 1
}

// dropStaleRegistration removes git's record of a checkout at dest, which the
// caller has just found does not exist.
//
// A checkout deleted without git being told stays registered, and reliant
// locks the checkouts it creates, so `git worktree prune` keeps the record
// too. git then refuses both the path ("missing but locked worktree") and the
// branch ("already checked out") to `git worktree add`, and nothing could ever
// rebuild the directory. Removing the one registration — never a repo-wide
// prune, which would also drop other worktrees' records — clears both. A path
// git does not know is a no-op.
//
// The registration is named by git's own record of it. git resolves a path it
// is given through realpath, which fails once the checkout's PARENT is gone
// too (a whole multi-repo workspace deleted), so the caller's spelling of dest
// would match nothing; its recorded path always matches itself.
func dropStaleRegistration(ctx context.Context, repoPath, dest string) {
	registered := registeredWorktreePath(ctx, repoPath, dest)
	if registered == "" {
		return
	}
	rm := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", "--force", registered)
	rm.Dir = repoPath
	if out, err := rm.CombinedOutput(); err != nil {
		logging.WarnContext(ctx, "could not drop a stale worktree registration", "repo", repoPath, "path", registered, "error", err, "output", strings.TrimSpace(string(out)))
	}
}

// registeredWorktreePath returns the path under which repoPath records a
// worktree at dest, or "" when it records none. Both sides are compared
// canonically, since git records the realpath (/private/var/... on macOS for
// /var/...).
func registeredWorktreePath(ctx context.Context, repoPath, dest string) string {
	list := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	list.Dir = repoPath
	out, err := list.Output()
	if err != nil {
		return ""
	}
	want := canonicalMissingPath(dest)
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok && canonicalMissingPath(p) == want {
			return p
		}
	}
	return ""
}

// canonicalMissingPath resolves symlinks in the longest prefix of p that
// exists, and keeps the rest as written: the path may name something gone.
func canonicalMissingPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
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
		r.Acquire = func(ctx context.Context, paths []string) (worktreereclaim.RecoveryLease, error) {
			return AcquireWorktreeRecovery(ctx, paths)
		}
		r.OnRepair = logRepairReport
		worktreeReclaimers[root] = r
		// Checkouts parked by a clean-up that a crash interrupted go back to
		// their paths before anything else looks at this root.
		r.RecoverQuarantined(context.Background(), "")
	}
	return r, nil
}

// repairSharedClones runs crash repair (worktreereclaim.RepairRepo) once over
// every repository reliant's checkouts belong to: zero-byte loose objects,
// torn worktree indexes, stale gc.log, registrations of checkouts that are
// gone. A VM that dies mid-write leaves exactly that, and until it is repaired
// every git command in those repositories fails. Called off the startup path;
// the sweep repeats it per repository before it reads one.
func repairSharedClones(ctx context.Context) {
	r, err := sharedWorktreeReclaimer()
	if err != nil {
		return
	}
	if fi, err := os.Stat(r.Root); err != nil || !fi.IsDir() {
		return
	}
	repos := worktreereclaim.DiscoverCommonDirs(r.Root)
	repaired := 0
	for _, rep := range r.RepairRepos(ctx, repos) {
		if rep.Repaired() {
			repaired++
		}
	}
	logging.InfoContext(ctx, logPrefix+" [gitrepair] Startup crash repair finished", "repositories", len(repos), "repaired", repaired)
}

// logRepairReport logs one repository's repair: what was fixed at INFO,
// registrations left for their owner at WARN, and damage that remains — lost
// objects above all — at ERROR, so it is never silently absorbed.
func logRepairReport(rep worktreereclaim.RepairReport) {
	attrs := []any{"repo", rep.CommonDir}
	add := func(key string, v []string) {
		if len(v) > 0 {
			attrs = append(attrs, key, strings.Join(v, "; "))
		}
	}
	add("empty_objects_removed", rep.EmptyObjects)
	add("indexes_rebuilt", rep.RebuiltIndexes)
	add("heads_restored", rep.RestoredHEADs)
	add("debris_removed", rep.RemovedDebris)
	add("registrations_pruned", rep.PrunedRegistrations)
	if rep.RemovedGCLog != "" {
		attrs = append(attrs, "gc_log_removed", rep.RemovedGCLog)
	}
	add("registrations_skipped", rep.SkippedRegistrations)
	add("lost_objects", rep.LostObjects)
	add("fsck_problems", rep.FsckProblems)
	add("problems", rep.Problems)
	switch {
	case rep.Unhealthy():
		logging.Error(logPrefix+" [gitrepair] Repository has damage that was not repaired", attrs...)
	case rep.Repaired():
		logging.Info(logPrefix+" [gitrepair] Repaired crash damage", attrs...)
	default:
		logging.Warn(logPrefix+" [gitrepair] Left registrations of missing checkouts locked by someone else", attrs...)
	}
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

func observeRecoveryWorkspace(ctx context.Context, r *worktreereclaim.Reclaimer, req worktreereclaim.RecoveryRequest) (worktreereclaim.RecoveryActivity, error) {
	if _, err := uuid.Parse(req.Worktree.ID); err != nil {
		return worktreereclaim.RecoveryActivity{}, errors.New("invalid worktree identity")
	}
	rel, err := filepath.Rel(r.Root, req.Worktree.Path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.HasPrefix(rel, ".reclaim"+string(filepath.Separator)) {
		return worktreereclaim.RecoveryActivity{}, errors.New("invalid observation path")
	}
	var paths []string
	for _, checkout := range req.Worktree.Checkouts {
		rel := filepath.FromSlash(checkout.Rel)
		if rel != "" && (filepath.IsAbs(rel) || filepath.Clean(rel) != rel || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return worktreereclaim.RecoveryActivity{}, errors.New("invalid observation checkout")
		}
		paths = append(paths, filepath.Join(req.Worktree.Path, rel))
	}
	if len(paths) == 0 {
		return worktreereclaim.RecoveryActivity{}, errors.New("observation checkouts required")
	}
	return ObserveWorktreeRecoveries(ctx, paths)
}

func handleWorktreeRecoveryObserve(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreereclaim.RecoveryRequest
	if json.Unmarshal(payload, &req) != nil {
		return nil, errors.New("invalid recovery payload")
	}
	r, err := sharedWorktreeReclaimer()
	if err != nil {
		return nil, err
	}
	activity, err := observeRecoveryWorkspace(ctx, r, req)
	response := worktreereclaim.RecoveryResponse{Outcome: worktreereclaim.OutcomeHeld, Reason: worktreereclaim.ReasonObservationGap, Activity: activity}
	if err == nil && worktreereclaim.RecoveryIdleEligible(activity, time.Now()) {
		response.Outcome = worktreereclaim.OutcomeVerified
		response.Reason = ""
	}
	return json.Marshal(response)
}

func handleWorktreeRecoveryCapture(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreereclaim.RecoveryRequest
	if json.Unmarshal(payload, &req) != nil {
		return nil, errors.New("invalid recovery payload")
	}
	r, err := sharedWorktreeReclaimer()
	if err != nil {
		return nil, err
	}
	return json.Marshal(r.CaptureRecoveryWorkspace(ctx, req))
}

func handleWorktreeRecoveryFinalize(ctx context.Context, payload []byte) ([]byte, error) {
	var req struct {
		worktreereclaim.RecoveryRequest
		Token string `json:"token"`
	}
	if json.Unmarshal(payload, &req) != nil {
		return nil, errors.New("invalid recovery payload")
	}
	r, err := sharedWorktreeReclaimer()
	if err != nil {
		return nil, err
	}
	token := req.Token
	req.Token = ""
	result := r.FinalizeRecovery(ctx, req.RecoveryRequest, worktreereclaim.RecoveryFinalizeDeps{
		Verify: func(ctx context.Context, c worktreereclaim.RecoveryCapture) (worktreereclaim.RecoveryReceipt, error) {
			return VerifyGitRecovery(ctx, c, token)
		},
		Observe: func(ctx context.Context, _ string) (worktreereclaim.RecoveryActivity, error) {
			return observeRecoveryWorkspace(ctx, r, req.RecoveryRequest)
		},
		Acquire: func(ctx context.Context, paths []string) (worktreereclaim.RecoveryLease, error) {
			return AcquireWorktreeRecovery(ctx, paths)
		},
		CheckProcessUse: worktreereclaim.CheckProcessUse,
	})
	return json.Marshal(result)
}
