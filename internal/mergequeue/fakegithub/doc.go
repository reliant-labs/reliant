// Copyright (c) 2025 Reliant Labs

// Package fakegithub is a fake GitHub for the merge queue's tests: a
// [ghport.Client] over real bare git repositories in a test's temp dir.
//
// It is test-support code, importable from any package's tests. What makes it
// worth more than a mock is that the git underneath is real: refs, commits,
// trees and merges are git's own, a [gitport.Repo] can fetch from and push to
// a fake repository by its [Repo.URL], and a PR's head is whatever its branch
// points at, so a push through git is a push GitHub sees.
//
// It models GitHub's squash merge (PUT /pulls/{n}/merge, merge_method squash)
// exactly as the queue relies on it:
//
//   - the PR must be open, else [ghport.NotMergeableError] (closed), or
//     [ghport.AlreadyMergedError] when it was merged;
//   - `sha` must equal the PR's current head, else [ghport.HeadMismatchError]
//     (GitHub's 409);
//   - the head is 3-way merged into the base branch's current tip; a conflict
//     is [ghport.NotMergeableError] (405);
//   - ONE commit is written whose only parent is the base tip and whose tree
//     is the merge result, titled "<PR title> (#<n>)" unless the call names a
//     title; the base branch advances to it and the PR reads as merged.
//
// Deleting an open PR's head branch closes the PR, as on GitHub.
//
// Fault injection ([GitHub.Inject], [Repo.ForeignWrite],
// [Repo.OverrideSquash], [Repo.DivergeNextSquash]) lets a test reproduce a
// rate limit, a 5xx (before or after the effect), another writer moving main
// between two calls, and GitHub's merge producing a different tree than the
// queue predicted.
//
// Not modelled: branch protection and rulesets, merge methods other than
// squash, the asynchronous `mergeable` computation (reads are always
// settled), and GitHub's default squash body (the commit message is exactly
// what the call sends).
//
//forge:exclude-contract: test support — a fake GitHub for tests; its non-test source imports testing
package fakegithub
