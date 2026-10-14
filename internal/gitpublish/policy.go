// Copyright (c) 2025 Reliant Labs
package gitpublish

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrPullRequestNotFound is returned by GitHubReader.PullRequest for a number
// that names no pull request in the repository.
var ErrPullRequestNotFound = errors.New("pull request not found")

// branchShape is a plain branch name: letters, digits and . _ / - only. It is
// stricter than git, on purpose: a name outside it is never a branch a workflow
// meant to publish to.
var branchShape = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// alwaysRefused are branch names no publish may touch, whatever the repository
// says its default is.
var alwaysRefused = []string{"main", "master", "HEAD", "release/*"}

// Check decides a request. A refusal is a Decision with Allowed false, not an
// error: the node reports it and the graph decides what to do. An error means
// the policy could not be evaluated (GitHub unreachable), and nothing may be
// pushed: it fails closed.
func Check(ctx context.Context, gh GitHubReader, req Request) (Decision, error) {
	refuse := func(def, format string, args ...any) (Decision, error) {
		return Decision{Reason: fmt.Sprintf(format, args...), Default: def}, nil
	}
	if req.Repo.Owner == "" || req.Repo.Name == "" {
		return refuse("", "the repository must be owner/name")
	}

	def, err := gh.DefaultBranch(ctx, req.Repo)
	if err != nil {
		return Decision{}, fmt.Errorf("read %s's default branch: %w", req.Repo, err)
	}

	branch := strings.TrimSpace(req.Branch)
	viaPR := req.PRHeadOf != 0
	if viaPR {
		if branch != "" {
			return refuse(def, "set either branch or pr_head_of, not both")
		}
		pr, err := gh.PullRequest(ctx, req.Repo, req.PRHeadOf)
		if errors.Is(err, ErrPullRequestNotFound) {
			return refuse(def, "pull request #%d does not exist in %s", req.PRHeadOf, req.Repo)
		}
		if err != nil {
			return Decision{}, fmt.Errorf("read pull request #%d: %w", req.PRHeadOf, err)
		}
		if !strings.EqualFold(pr.State, "open") {
			return refuse(def, "pull request #%d is %s", req.PRHeadOf, pr.State)
		}
		if !strings.EqualFold(pr.HeadRepo, req.Repo.String()) || !strings.EqualFold(pr.BaseRepo, req.Repo.String()) {
			return refuse(def, "pull request #%d comes from %s, a different repository from %s: publishing to a fork is not allowed",
				req.PRHeadOf, orUnknown(pr.HeadRepo), req.Repo)
		}
		branch = pr.HeadRef
	}

	if branch == "" {
		return refuse(def, "no branch to publish to: set branch or pr_head_of")
	}
	if !ValidBranch(branch) {
		return refuse(def, "%q is not a plain branch name (letters, digits and . _ / - only; no refs/ prefix, no tags)", branch)
	}
	if strings.EqualFold(branch, def) {
		return refuse(def, "%q is the repository's default branch", branch)
	}
	if NeverPublishable(branch) {
		return refuse(def, "%q is a protected name (main, master and release/* are never published to)", branch)
	}
	// A pull request's own head is the one thing the allow list does not gate:
	// the pull request already names it, and the same-repository check above is
	// what keeps it safe.
	if !viaPR && len(req.Allow) > 0 && !matchesAny(req.Allow, branch) {
		return refuse(def, "%q matches none of the allowed branches %v", branch, req.Allow)
	}

	protected, err := gh.Protected(ctx, req.Repo, branch)
	if err != nil {
		return Decision{}, fmt.Errorf("read whether %q is protected: %w", branch, err)
	}
	if protected {
		return refuse(def, "%q is a protected branch", branch)
	}
	return Decision{Allowed: true, Branch: branch, Default: def}, nil
}

// NeverPublishable reports whether name is a branch no publish touches whatever
// the repository's default is: main, master, HEAD and release/*. The machine
// repeats this check; the repository's own default and protected branches are
// the server's to know.
func NeverPublishable(name string) bool {
	for _, p := range alwaysRefused {
		if matchGlob(p, name) {
			return true
		}
	}
	return false
}

// RemoteURL is where a push to repo goes. It is built from the node's repo and
// nothing else, so no remote configured in the checkout can redirect it.
func RemoteURL(repo Repo) string {
	return "https://github.com/" + repo.Owner + "/" + repo.Name + ".git"
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown repository"
	}
	return s
}

// ValidBranch reports whether name is a plain branch git would accept. The
// machine checks it again before it pushes: it does not trust the server's word
// for what goes into a refspec.
func ValidBranch(name string) bool {
	if !branchShape.MatchString(name) {
		return false
	}
	if strings.HasPrefix(name, "refs/") || strings.HasPrefix(name, "-") ||
		strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".") || strings.Contains(name, "..") ||
		strings.Contains(name, "//") || strings.HasSuffix(name, ".lock") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// matchGlob matches name against a pattern where * matches any run of
// characters including "/", so "autopilot/*" covers "autopilot/a/b" and
// "release/*" covers every release branch. path.Match's * stops at "/", which
// would let "release/v1/hotfix" slip past "release/*".
func matchGlob(pattern, name string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	rest := name[len(parts[0]):]
	for i := 1; i < len(parts); i++ {
		last := i == len(parts)-1
		if last {
			return strings.HasSuffix(rest, parts[i]) && len(rest) >= len(parts[i])
		}
		idx := strings.Index(rest, parts[i])
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(parts[i]):]
	}
	return true
}

func matchesAny(patterns []string, name string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if matchGlob(p, name) {
			return true
		}
	}
	return false
}
