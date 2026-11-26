// Copyright (c) 2025 Reliant Labs
package gitport

import "context"

// Port is the local git the queue drives. [*Repo] implements it over the git
// binary; the candidate builder (Q2) and the reverter (Q6) take it as a
// dependency.
//
//forge:contract
type Port interface {
	// Dir is the repository's git directory.
	Dir() string
	// Fetch fetches refspecs from url into the repository.
	Fetch(ctx context.Context, url string, refspecs ...string) error
	// ResolveCommit resolves rev to a commit sha.
	ResolveCommit(ctx context.Context, rev string) (string, error)
	// TreeOf returns the tree sha of a commit.
	TreeOf(ctx context.Context, rev string) (string, error)
	// Squash writes one commit onto Base whose tree is Head merged into
	// Base. A conflict is a [*ConflictError].
	Squash(ctx context.Context, in SquashInput) (Result, error)
	// Revert writes the exact revert of a commit onto Onto. A conflict is a
	// [*ConflictError].
	Revert(ctx context.Context, in RevertInput) (Result, error)
	// Push pushes a commit to a ref of url.
	Push(ctx context.Context, url string, in PushInput) error
}
