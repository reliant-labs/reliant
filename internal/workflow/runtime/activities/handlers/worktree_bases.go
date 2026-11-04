package handlers

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitref"
)

var worktreeHeadSHA = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// repoWorktreeBases translates registered relative paths to the IDs consumed
// by workspacecreate. Unknown paths never become filesystem authority.
func repoWorktreeBases(repos []*core.Repo, target, base string, overrides map[string]string) (map[string]string, string, error) {
	byPath := map[string]*core.Repo{}
	for _, repo := range repos {
		byPath[repo.RelativePath] = repo
	}
	bases := map[string]string{}
	for path, branch := range overrides {
		repo, ok := byPath[path]
		if !ok || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") || (path != "" && filepath.Clean(path) != path) {
			return nil, "", fmt.Errorf("base_branches: %q is not a registered repo of this project", path)
		}
		if err := gitref.ValidateBranchName(branch); err != nil {
			return nil, "", err
		}
		bases[repo.ID] = branch
	}
	if target == "" {
		return bases, "", nil
	}
	repo, ok := byPath[target]
	// A standalone project registers its root as "". Its repo name remains
	// an explicit identity, not permission to use an arbitrary project.path.
	if !ok && len(repos) == 1 && repos[0].RelativePath == "" && repos[0].Name == target {
		repo, ok = repos[0], true
	}
	if !ok || filepath.IsAbs(target) || target == ".." || strings.HasPrefix(target, "../") || filepath.Clean(target) != target {
		return nil, "", fmt.Errorf("target_repo %q is not a registered repo of this project", target)
	}
	if base != "" {
		if err := gitref.ValidateBranchName(base); err != nil {
			return nil, "", err
		}
		bases[repo.ID] = base
	}
	return bases, repo.ID, nil
}

func validateWorktreeFetch(targetID, branch, expected string) error {
	if branch == "" && expected == "" {
		return nil
	}
	if targetID == "" || !worktreeHeadSHA.MatchString(expected) {
		return fmt.Errorf("fetch_branch requires a registered target_repo and exact expected_head SHA")
	}
	return gitref.ValidateBranchName(branch)
}
