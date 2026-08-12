// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/gitutil"
	"github.com/reliant-labs/reliant/internal/repo"
)

func init() {
	RegisterCommand("project.check_git", handleCheckGit)
	RegisterCommand("project.init_files", handleInitFiles)
	RegisterCommand("project.git_info", handleGitInfo)
	RegisterCommand("project.git_branches", handleGitBranches)
	RegisterCommand("project.check_init_status", handleCheckInitStatus)
	RegisterCommand("project.git_changes", handleGitChanges)
	RegisterCommand("project.init_git_repo", handleInitGitRepo)
}

// --- project.check_git ---

type checkGitRequest struct {
	Path string `json:"path"`
}

type checkGitResponse struct {
	IsGitRepo bool `json:"is_git_repo"`
}

func handleCheckGit(ctx context.Context, payload []byte) ([]byte, error) {
	var req checkGitRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// Always scan the filesystem — the scan is cheap and caching a positive
	// result would make the daemon lie after a `.git` is removed, which would
	// in turn pin the API's cached is_git_repo flag to a stale true. The flag
	// is a bidirectional cache of this observation, so this answer must
	// reflect the live filesystem in both directions.
	//
	// This uses the same nested-aware discovery as repo.discover rather than a
	// bare root stat. A multi-repo project (root with no `.git`, children that
	// have one) is a git project; answering false here would contradict
	// CreateProject, which derives the same flag from repo.Discover, and the
	// API would then persist that false over the correct value on every read.
	found, err := repo.Discover(ctx, req.Path, 0)
	if err != nil {
		// A path that can't be scanned (missing, not a dir) is not a git repo.
		// Report false rather than failing: the caller keeps its last-known
		// value on error, which would mask a genuinely removed `.git`.
		return json.Marshal(checkGitResponse{IsGitRepo: false})
	}
	return json.Marshal(checkGitResponse{IsGitRepo: len(found) > 0})
}

// --- project.init_files ---

type initFilesRequest struct {
	Path           string `json:"path"`
	DefaultContent string `json:"default_content"`
	// SkipReliantMD is set by per-repo inits in multi-repo projects:
	// repo-level reliant.md is opt-in (the user creates it if they want
	// repo-scoped memory), while the project root always gets a templated one.
	SkipReliantMD bool `json:"skip_reliant_md"`
}

type initFilesResponse struct {
	CreatedReliantMD    bool   `json:"created_reliant_md"`
	CreatedReliantDir   bool   `json:"created_reliant_dir"`
	CreatedSkillsDir    bool   `json:"created_skills_dir"`
	CreatedWorkflowsDir bool   `json:"created_workflows_dir"`
	Error               string `json:"error,omitempty"`
}

func handleInitFiles(_ context.Context, payload []byte) ([]byte, error) {
	var req initFilesRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	resp := initFilesResponse{}

	if !req.SkipReliantMD && req.DefaultContent != "" {
		reliantMDPath := filepath.Join(req.Path, "reliant.md")
		if _, err := os.Stat(reliantMDPath); os.IsNotExist(err) {
			if err := os.WriteFile(reliantMDPath, []byte(req.DefaultContent), 0644); err != nil {
				resp.Error = err.Error()
				return json.Marshal(resp)
			}
			resp.CreatedReliantMD = true
		}
	}

	reliantDir := filepath.Join(req.Path, ".reliant")
	if _, err := os.Stat(reliantDir); os.IsNotExist(err) {
		if err := os.MkdirAll(reliantDir, 0755); err != nil {
			resp.Error = err.Error()
			return json.Marshal(resp)
		}
		resp.CreatedReliantDir = true
	}

	// Always ensure the standard subdirs exist so downstream loaders can
	// scan them without distinguishing ENOENT from "empty directory."
	skillsDir := filepath.Join(reliantDir, "skills")
	if _, err := os.Stat(skillsDir); os.IsNotExist(err) {
		if err := os.MkdirAll(skillsDir, 0755); err != nil {
			resp.Error = err.Error()
			return json.Marshal(resp)
		}
		resp.CreatedSkillsDir = true
	}

	workflowsDir := filepath.Join(reliantDir, "workflows")
	if _, err := os.Stat(workflowsDir); os.IsNotExist(err) {
		if err := os.MkdirAll(workflowsDir, 0755); err != nil {
			resp.Error = err.Error()
			return json.Marshal(resp)
		}
		resp.CreatedWorkflowsDir = true
	}

	return json.Marshal(resp)
}

// --- project.git_info ---

