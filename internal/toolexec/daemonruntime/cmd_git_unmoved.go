// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func init() {
	RegisterCommand(unmovedWorkCommand, handleGitUnmovedWork)
}

// unmovedWorkCommand reports, for each repository of a checkout, the work in
// it that a fresh clone elsewhere would not carry: uncommitted changes,
// branches with commits on no remote, stashes and extra git worktrees. It is
// read-only: it runs git's own read commands and writes nothing, not even
// git's index (GIT_OPTIONAL_LOCKS=0). The server asks it before a project is
// set up on another machine (PreviewProjectMachineMove).
const unmovedWorkCommand = "git.unmoved_work"

const (
	// unmovedChangedFilesMax bounds the changed paths named per repository;
	// the count is always whole.
	unmovedChangedFilesMax = 20
	// unmovedBranchesMax bounds the local branches inspected per repository:
	// each costs one git process.
	unmovedBranchesMax = 200
)

type gitUnmovedWorkRequest struct {
	// Path is the checkout's root on this machine.
	Path string `json:"path"`
	// Repos are the repositories to report, relative to Path ("" is the
	// root itself).
	Repos []string `json:"repos"`
}

type gitUnmovedWorkResponse struct {
	Repos []unmovedRepo `json:"repos"`
}

type unmovedRepo struct {
	RelativePath     string            `json:"relative_path"`
	ChangedFileCount int               `json:"changed_file_count"`
	ChangedFiles     []string          `json:"changed_files,omitempty"`
	UnpushedBranches []unmovedBranch   `json:"unpushed_branches,omitempty"`
	StashCount       int               `json:"stash_count"`
	Worktrees        []unmovedWorktree `json:"worktrees,omitempty"`
	// Error is why this repository could not be read; the others still are.
	Error string `json:"error,omitempty"`
}

type unmovedBranch struct {
	Name    string `json:"name"`
	Commits int    `json:"commits"`
}

type unmovedWorktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
}

func handleGitUnmovedWork(ctx context.Context, payload []byte) ([]byte, error) {
	var req gitUnmovedWorkRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	root := strings.TrimSpace(req.Path)
	if root == "" {
		return nil, fmt.Errorf("path is required")
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("the checkout is not on this machine: %s is not a directory", root)
	}
	repos := req.Repos
	if len(repos) == 0 {
		repos = []string{""}
	}
	resp := gitUnmovedWorkResponse{Repos: make([]unmovedRepo, 0, len(repos))}
	for _, rel := range repos {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		resp.Repos = append(resp.Repos, unmovedWorkOf(ctx, root, rel))
	}
	return json.Marshal(resp)
}

// unmovedWorkOf reads one repository. A repository that cannot be read says
// why in Error rather than failing the whole report.
func unmovedWorkOf(ctx context.Context, root, rel string) unmovedRepo {
	out := unmovedRepo{RelativePath: rel}
	clean := filepath.Clean(filepath.FromSlash(strings.Trim(rel, "/")))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		out.Error = fmt.Sprintf("%q is not a path inside the checkout", rel)
		return out
	}
	dir := filepath.Join(root, clean)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		out.Error = fmt.Sprintf("no git repository at %s", dir)
		return out
	}

	status, err := readGit(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.ChangedFiles, out.ChangedFileCount = parsePorcelainZ(status, unmovedChangedFilesMax)

	branches, err := unpushedBranches(ctx, dir)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.UnpushedBranches = branches

	stashes, err := readGit(ctx, dir, "stash", "list", "--format=%gd")
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.StashCount = countLines(stashes)

	worktrees, err := readGit(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Worktrees = extraWorktrees(worktrees)
	return out
}

// readGit runs a read-only git command in dir and returns its stdout.
func readGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// No index refresh, no prompts: this must never write to the checkout or
	// wait on a credential.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0") //nolint:forbidigo // passes this process environment to a child process; not configuration
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s failed: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout, nil
}

// parsePorcelainZ counts the entries of `git status --porcelain=v1 -z` and
// returns the first max paths, sorted. A rename or copy entry is followed by
// its source path, which is not a second change.
func parsePorcelainZ(out []byte, max int) ([]string, int) {
	fields := strings.Split(string(out), "\x00")
	var paths []string
	count := 0
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		count++
		if len(paths) < max {
			paths = append(paths, f[3:])
		}
		if x := f[0]; x == 'R' || x == 'C' {
			i++ // the source path
		}
	}
	sort.Strings(paths)
	return paths, count
}

// unpushedBranches is every local branch with commits that no
// remote-tracking branch has: in a repository with no remote at all, every
// branch with a commit.
func unpushedBranches(ctx context.Context, dir string) ([]unmovedBranch, error) {
	refs, err := readGit(ctx, dir, "for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil {
		return nil, err
	}
	var out []unmovedBranch
	for i, ref := range strings.Split(strings.TrimSpace(string(refs)), "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if i >= unmovedBranchesMax {
			break
		}
		n, err := readGit(ctx, dir, "rev-list", "--count", ref, "--not", "--remotes")
		if err != nil {
			return nil, err
		}
		commits, err := strconv.Atoi(strings.TrimSpace(string(n)))
		if err != nil || commits == 0 {
			continue
		}
		out = append(out, unmovedBranch{Name: strings.TrimPrefix(ref, "refs/heads/"), Commits: commits})
	}
	return out, nil
}

// extraWorktrees is `git worktree list --porcelain` without its first entry,
// the main checkout.
func extraWorktrees(out []byte) []unmovedWorktree {
	var all []unmovedWorktree
	for _, block := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
		var wt unmovedWorktree
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				wt.Path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "branch "):
				wt.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
			}
		}
		if wt.Path != "" {
			all = append(all, wt)
		}
	}
	if len(all) <= 1 {
		return nil
	}
	return all[1:]
}

func countLines(out []byte) int {
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
