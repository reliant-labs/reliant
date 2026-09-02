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
	Token  string `json:"token,omitempty"`
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
	// the clone on disk. `git clone` itself refuses to run into an existing
	// non-empty directory, so without this check a redelivered clone that
	// already succeeded would report failure on retry even though req.Path
	// is a valid, complete clone. Only short-circuits when req.Path already
	// looks like a real clone (has .git); any other pre-existing conflict
	// (a non-git directory in the way) still falls through to the normal
	// clone attempt and its normal failure.
	if _, err := os.Stat(filepath.Join(req.Path, ".git")); err == nil {
		return json.Marshal(gitCloneResponse{
			Success: true,
			Path:    req.Path,
		})
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(req.Path), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %v", err)
	}

	// Build the repo URL with token if provided
	cloneURL := req.Repo
	if req.Token != "" {
		cloneURL = injectTokenInURL(req.Repo, req.Token)
	}

	// Run git clone
	args := []string{"clone", "--branch", req.Branch, cloneURL, req.Path}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	// git clone can be memory-heavy on large repos; attribute a SIGKILL to
	// the workspace OOM killer when the cgroup recorded one.
	oomSnap := memReader.SnapshotOOMKills()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git clone failed: %v: %s", wrapChildOOMKill(err, oomSnap), sanitizeGitOutput(string(output), req.Token))
	}

	// If token was provided, set up credential helper for this repo so push/pull works
	if req.Token != "" {
		setupCredentialForRepo(ctx, req.Path, req.Repo, req.Token)
	}

	return json.Marshal(gitCloneResponse{
		Success: true,
		Path:    req.Path,
	})
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

// injectTokenInURL adds an access token to an HTTPS git URL.
// e.g. "https://github.com/org/repo" -> "https://x-access-token:TOKEN@github.com/org/repo"
func injectTokenInURL(repoURL, token string) string {
	if strings.HasPrefix(repoURL, "https://") {
		return strings.Replace(repoURL, "https://", "https://x-access-token:"+token+"@", 1)
	}
	if strings.HasPrefix(repoURL, "http://") {
		return strings.Replace(repoURL, "http://", "http://x-access-token:"+token+"@", 1)
	}
	return repoURL
}

// sanitizeGitOutput removes tokens from git output to prevent leaking secrets.
func sanitizeGitOutput(output, token string) string {
	if token == "" {
		return output
	}
	return strings.ReplaceAll(output, token, "***")
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
	// Token is an optional access token for private repos.
	Token string `json:"token,omitempty"`
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

	if err := os.MkdirAll(filepath.Dir(req.Path), 0755); err != nil {
		return nil, fmt.Errorf("failed to create parent directory: %w", err)
	}

	cloneURL := req.Repo
	if req.Token != "" {
		cloneURL = injectTokenInURL(req.Repo, req.Token)
	}
	args := []string{"clone", "--branch", branch, cloneURL, req.Path}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone failed: %v: %s", err, sanitizeGitOutput(string(output), req.Token))
	}
	if req.Token != "" {
		setupCredentialForRepo(ctx, req.Path, req.Repo, req.Token)
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

// setupCredentialForRepo configures git credential storage for the cloned repo
// so that subsequent push/pull operations use the token.
func setupCredentialForRepo(ctx context.Context, repoPath, repoURL, token string) {
	// Set up credential.helper store for this repo
	_ = exec.CommandContext(ctx, "git", "-C", repoPath, "config", "credential.helper", "store").Run()

	// Determine the host from the repo URL
	host := "github.com"
	if strings.Contains(repoURL, "gitlab.com") {
		host = "gitlab.com"
	}

	// Write credential to the store file
	homeDir, _ := os.UserHomeDir()
	credFile := filepath.Join(homeDir, ".git-credentials")
	credLine := fmt.Sprintf("https://x-access-token:%s@%s\n", token, host)

	f, err := os.OpenFile(credFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err == nil {
		_, _ = f.WriteString(credLine)
		f.Close()
	}
}
