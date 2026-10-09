// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() {
	RegisterCommand("git.clone", handleGitClone)
	RegisterCommand("git.pull", handleGitPull)
	RegisterCommand("git.remove", handleGitRemove)
	RegisterCommand("git.reclone", handleGitReclone)
}

// =============================================================================
// git.clone
// =============================================================================

type gitCloneRequest struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

type gitCloneResponse struct {
	Success bool   `json:"success"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
}

func handleGitClone(ctx context.Context, payload []byte) ([]byte, error) {
	var req gitCloneRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	if req.Repo == "" {
		return nil, fmt.Errorf("repo is required")
	}

	// Default branch
	if req.Branch == "" {
		req.Branch = "main"
	}

	// Default path: /home/workspace/projects/<repo-name>
	if req.Path == "" {
		repoName := repoNameFromURL(req.Repo)
		req.Path = filepath.Join("/home/workspace/projects", repoName)
	}

	// Idempotency guard: a redelivered git.clone (JetStream WorkQueue
	// redelivery after a dispatch timeout/NAK — see drainPendingCommands in
	// nats_bridge.go) can arrive after the FIRST attempt already completed
	// the clone on disk. Because the clone is atomic (below), a .git at
	// req.Path can only be a finished checkout, never a partial one.
	if _, err := os.Stat(filepath.Join(req.Path, ".git")); err == nil {
		return json.Marshal(gitCloneResponse{
			Success: true,
			Path:    req.Path,
		})
	}

	if err := cloneAtomically(ctx, req.Repo, req.Branch, req.Path); err != nil {
		return nil, err
	}

	return json.Marshal(gitCloneResponse{
		Success: true,
		Path:    req.Path,
	})
}

// cloneStagingPrefix names the hidden sibling directory a clone is built in
// before it is renamed into place.
const cloneStagingPrefix = ".reliant-clone-"

// cloneAtomically clones repo into dest so that dest only ever holds a
// COMPLETE checkout or nothing at all.
//
// `git clone` writes straight into its destination as it goes. A clone that
// fails, times out, or dies with the daemon (an OOM kill, a pod suspended
// mid-clone) therefore leaves a partial directory at the project path — and a
// directory at the project path is exactly what the rest of the product reads
// as "the project is there". So the clone is built in a hidden sibling
// directory and renamed into place only once git has exited successfully. A
// sibling, not os.TempDir: rename(2) is atomic only within one filesystem, and
// the projects directory is a mounted volume while /tmp is not.
//
// dest may already exist as an EMPTY directory (created by an older
// pre-create path, or by the user), and is adopted: the rename replaces it.
// A NON-EMPTY dest that is not a checkout is someone's files, never ours to
// replace, and is refused.
func cloneAtomically(ctx context.Context, repo, branch, dest string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}
	if err := ensureCloneTargetAvailable(dest); err != nil {
		return err
	}
	// Staging directories orphaned by a clone that died mid-way (the process
	// was killed, so no defer ran) are swept here, so they cannot accumulate.
	removeStaleCloneStaging(parent, filepath.Base(dest))

	staging, err := os.MkdirTemp(parent, cloneStagingPrefix+filepath.Base(dest)+"-")
	if err != nil {
		return fmt.Errorf("failed to create clone staging directory: %v", err)
	}
	// Whatever happens below, the staging directory does not outlive this
	// call: on success it has been renamed away and this is a no-op.
	defer func() { _ = os.RemoveAll(staging) }()

	// Clone INTO the (empty) staging directory: `git clone <url> <dir>`
	// accepts an existing empty directory. The URL stays clean: a token in it
	// would be written to .git/config as remote.origin.url and outlive its
	// validity there.
	cmd := exec.CommandContext(ctx, "git", "clone", "--branch", branch, repo, staging)
	cmd.Env = gitCredentialEnv(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))

	// git clone can be memory-heavy on large repos; attribute a SIGKILL to
	// the workspace OOM killer when the cgroup recorded one.
	oomSnap := memReader.SnapshotOOMKills()
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %v: %s", wrapChildOOMKill(err, oomSnap), string(output))
	}

	// An empty directory at dest (checked above) is replaced by the rename on
	// POSIX only when it is removed first; os.Rename onto a non-empty or
	// existing directory fails with ENOTEMPTY/EEXIST.
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clear empty clone target %s: %v", dest, err)
	}
	if err := os.Rename(staging, dest); err != nil {
		return fmt.Errorf("failed to move clone into place at %s: %v", dest, err)
	}
	return nil
}

// ensureCloneTargetAvailable allows a clone into dest when dest is absent or
// an empty directory, and refuses anything else with a reason the user can
// act on.
func ensureCloneTargetAvailable(dest string) error {
	info, err := os.Stat(dest)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot inspect clone target %s: %v", dest, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cannot clone into %s: a file already exists there", dest)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return fmt.Errorf("cannot inspect clone target %s: %v", dest, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("cannot clone into %s: the directory already exists and is not empty", dest)
	}
	return nil
}

// removeStaleCloneStaging deletes staging directories for the same target
// left behind by an earlier clone that was killed before its cleanup ran.
// Scoped to this target's prefix, so a concurrent clone of a DIFFERENT repo
// in the same parent is never touched. Best-effort: a failure here costs disk
// space, never correctness.
func removeStaleCloneStaging(parent, base string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	prefix := cloneStagingPrefix + base + "-"
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			_ = os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
}

// repoNameFromURL extracts the repository name from a git URL.
// e.g. "https://github.com/org/repo.git" -> "repo"
func repoNameFromURL(url string) string {
	url = strings.TrimSuffix(url, ".git")
	url = strings.TrimSuffix(url, "/")
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "repo"
}

// =============================================================================
// git.pull
// =============================================================================

type gitPullRequest struct {
	// Path is the absolute path to the existing clone on this daemon.
	Path string `json:"path"`
	// Branch is the branch to pull. Empty means pull whatever HEAD tracks.
	Branch string `json:"branch,omitempty"`
}

type gitPullResponse struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

func handleGitPull(ctx context.Context, payload []byte) ([]byte, error) {
	var req gitPullRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	// Refuse to run git in something that isn't actually a clone — git pull
	// in a non-repo silently noops with a confusing error; surface it cleanly.
	if _, err := os.Stat(filepath.Join(req.Path, ".git")); err != nil {
		return nil, fmt.Errorf("not a git repository: %s", req.Path)
	}

	args := []string{"-C", req.Path, "pull", "--ff-only"}
	if req.Branch != "" {
		args = append(args, "origin", req.Branch)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	oomSnap := memReader.SnapshotOOMKills()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git pull failed: %v: %s", wrapChildOOMKill(err, oomSnap), string(output))
	}
	return json.Marshal(gitPullResponse{
		Success: true,
		Output:  truncateGitOutput(string(output)),
	})
}

// =============================================================================
// git.remove
// =============================================================================
//
// Deletes the on-disk clone. The caller is responsible for deleting the
// corresponding project_daemons row — this handler does NOT touch the DB
// because the daemon doesn't own that table. Idempotent: a missing path
// returns success.

type gitRemoveRequest struct {
	Path string `json:"path"`
}

type gitRemoveResponse struct {
	Success bool   `json:"success"`
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`
}

