// Copyright (c) 2026 Reliant Labs

// Package ghblobs reads one git blob of a GitHub repository by its id, as a
// person: the source a coverage report covers (forge
// docs/proposals/tests-coverage-flakes.md, Amendment 1 part A).
//
// A coverage report names each covered file's git blob id, never its text.
// So the coverage page reads the bytes from GitHub at view time, and nothing
// stores them:
//
//	GET /repos/{owner}/{repo}/git/blobs/{sha}   Accept: application/vnd.github.raw
//
// forge:outbound-io
//
// Every read asks GitHub with the CALLER's own token (control-plane's
// delegated token when hosted, their saved GitHub connection when
// self-hosted), so a person reads only what GitHub would show them. A
// project-wide credential (an App installation token) was rejected: it would
// let anyone who can read the project's tests read its code.
//
// Built by internal/app's CoverageSourceBlobs helper (providers_api.go) and
// consumed as coverage_source's Blobs: its TokenSource is the hosted-vs-saved
// choice gitHubTokenSource makes, and it is absent (nil) on a hosted
// deployment without INTERNAL_SERVICE_SECRET, neither of which a Deps
// field filled by type can say.
//
//forge:provided
package ghblobs

import (
	"context"
	"errors"
	"net/http"
)

// MaxBytes is the largest blob Read returns by default: what a coverage page
// shows (GitHub's own limit for the endpoint is 100 MB).
const MaxBytes = 2 << 20

// MaxInFlight bounds the reads one Reader holds at once, so the bytes it
// holds stay under MaxInFlight * MaxBytes however many viewers ask together:
// the API server reads covered files, and must not run out of memory doing
// it. A read past the bound waits for a slot, or for its context.
const MaxInFlight = 4

// Service is the adapter's surface. *Reader implements it.
type Service interface {
	// Read returns the bytes of blob blobID in owner/repo, read as userID.
	// Errors: the token source's (not connected, needs reconnect),
	// ErrTokenRejected, ErrNotFound, ErrTooLarge, ErrInvalid; anything else
	// is GitHub being unreachable or refusing for another reason (a rate
	// limit), which a retry may fix.
	Read(ctx context.Context, userID, owner, repo, blobID string) ([]byte, error)
}

// TokenSource returns a user's current GitHub token. ghaccess's token sources
// satisfy it; an error the user must act on (not connected, needs reconnect)
// is passed through for the caller to classify.
type TokenSource interface {
	Token(ctx context.Context, userID string) (string, error)
}

// Deps are the adapter's collaborators.
type Deps struct {
	// Tokens supplies the caller's own GitHub token for each Read.
	Tokens TokenSource
	// APIBaseURL overrides GitHub's API (tests). Empty is DefaultAPIBaseURL.
	// forge:optional-dep
	APIBaseURL string
	// HTTPClient performs the calls. nil is an SSRF-guarded client with a
	// 20s timeout.
	// forge:optional-dep
	HTTPClient *http.Client
	// MaxBytes bounds one blob. 0 is MaxBytes.
	// forge:optional-dep
	MaxBytes int64
}

var (
	// ErrTokenRejected is GitHub refusing the caller's token (401): they
	// must reconnect GitHub.
	ErrTokenRejected = errors.New("ghblobs: GitHub rejected the token")
	// ErrNotFound is GitHub having no such blob, or not showing the
	// repository to the caller (404 is GitHub's answer for both).
	ErrNotFound = errors.New("ghblobs: GitHub has no such blob for this viewer")
	// ErrTooLarge is a blob over the size bound.
	ErrTooLarge = errors.New("ghblobs: the blob is larger than a coverage page shows")
	// ErrInvalid is an owner, repository or blob id that is not one GitHub
	// could name. Nothing is sent.
	ErrInvalid = errors.New("ghblobs: not a GitHub repository and blob id")
)

var _ Service = (*Reader)(nil)
