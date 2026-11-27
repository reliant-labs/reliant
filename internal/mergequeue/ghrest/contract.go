// Copyright (c) 2025 Reliant Labs

// Package ghrest is the merge queue's port onto GitHub's REST API: the
// ghport.Client the queue lands with.
//
// forge:outbound-io
//
// It speaks plain HTTP+JSON to api.github.com with a token it asks for per
// call and per repository, and maps every response onto ghport's typed errors,
// so the queue never sees an HTTP status:
//
//   - 404 is [ghport.ErrNotFound];
//   - a merge's 409 is [ghport.HeadMismatchError] (the actual head re-read);
//   - a merge's 405 re-reads the PR: merged is [ghport.AlreadyMergedError],
//     closed or conflicting or blocked is [ghport.NotMergeableError];
//   - 403/429 with the rate-limit headers (or GitHub's secondary-limit text)
//     is [ghport.RateLimitError], with the reset time GitHub gave;
//   - 5xx and transport failures are [ghport.TransientError];
//   - a ref create's 422 "already exists" is [ghport.ErrAlreadyExists], a ref
//     update's 422 "not a fast forward" is [ghport.ErrNotFastForward], any
//     other 422 a [ghport.ValidationError];
//   - anything else (401, a 403 that is not a rate limit) is an [*APIError].
//
// No response body is ever echoed into an error beyond GitHub's short
// `message` field, and the token never is.
//
// Built by the worker for the merge queue (queuetoken.Clients), per
// repository, with that repository's token source; not a node compose.go
// constructs from Deps.
//
//forge:provided
package ghrest

import (
	"context"
	"net/http"

	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
)

// Service is ghport.Client: everything the merge queue asks of GitHub. See
// ghport for each method's contract and errors.
type Service interface {
	GetRef(ctx context.Context, repo ghport.Repo, ref string) (ghport.Ref, error)
	CreateRef(ctx context.Context, repo ghport.Repo, ref, sha string) error
	UpdateRef(ctx context.Context, repo ghport.Repo, ref, sha string, force bool) error
	DeleteRef(ctx context.Context, repo ghport.Repo, ref string) error

	GetPullRequest(ctx context.Context, repo ghport.Repo, number int) (ghport.PullRequest, error)
	MergePullRequest(ctx context.Context, repo ghport.Repo, number int, req ghport.MergeRequest) (ghport.MergeResult, error)

	CreateCheckRun(ctx context.Context, repo ghport.Repo, in ghport.CreateCheckRun) (ghport.CheckRun, error)
	UpdateCheckRun(ctx context.Context, repo ghport.Repo, id int64, in ghport.UpdateCheckRun) (ghport.CheckRun, error)
	ListCheckRuns(ctx context.Context, repo ghport.Repo, sha string, opts ghport.ListCheckRuns) ([]ghport.CheckRun, error)

	GetContents(ctx context.Context, repo ghport.Repo, path, ref string) ([]byte, error)
	GetCommit(ctx context.Context, repo ghport.Repo, sha string) (ghport.Commit, error)
	CreateCommit(ctx context.Context, repo ghport.Repo, in ghport.NewCommit) (ghport.Commit, error)

	CreateIssueComment(ctx context.Context, repo ghport.Repo, number int, body string) (ghport.IssueComment, error)
	ListIssueComments(ctx context.Context, repo ghport.Repo, number int) ([]ghport.IssueComment, error)
}

// TokenFunc returns the token to call GitHub with for one repository. It is
// asked on every call, so a source that caches and renews (an installation
// token lives an hour) stays the one place that knows when it expires.
type TokenFunc func(ctx context.Context, repo ghport.Repo) (string, error)

// Deps are the adapter's collaborators.
type Deps struct {
	// Token authenticates every call.
	Token TokenFunc
	// HTTPClient performs the calls. nil uses a traced client with a 30s
	// timeout.
	//
	// forge:optional-dep
	HTTPClient *http.Client
}
