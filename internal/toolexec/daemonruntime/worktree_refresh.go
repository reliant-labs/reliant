package daemonruntime

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/reliant-labs/reliant/internal/gitref"
)

var fetchedWorktreeSHA = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// refreshWorktreeHead fetches one validated branch as argv data, never shell
// source, and refuses drift before any checkout is created. The caller then
// uses the immutable expected SHA, never a local branch preferred by resolution.
func refreshWorktreeHead(ctx context.Context, repoPath, branch, expected string) error {
	if err := gitref.ValidateBranchName(branch); err != nil {
		return err
	}
	if !fetchedWorktreeSHA.MatchString(expected) {
		return fmt.Errorf("expected_head must be an exact commit SHA")
	}
	if !fetchOriginBranch(ctx, repoPath, branch) {
		return fmt.Errorf("cannot refresh target branch from origin")
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "refs/remotes/origin/"+branch+"^{commit}")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("resolve refreshed target branch: %w", err)
	}
	if strings.TrimSpace(string(out)) != expected {
		return fmt.Errorf("PR head moved after it was read: refusing worktree checkout (expected %s)", expected)
	}
	return nil
}
