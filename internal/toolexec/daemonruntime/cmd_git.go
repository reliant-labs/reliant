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

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git clone failed: %v: %s", err, sanitizeGitOutput(string(output), req.Token))
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