type gitInfoRequest struct {
	Path string `json:"path"`
}

type gitInfoResponse struct {
	CurrentBranch  string   `json:"current_branch"`
	RemoteURL      string   `json:"remote_url"`
	HasChanges     bool     `json:"has_changes"`
	Status         string   `json:"status"`
	StagedFiles    []string `json:"staged_files"`
	UnstagedFiles  []string `json:"unstaged_files"`
	UntrackedFiles []string `json:"untracked_files"`
	Ahead          int32    `json:"ahead"`
	Behind         int32    `json:"behind"`
}

func handleGitInfo(ctx context.Context, payload []byte) ([]byte, error) {
	var req gitInfoRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	resp := gitInfoResponse{Status: "clean"}

	// Current branch
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = req.Path
	if out, err := branchCmd.Output(); err == nil {
		resp.CurrentBranch = strings.TrimSpace(string(out))
	} else {
		resp.CurrentBranch = "main"
	}

	// Remote URL
	remoteCmd := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
	remoteCmd.Dir = req.Path
	if out, err := remoteCmd.Output(); err == nil {
		resp.RemoteURL = strings.TrimSpace(string(out))
	}

	// Git status
	statusCmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "status", "--porcelain", "--untracked-files=all")
	statusCmd.Dir = req.Path
	if statusOutput, err := statusCmd.Output(); err == nil {
		lines := strings.Split(strings.TrimRight(string(statusOutput), "\n\r "), "\n")
		for _, line := range lines {
			if len(line) < 3 {
				continue
			}
			resp.HasChanges = true
			statusCode := line[:2]
			filePath := parseGitStatusPath(line[3:])
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
	}

	// Ahead/behind
	revListCmd := exec.CommandContext(ctx, "git", "rev-list", "--left-right", "--count", fmt.Sprintf("HEAD...origin/%s", resp.CurrentBranch))
	revListCmd.Dir = req.Path
	if out, err := revListCmd.Output(); err == nil {
		parts := strings.Fields(strings.TrimSpace(string(out)))
		if len(parts) == 2 {
			if a, err := strconv.Atoi(parts[0]); err == nil && a >= 0 && a <= int(^uint32(0)>>1) {
				resp.Ahead = int32(a) //nolint:gosec // bounds checked above
			}
			if b, err := strconv.Atoi(parts[1]); err == nil && b >= 0 && b <= int(^uint32(0)>>1) {
				resp.Behind = int32(b) //nolint:gosec // bounds checked above
			}
		}
	}

	return json.Marshal(resp)
}

// --- project.git_branches ---

type gitBranchesRequest struct {
	Path string `json:"path"`
}

type gitBranch struct {
	Name          string `json:"name"`
	IsCurrent     bool   `json:"is_current"`
	IsRemote      bool   `json:"is_remote"`
	IsDetached    bool   `json:"is_detached"`
	CommitSHA     string `json:"commit_sha,omitempty"`
	Upstream      string `json:"upstream,omitempty"`
	LastCommitAge int64  `json:"last_commit_age"`
}

type gitBranchesResponse struct {
	Branches []gitBranch `json:"branches"`
}

func handleGitBranches(ctx context.Context, payload []byte) ([]byte, error) {
	var req gitBranchesRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	resp := gitBranchesResponse{}

	// Check if HEAD is detached
	var isDetached bool
	var detachedCommitSHA string
	headCmd := exec.CommandContext(ctx, "git", "symbolic-ref", "-q", "HEAD")
	headCmd.Dir = req.Path
	if err := headCmd.Run(); err != nil {
		isDetached = true
		shaCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
		shaCmd.Dir = req.Path
		if shaOutput, err := shaCmd.Output(); err == nil {
			detachedCommitSHA = strings.TrimSpace(string(shaOutput))
		}
	}

	nowUnix := time.Now().Unix()

	// List all branches (local + remote) in one command
	refCmd := exec.CommandContext(ctx, "git", "for-each-ref",
		"--format=%(refname)|%(refname:short)|%(HEAD)|%(upstream:short)|%(committerdate:unix)",
		"refs/heads/", "refs/remotes/")
	refCmd.Dir = req.Path
	refOutput, err := refCmd.Output()
	if err != nil {
		return json.Marshal(resp)
	}

	lines := strings.Split(strings.TrimSpace(string(refOutput)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}

		fullRef := strings.TrimSpace(parts[0])
		name := strings.TrimSpace(parts[1])
		isCurrent := parts[2] == "*"
		upstream := strings.TrimSpace(parts[3])
		isRemote := strings.HasPrefix(fullRef, "refs/remotes/")

		// Skip HEAD pointer and origin-only entries
		if isRemote && (strings.HasSuffix(name, "/HEAD") || name == "origin") {
			continue
		}
		// Skip detached HEAD notation
		if strings.HasPrefix(name, "(HEAD detached") {
			continue
		}

		// Upstream is only meaningful for local branches
		if isRemote {
			upstream = ""
		}

		var lastCommitAge int64
		if commitTime, err := strconv.ParseInt(strings.TrimSpace(parts[4]), 10, 64); err == nil {
			lastCommitAge = nowUnix - commitTime
		}

		resp.Branches = append(resp.Branches, gitBranch{
			Name:          name,
			IsCurrent:     isCurrent && !isDetached,
			IsRemote:      isRemote,
			Upstream:      upstream,
			LastCommitAge: lastCommitAge,
		})
	}

	// If detached HEAD, add special entry
	if isDetached && detachedCommitSHA != "" {
		shortSHA := detachedCommitSHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		resp.Branches = append([]gitBranch{{
			Name:       shortSHA,
			IsCurrent:  true,
			IsDetached: true,
			CommitSHA:  detachedCommitSHA,
		}}, resp.Branches...)
	}

	return json.Marshal(resp)
}

