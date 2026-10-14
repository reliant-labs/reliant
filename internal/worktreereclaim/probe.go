// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"
)

const (
	prLookupTimeout = 15 * time.Second
	fetchTimeout    = 30 * time.Second
)

// GitHubPRMerged reports whether branch has a merged pull request whose head
// commit is exactly head. Matching the commit, not just the branch name, is
// what keeps it safe: a branch that got more commits after its PR merged, or a
// name reused by a later PR, does not match, so the new work is still held.
// Any failure (no gh, no login, no network, not a GitHub remote) is false: the
// worktree is then judged on what git alone can show.
func GitHubPRMerged(ctx context.Context, checkout, branch, head string) bool {
	ctx, cancel := context.WithTimeout(ctx, prLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--state", "merged", "--head", branch, "--limit", "20", "--json", "headRefOid")
	cmd.Dir = checkout
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	var prs []struct {
		HeadRefOid string `json:"headRefOid"`
	}
	if json.Unmarshal(out, &prs) != nil {
		return false
	}
	for _, pr := range prs {
		if pr.HeadRefOid == head {
			return true
		}
	}
	return false
}

// FetchBaseBranch refreshes refs/remotes/origin/<base> so a merge made since
// this clone last fetched is visible. It only adds objects and moves that one
// remote-tracking ref; it never touches a branch or a working tree. Failure is
// ignored: the worktree is then judged on the refs it already has.
func FetchBaseBranch(ctx context.Context, checkout, base string) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	_, _ = git(ctx, checkout, nil, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "origin",
		"+refs/heads/"+base+":refs/remotes/origin/"+base)
}
