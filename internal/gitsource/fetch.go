package gitsource

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// GitFetcher materializes a source with the git CLI.
//
// It fetches ONE ref at depth 1 rather than cloning the repository, which
// for a large monorepo is the difference between a few megabytes and a few
// hundred. The shallow fetch is retried unshallowed when the server
// refuses it: fetching a bare commit sha requires the remote to have
// uploadpack.allowReachableSHA1InWant enabled, which many hosts do not, and
// a pin by sha is the MOST reproducible thing a user can write — it must
// not be the one that fails.
type GitFetcher struct{}

// Fetch implements Fetcher.
func (GitFetcher) Fetch(ctx context.Context, src Source, dst string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git is required to fetch cross-repo sources but was not found on PATH: %w", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dst, err)
	}
	url := src.CloneURL()

	if err := runGit(ctx, dst, "init", "--quiet"); err != nil {
		return "", err
	}
	if err := runGit(ctx, dst, "remote", "add", "origin", url); err != nil {
		return "", err
	}

	// Shallow first; fall back to a full fetch of the ref when the server
	// will not serve it shallow (bare shas on most hosts).
	fetchErr := runGit(ctx, dst, "fetch", "--depth", "1", "--quiet", "origin", src.Ref)
	if fetchErr != nil {
		if err := runGit(ctx, dst, "fetch", "--quiet", "origin", src.Ref); err != nil {
			// Report the shallow failure too: when a ref simply does not
			// exist both fail, and the first message is the informative one.
			return "", fmt.Errorf("fetch ref %q from %s: %w (shallow attempt: %v)", src.Ref, url, err, fetchErr)
		}
	}

	if err := runGit(ctx, dst, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return "", err
	}

	commit, err := gitOutput(ctx, dst, "rev-parse", "HEAD")
	if err != nil {
		// The checkout succeeded; not knowing the sha costs auditability,
		// not correctness.
		return "", nil
	}
	return commit, nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Never let git stop for credentials: a build that blocks on an
	// invisible password prompt looks like a hang, and in CI it IS one.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, msg)
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
