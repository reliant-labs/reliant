// Copyright (c) 2026 Reliant Labs

// Package ghcontents reads one file of a GitHub repository at a commit, as
// the Reliant GitHub App, with no checkout: what the PR pipeline's projector
// reads a repository's .reliant/pipeline.yaml from its base branch with
// (pipeline build plan task M3).
//
//	GET /repos/{owner}/{repo}/contents/{path}?ref={sha}   Accept: application/vnd.github.raw
//
// forge:outbound-io
//
// Each read asks control-plane for an installation token scoped to the one
// repository with contents:read, minted for the user the read acts for (the
// repository's enroller), whose access to the repository gates the mint.
// Nothing is cached: one read is one mint and one GET, and the projector
// reads only when a push to the base branch may have changed the file.
//
// Built by internal/app's NewTriggerDeliveryRelay, only on a worker with a
// control plane and pipeline.enabled on: a deployment with no control plane
// has no App to read as, which the projector records as the config error.
//
//forge:provided
package ghcontents

import (
	"context"
	"errors"
	"net/http"

	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
)

// MaxBytes is the largest file Read returns by default. A pipeline.yaml is a
// few kilobytes.
const MaxBytes = 1 << 20

// Request names one file at one commit.
type Request struct {
	// UserID is the Reliant user the read acts for: their access to the
	// repository gates the token. OrgID, when set, mints through that org's
	// linked installation only.
	UserID, OrgID string
	Owner, Repo   string
	// Path is repository-relative, with no leading slash.
	Path string
	// Ref is a commit sha (or a branch or tag).
	Ref string
}

// Service is the adapter's surface. *Reader implements it.
type Service interface {
	// Read returns the bytes of req.Path at req.Ref. Errors: ErrNotFound,
	// ErrTooLarge, ErrInvalid, the token mint's (gitcredentialclient's typed
	// refusals); anything else is GitHub being unreachable or refusing for
	// another reason (a rate limit), which a retry may fix.
	Read(ctx context.Context, req Request) ([]byte, error)
}

// Tokens mints installation tokens. gitcredentialclient.Service satisfies
// it.
type Tokens interface {
	MintInstallationToken(ctx context.Context, req gitcredentialclient.InstallationRequest) (gitcredentialclient.InstallationToken, error)
}

// Deps are the adapter's collaborators.
type Deps struct {
	// Tokens mints the App's installation token for each Read.
	Tokens Tokens
	// APIBaseURL overrides GitHub's API (tests). Empty is DefaultAPIBaseURL.
	// forge:optional-dep
	APIBaseURL string
	// HTTPClient performs the calls. nil is an SSRF-guarded client with a
	// 20s timeout.
	// forge:optional-dep
	HTTPClient *http.Client
	// MaxBytes bounds one file. 0 is MaxBytes.
	// forge:optional-dep
	MaxBytes int64
}

var (
	// ErrNotFound is GitHub having no such file at the ref (or not showing
	// the repository to the App: 404 is GitHub's answer for both).
	ErrNotFound = errors.New("ghcontents: no such file at that ref")
	// ErrTooLarge is a file over the size bound.
	ErrTooLarge = errors.New("ghcontents: the file is larger than the bound")
	// ErrInvalid is an owner, repository, path or ref GitHub could not name.
	// Nothing is sent.
	ErrInvalid = errors.New("ghcontents: not a GitHub repository file and ref")
)

var _ Service = (*Reader)(nil)