// --- project.check_init_status ---

type checkInitStatusRequest struct {
	Path      string `json:"path"`
	IsGitRepo bool   `json:"is_git_repo"`
}

type checkInitStatusResponse struct {
	Initialized bool   `json:"initialized"`
	Message     string `json:"message"`
}

func handleCheckInitStatus(_ context.Context, payload []byte) ([]byte, error) {
	var req checkInitStatusRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	resp := checkInitStatusResponse{
		Initialized: true,
		Message:     "Project is initialized",
	}

	if _, err := os.Stat(req.Path); os.IsNotExist(err) {
		resp.Initialized = false
		resp.Message = "Project path does not exist"
	} else if req.IsGitRepo {
		gitPath := filepath.Join(req.Path, ".git")
		if _, err := os.Stat(gitPath); os.IsNotExist(err) {
			resp.Initialized = false
			resp.Message = "Git repository not initialized"
		}
	}

	return json.Marshal(resp)
}

// --- project.git_changes ---

type gitChangesRequest struct {
	Path string `json:"path"`
}

type fileChange struct {
	Path            string `json:"path"`
	Status          string `json:"status"`
	IsNew           bool   `json:"is_new"`
	Diff            string `json:"diff,omitempty"`
	Content         string `json:"content,omitempty"`
	OriginalContent string `json:"original_content,omitempty"`
}

type gitChangesResponse struct {
	Branch     string       `json:"branch"`
	Files      []fileChange `json:"files"`
	TotalFiles int32        `json:"total_files"`
}

func handleGitChanges(ctx context.Context, payload []byte) ([]byte, error) {
	var req gitChangesRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	resp := gitChangesResponse{Files: []fileChange{}}

	// Get current branch
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = req.Path
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get current branch: %w", err)
	}
	resp.Branch = strings.TrimSpace(string(branchOutput))

	// Get git status
	statusCmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "status", "--porcelain", "--untracked-files=all")
	statusCmd.Dir = req.Path
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get git status: %w", err)
	}

	lines := strings.Split(string(statusOutput), "\n")
	for _, line := range lines {
		if len(line) < 4 {
			continue
		}
		status := strings.TrimSpace(line[:2])
		filePath := parseGitStatusPath(line[3:])
		if filePath == "" {
			continue
		}

		fc := fileChange{Path: filePath}

		switch {
		case strings.Contains(status, "M"):
			fc.Status = "modified"
			fc.Diff = gitDiffOutput(ctx, req.Path, filePath)
			fc.Content = readFileContent(req.Path, filePath)
			fc.OriginalContent = gitShowHEAD(ctx, req.Path, filePath)
		case strings.Contains(status, "A"):
			fc.Status = "staged"
			fc.IsNew = true
			fc.Content = readFileContent(req.Path, filePath)
		case strings.Contains(status, "D"):
			fc.Status = "deleted"
			fc.OriginalContent = gitShowHEAD(ctx, req.Path, filePath)
		case strings.Contains(status, "?"):
			fc.Status = "untracked"
			fc.IsNew = true
			fc.Content = readFileContent(req.Path, filePath)
		default:
			fc.Status = "modified"
			fc.Diff = gitDiffOutput(ctx, req.Path, filePath)
			fc.Content = readFileContent(req.Path, filePath)
			fc.OriginalContent = gitShowHEAD(ctx, req.Path, filePath)
		}

		resp.Files = append(resp.Files, fc)
	}

	resp.TotalFiles = int32(len(resp.Files))
	return json.Marshal(resp)
}