func handleGitRemove(_ context.Context, payload []byte) ([]byte, error) {
	var req gitRemoveRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	// Refuse to remove paths that aren't clearly project clones to avoid
	// turning a misconfigured path into `rm -rf /` style damage. We require
	// the path to either not exist OR contain a .git directory/file.
	if info, err := os.Stat(req.Path); err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("path is not a directory: %s", req.Path)
		}
		if _, err := os.Stat(filepath.Join(req.Path, ".git")); err != nil {
			return nil, fmt.Errorf("refusing to remove non-clone path (no .git): %s", req.Path)
		}
		if err := os.RemoveAll(req.Path); err != nil {
			return nil, fmt.Errorf("remove clone: %w", err)
		}
		return json.Marshal(gitRemoveResponse{Success: true, Removed: true})
	}
	// Path doesn't exist — idempotent success.
	return json.Marshal(gitRemoveResponse{Success: true, Removed: false})
}

// =============================================================================
// git.reclone
// =============================================================================
//
// Combines git.remove + git.clone into one round trip so the caller sees a
// single atomic-ish operation. Same destructive semantics as git.remove for
// the existing path; same auth/credential handling as git.clone for the new
// clone.

type gitRecloneRequest struct {
	// Path is the existing clone path (will be removed before re-cloning to
	// the same location).
	Path string `json:"path"`
	// Repo is the git remote URL to clone from.
	Repo string `json:"repo"`
	// Branch is the branch to check out (default "main").
	Branch string `json:"branch,omitempty"`
}

type gitRecloneResponse struct {
	Success bool   `json:"success"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
}

func handleGitReclone(ctx context.Context, payload []byte) ([]byte, error) {
	var req gitRecloneRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Path == "" || req.Repo == "" {
		return nil, fmt.Errorf("path and repo are required")
	}
	branch := req.Branch
	if branch == "" {
		branch = "main"
	}

	// Best-effort remove: if the directory doesn't exist this is fine. We
	// only enforce the "must contain .git" guard when the directory does
	// exist (mirrors handleGitRemove).
	if info, err := os.Stat(req.Path); err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("path is not a directory: %s", req.Path)
		}
		if _, err := os.Stat(filepath.Join(req.Path, ".git")); err != nil {
			return nil, fmt.Errorf("refusing to remove non-clone path (no .git): %s", req.Path)
		}
		if err := os.RemoveAll(req.Path); err != nil {
			return nil, fmt.Errorf("remove existing clone: %w", err)
		}
	}

	if err := cloneAtomically(ctx, req.Repo, branch, req.Path); err != nil {
		return nil, err
	}
	return json.Marshal(gitRecloneResponse{Success: true, Path: req.Path})
}

// truncateGitOutput keeps git pull/clone output bounded so a noisy upstream
// (e.g. tons of CHANGELOG output) doesn't pump megabytes back through the
// daemon command channel. Returns head + tail when over the cap.
func truncateGitOutput(out string) string {
	const cap = 4 * 1024
	if len(out) <= cap {
		return out
	}
	return out[:cap/2] + "\n…[truncated]…\n" + out[len(out)-cap/2:]
}
