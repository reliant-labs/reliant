// Copyright (c) 2025 Reliant Labs
package gatehost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
)

// GitFetcher is the gate's fetch step: a fresh repository in the gate's
// directory, one fetch of the candidate ref from GitHub (after one of the
// batch's base commit, when the request names it), a check that it is the
// candidate sha, and a detached checkout of it.
//
// The credential is the machine's, given to git for the one fetch through
// Env (the daemon's per-invocation credential helper config). Nothing of it
// is written into the directory: the fetch names a URL, never a remote, so
// .git/config holds no URL, helper or header, and Fetch checks that before it
// hands the tree to the boundary.
type GitFetcher struct {
	// Env is git's environment (required): the daemon passes its own with
	// the credential helper's per-invocation config.
	Env func() []string
	// BaseURL is where repositories are fetched from; "" is
	// https://github.com. Tests point it at a directory of bare repos.
	BaseURL string
	// Depth is the fetch depth; 0 is 1.
	Depth int
}

// Fetch implements Fetcher.
func (f GitFetcher) Fetch(ctx context.Context, spec FetchSpec) error {
	base := strings.TrimRight(f.BaseURL, "/")
	if base == "" {
		base = "https://github.com"
	}
	url := base + "/" + spec.Repo + ".git"
	if f.Env == nil {
		return errors.New("git fetcher has no environment")
	}
	env := append(f.Env(), "GIT_TERMINAL_PROMPT=0")
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.Env = spec.Dir, env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(stdout.String()), nil
	}
	if _, err := git("init", "-q"); err != nil {
		return err
	}
	depth := "--depth=" + strconv.Itoa(max(f.Depth, 1))
	candidate := []string{"fetch", "--quiet", "--no-tags", depth, url, "+refs/heads/" + spec.Ref + ":refs/gate/candidate"}
	if spec.BaseSHA != "" {
		if !shaPattern.MatchString(spec.BaseSHA) {
			return fmt.Errorf("%w: base sha %q is not a full commit sha", gaterun.ErrInvalidRequest, spec.BaseSHA)
		}
		// The base first, shallow, then the candidate with no depth: the
		// base is a common commit, so the second fetch brings only the
		// commits the candidate adds over it (a batch is one squash commit
		// per entry on its base), and the base stays reachable from the
		// candidate for merge-base and diff.
		if _, err := git("fetch", "--quiet", "--no-tags", depth, url, spec.BaseSHA+":"+BaseRef); err != nil {
			return fmt.Errorf("fetch the base %s: %w", spec.BaseSHA, err)
		}
		candidate = slices.DeleteFunc(candidate, func(a string) bool { return a == depth })
	}
	if _, err := git(candidate...); err != nil {
		return err
	}
	got, err := git("rev-parse", "--verify", "refs/gate/candidate^{commit}")
	if err != nil {
		return err
	}
	if got != spec.SHA {
		return fmt.Errorf("%w: %s is at %s, the candidate is %s", ErrCandidateMoved, spec.Ref, got, spec.SHA)
	}
	if _, err := git("-c", "advice.detachedHead=false", "checkout", "-q", "--detach", spec.SHA); err != nil {
		return err
	}
	return checkNoCredential(spec.Dir)
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// BaseRef is where the gate's checkout holds the batch's base commit, when
// the request names one.
const BaseRef = "refs/gate/base"

// credentialConfig matches what would carry a credential in a repository's
// config: a helper, an extra header, or a URL with user info.
var credentialConfig = regexp.MustCompile(`(?i)credential|extraheader|askpass|://[^/\s]*@`)

// checkNoCredential refuses a fetched tree whose git config could carry a
// credential into the boundary.
func checkNoCredential(dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		return fmt.Errorf("read the fetched repository's config: %w", err)
	}
	if m := credentialConfig.Find(b); m != nil {
		return fmt.Errorf("the fetched repository's config holds %q; refusing to hand it to the gate", m)
	}
	return nil
}
