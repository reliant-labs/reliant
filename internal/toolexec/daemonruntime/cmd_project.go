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

func handleCheckGit(_ context.Context, payload []byte) ([]byte, error) {
	var req checkGitRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	resp := checkGitResponse{IsGitRepo: gitutil.IsGitRepository(req.Path)}
	return json.Marshal(resp)
}

// --- project.init_files ---

type initFilesRequest struct {
	Path           string `json:"path"`
	DefaultContent string `json:"default_content"`
}

type initFilesResponse struct {
	Created bool   `json:"created"`
	Error   string `json:"error,omitempty"`
}

func handleInitFiles(_ context.Context, payload []byte) ([]byte, error) {
	var req initFilesRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	reliantMDPath := filepath.Join(req.Path, "reliant.md")
	resp := initFilesResponse{}
	if _, err := os.Stat(reliantMDPath); os.IsNotExist(err) {
		if err := os.WriteFile(reliantMDPath, []byte(req.DefaultContent), 0644); err != nil {
			resp.Error = err.Error()
		} else {
			resp.Created = true
		}
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
				resp.Ahead = int32(a)
			}
			if b, err := strconv.Atoi(parts[1]); err == nil && b >= 0 && b <= int(^uint32(0)>>1) {
				resp.Behind = int32(b)
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

	// Prune stale remote tracking branches
	pruneCmd := exec.CommandContext(ctx, "git", "fetch", "--prune")
	pruneCmd.Dir = req.Path
	_ = pruneCmd.Run()

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

	// Local branches
	localCmd := exec.CommandContext(ctx, "git", "branch", "--format=%(refname:short)|%(HEAD)|%(upstream:short)|%(committerdate:unix)")
	localCmd.Dir = req.Path
	localOutput, err := localCmd.CombinedOutput()
	if err != nil {
		return json.Marshal(resp)
	}

	localBranchNames := make(map[string]bool)
	localLines := strings.Split(strings.TrimSpace(string(localOutput)), "\n")
	for _, line := range localLines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		isCurrent := parts[1] == "*"

		if strings.HasPrefix(name, "(HEAD detached") {
			continue
		}
		localBranchNames[name] = true

		upstream := ""
		if len(parts) > 2 {
			upstream = strings.TrimSpace(parts[2])
		}

		var lastCommitAge int64
		if len(parts) > 3 {
			if commitTime, err := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64); err == nil {
				lastCommitAge = nowUnix - commitTime
			}
		}

		resp.Branches = append(resp.Branches, gitBranch{
			Name:          name,
			IsCurrent:     isCurrent && !isDetached,
			IsRemote:      false,
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

	// Remote branches
	remoteCmd := exec.CommandContext(ctx, "git", "branch", "-r", "--format=%(refname:short)|%(committerdate:unix)")
	remoteCmd.Dir = req.Path
	remoteOutput, err := remoteCmd.CombinedOutput()
	if err == nil {
		remoteLines := strings.Split(strings.TrimSpace(string(remoteOutput)), "\n")
		for _, line := range remoteLines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "|")
			if len(parts) < 1 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			if strings.HasSuffix(name, "/HEAD") || name == "origin" {
				continue
			}
			var lastCommitAge int64
			if len(parts) > 1 {
				if commitTime, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64); err == nil {
					lastCommitAge = nowUnix - commitTime
				}
			}
			resp.Branches = append(resp.Branches, gitBranch{
				Name:          name,
				IsRemote:      true,
				LastCommitAge: lastCommitAge,
			})
		}
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

	// Check if already git repo
	if gitutil.IsGitRepository(req.Path) {
		return json.Marshal(initGitRepoResponse{Error: "a .git directory already exists at this path"})
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
