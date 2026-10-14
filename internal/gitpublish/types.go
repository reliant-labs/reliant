// Copyright (c) 2025 Reliant Labs

// Package gitpublish decides whether a git_publish node may push, and defines
// what the daemon's git.publish_prepare and git.publish commands exchange with
// the server.
//
// The decision runs on the server, in the workflow activity, never on the
// machine: the machine holds the checkout and nothing else it needs to judge.
// The policy asks GitHub, through the run's identity, what the repository's
// default branch is, whether a branch is protected, and whether a pull
// request's head is in this repository.
//
// What the policy refuses, always:
//
//   - the repository's default branch, any protected branch, main, master and
//     release/*;
//   - a name that is not a plain branch (refs/..., tags, odd characters);
//   - a branch outside the node's allow list, when it has one;
//   - a pull request head in a fork, a closed pull request, or a head whose
//     name differs from the one the pull request names.
//
// It never decides to force-push, because the push command has no force path.
//
//forge:exclude-contract: pure branch policy over a consumer-declared GitHub reader; no state, no I/O of its own
package gitpublish

import "context"

// Repo is a GitHub repository, as the node names it.
type Repo struct {
	Owner string
	Name  string
}

// String is "owner/name".
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// GitHubReader answers the three questions the policy puts to GitHub. The
// activity implements it over the github integration's read actions, with the
// run's identity; tests fake it.
type GitHubReader interface {
	// DefaultBranch is the repository's default branch.
	DefaultBranch(ctx context.Context, repo Repo) (string, error)
	// Protected reports whether the branch is protected. A branch that does not
	// exist yet is not protected.
	Protected(ctx context.Context, repo Repo, branch string) (bool, error)
	// PullRequest returns an open pull request's head, or ErrPullRequestNotFound.
	PullRequest(ctx context.Context, repo Repo, number int64) (PullRequestHead, error)
}

// PullRequestHead is the part of a pull request the policy needs.
type PullRequestHead struct {
	State string // "open" | "closed"
	// HeadRepo is the repository the head branch lives in, owner/name.
	HeadRepo string
	// HeadRef is the head branch's name.
	HeadRef string
	// BaseRepo is the repository the pull request is against, owner/name.
	BaseRepo string
}

// Request is what a git_publish node asks for.
type Request struct {
	Repo Repo
	// Branch is the branch to push to; empty when PRHeadOf is set.
	Branch string
	// PRHeadOf, when non-zero, publishes to that pull request's head branch.
	PRHeadOf int64
	// Allow are glob patterns the branch must match; empty allows any.
	Allow []string
}

// Decision is the policy's verdict. Branch is the branch to push to, set when
// Allowed.
type Decision struct {
	Allowed bool
	Branch  string
	// Reason says why a publish was refused, in words for a person.
	Reason string
	// Default is the repository's default branch, for the audit trail.
	Default string
}
