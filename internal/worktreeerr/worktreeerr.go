// Copyright (c) 2025 Reliant Labs

// Package worktreeerr names why a daemon refused to make a worktree checkout,
// in a form that survives the trip back to the server.
//
// The daemon is the only side that ran git, so it is the only side that can
// tell the caller's input — a base branch that does not exist, a branch name
// git refuses — from a real failure: git crashed, the disk is full, the
// repository is broken. It says which with a Reason on the worktree.create
// response, and the server (internal/workspacecreate) maps the Reason onto a
// typed error whose class keeps it from becoming an issue.
//
// The server never reads git's output to decide. That text changes between
// git versions ("is already checked out at" became "is already used by
// worktree at" in 2.42) and is translated under a non-English locale, so a
// match on it silently stops matching and every user mistake becomes a
// server error again.
//
// The empty Reason means the daemon recognized nothing the caller could fix:
// a server fault.
//
//forge:exclude-contract: wire vocabulary shared by the daemon and the server; constants only, no I/O
package worktreeerr

// Reason is why `git worktree add` refused a create, as the daemon diagnosed
// it from the repository, not from git's output.
type Reason string

const (
	// BaseNotFound: the base branch the caller named resolves to no commit
	// in the repository. The response's detail is the base branch.
	BaseNotFound Reason = "base_not_found"
	// InvalidBranchName: git refuses the new branch's name. The detail is
	// the branch.
	InvalidBranchName Reason = "invalid_branch_name"
	// BranchNameConflict: the branch cannot exist beside an existing one,
	// because one name is a directory of the other (`release` and
	// `release/x`; git stores refs as files). The detail is the existing
	// branch.
	BranchNameConflict Reason = "branch_name_conflict"
	// BranchCheckedOut: the branch is checked out in another worktree, and
	// git refuses to check one branch out twice. The detail is that
	// worktree's path.
	BranchCheckedOut Reason = "branch_checked_out"
)