// --- project.init_git_repo ---

type initGitRepoRequest struct {
	Path              string   `json:"path"`
	InitialBranch     string   `json:"initial_branch"`
	GitignorePatterns []string `json:"gitignore_patterns"`
	InitialCommit     bool     `json:"initial_commit"`
	// OnlyIfEmpty gates auto-init: when true, init is skipped (Success=false,
	// no error) if the directory already contains real content, so an
	// existing non-empty folder a user opened is left untouched. A brand-new
	// empty project (including a fresh cloud workspace, whose only possible
	// entry is the reliant.md scaffold) still initializes. The manual
	// "Initialize Git" flow leaves this false and always inits.
	OnlyIfEmpty bool `json:"only_if_empty"`
}

type initGitRepoResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func handleInitGitRepo(ctx context.Context, payload []byte) ([]byte, error) {
	var req initGitRepoRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// Check path exists
	if _, err := os.Stat(req.Path); os.IsNotExist(err) {
		return json.Marshal(initGitRepoResponse{Error: "project path does not exist"})
	}

	// Normalize + validate the branch name up front so a bad value (e.g.
	// a trailing space from the UI) fails cleanly before any state exists.
	req.InitialBranch = gitutil.NormalizeBranchName(req.InitialBranch)
	if err := gitutil.ValidateBranchName(req.InitialBranch); err != nil {
		return json.Marshal(initGitRepoResponse{Error: err.Error()})
	}

	// NOTE: an existing .git is NOT an error — gitutil.InitGitRepository
	// adopts it (retry-safe recovery from a previously failed init).

	// Auto-init gate: only initialize an EMPTY project. Skips (without error)
	// when the directory already holds real content, so an existing folder a
	// user opened is left alone and the app prompts instead.
	if req.OnlyIfEmpty {
		empty, err := dirIsEffectivelyEmpty(req.Path)
		if err != nil {
			return json.Marshal(initGitRepoResponse{Error: fmt.Sprintf("check dir empty: %v", err)})
		}
		if !empty {
			return json.Marshal(initGitRepoResponse{Success: false, Error: "directory not empty; skipping auto git init"})
		}
	}

	if len(req.GitignorePatterns) == 0 {
		req.GitignorePatterns = gitutil.DefaultGitignorePatterns()
	}

	opts := gitutil.InitGitRepositoryOptions{
		Path:              req.Path,
		InitialBranch:     req.InitialBranch,
		GitignorePatterns: req.GitignorePatterns,
		InitialCommit:     req.InitialCommit,
	}

	if err := gitutil.InitGitRepository(ctx, opts); err != nil {
		return json.Marshal(initGitRepoResponse{Error: err.Error()})
	}

	return json.Marshal(initGitRepoResponse{Success: true})
}

// dirIsEffectivelyEmpty reports whether path contains no real content —
// treating the reliant scaffold (reliant.md / .reliant) and an existing .git
// as "empty" so a freshly created project (including a cloud workspace that
// already has reliant.md written) still qualifies for auto git-init, while a
// folder with actual files does not.
func dirIsEffectivelyEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		switch e.Name() {
		case "reliant.md", ".reliant", ".git":
			continue
		}
		return false, nil
	}
	return true, nil
}

// --- Helpers ---

func gitDiffOutput(ctx context.Context, repoPath, filePath string) string {
	cmd := exec.CommandContext(ctx, "git", "diff", "HEAD", "--", filePath)
	cmd.Dir = repoPath
	if out, err := cmd.Output(); err == nil {
		return string(out)
	}
	return ""
}

func gitShowHEAD(ctx context.Context, repoPath, filePath string) string {
	cmd := exec.CommandContext(ctx, "git", "show", "HEAD:"+filePath)
	cmd.Dir = repoPath
	if out, err := cmd.Output(); err == nil {
		return string(out)
	}
	return ""
}

func readFileContent(repoPath, filePath string) string {
	if content, err := os.ReadFile(filepath.Join(repoPath, filePath)); err == nil {
		return string(content)
	}
	return ""
}

// parseGitStatusPath parses a file path field from `git status --porcelain` output.
func parseGitStatusPath(rawPath string) string {
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
