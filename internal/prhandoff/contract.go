// Copyright (c) 2025 Reliant Labs

// Package prhandoff enrolls future PRs from authenticated server receipts and
// records trusted review completions for the same original author. This slice
// is deliberately not wired into app roots: pending_wait is NOT delivery.
//
//forge:not-a-component: disabled PR handoff slice; constructed explicitly by receipt adapters/tests only
package prhandoff

import (
	"context"
	"time"
)

// Service is the narrow original-author handoff boundary. Inputs come only from
// server activities or authenticated event admission, never public PR prose.
type Service interface {
	Published(context.Context, PublishInput) error
	Created(context.Context, CreateInput) error
	Reviewed(context.Context, ReviewInput) (Review, error)
}

// Deps declares the authority and persistence seams.
type Deps struct {
	Store   Store
	Authors Authors
	GitHub  GitHub
}

// Author identifies the original root AND its exact Temporal execution.
type Author struct {
	OwnerID     string
	RootID      string
	ExecutionID string
	ChatID      string
	WorktreeID  string
	// WorktreePath is resolved from the owned worktree, never a client claim.
	WorktreePath string
}

// Site is captured from the executing Temporal activity, not an LLM argument.
// The Authors implementation must verify the root/chat/worktree ownership.
type Site struct {
	RootID      string
	ExecutionID string
	ChatID      string
}

// Authors resolves server call sites and checks whether the enrolled execution
// is still available. False means explicit human attention, never restart.
type Authors interface {
	Resolve(context.Context, Site) (Author, error)
	// Checkout resolves a registered project repository under the owned workspace.
	Checkout(context.Context, Author, Repository) (string, error)
	Available(context.Context, Author) (bool, error)
}

// Repository is a provider-returned stable identity, not merely owner/name.
type Repository struct {
	ID   int64
	Name string
}

// Pull is a provider-returned, open, same-repository PR snapshot.
type Pull struct {
	Repo       Repository
	Number     int64
	ObjectID   int64
	HeadRepoID int64
	Branch     string
	HeadSHA    string
	Open       bool
}

// Completion is a provider-fetched authorized review, with its source commit.
// GitHub.Review must authenticate the owner and authorize the actual reviewer;
// a webhook signature or a comment saying "review passed" is insufficient.
type Completion struct {
	Pull     Pull
	ReviewID string
	HeadSHA  string
	Verdict  string
}

// GitHub reads provider facts under the enrolled owner's repository-bound
// credentials. Review must authorize its producer, not trust event text.
type GitHub interface {
	Repository(context.Context, string, string) (Repository, error)
	BranchHead(context.Context, string, Repository, string) (string, error)
	Pull(context.Context, string, Repository, int64) (Pull, error)
	Review(context.Context, string, Repository, int64, string) (Completion, error)
}

// Publication is a successful git_publish receipt verified against GitHub.
type Publication struct {
	ID      string
	Author  Author
	Repo    Repository
	Branch  string
	HeadSHA string
	// PreviousHeadSHA is GitHub's branch head observed before this push. Refresh
	// uses compare-and-swap so an old receipt cannot regress a newer binding.
	PreviousHeadSHA string
	CreatedAt       time.Time
}

// Binding is immutable original ownership; only the published head can advance.
type Binding struct {
	ID            string
	Author        Author
	Pull          Pull
	PublicationID string
}

// Review is a durable, deduplicated handoff disposition. No state in this slice
// claims delivery; pending_wait needs a future race-safe Temporal subscription.
type Review struct {
	ID        string
	BindingID string
	ReviewID  string
	HeadSHA   string
	Verdict   string
	State     string
	Reason    string
}

// Store owns atomic receipt/enrollment/head-refresh and review deduplication.
// RecordPublication must never replace original ownership. Enroll must join a
// publication for the exact author, repo, branch and full SHA, or fail closed.
// RecordReview must serialize with head refresh and mark a concurrent old-head
// review stale; an existing review ID is returned unchanged, never redelivered.
type Store interface {
	RecordPublication(context.Context, Publication) error
	Enroll(context.Context, Binding) error
	Binding(context.Context, string, int64, int64) (Binding, error)
	RecordReview(context.Context, Review) (Review, error)
}

// PublishInput is supplied after a successful push. WorktreePath is verified
// against the owned worktree; it cannot enroll a different checkout.
type PublishInput struct {
	Site            Site
	Repository      string
	WorktreePath    string
	Branch          string
	HeadSHA         string
	PreviousHeadSHA string
}

// CreateInput comes from a successful authenticated PR-create response. Neither
// body nor trailers participate. Number, object ID and SHA must match GitHub.
type CreateInput struct {
	Site       Site
	Repository string
	Number     int64
	ObjectID   int64
	HeadSHA    string
}

// ReviewInput names a review to fetch, not findings or a target run to trust.
// OwnerID must come from authenticated event admission, not webhook JSON.
type ReviewInput struct {
	OwnerID      string
	RepositoryID int64
	Number       int64
	ReviewID     string
}
