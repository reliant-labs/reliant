// Copyright (c) 2025 Reliant Labs

// Package ghport is the merge queue's port onto GitHub: the handful of REST
// operations the queue needs to build, gate and land a batch, in
// vendor-neutral types.
//
// The queue (internal/mergequeue) depends on [Client] only. An adapter over the
// GitHub REST API implements it with the Queue App's installation token; the
// fakegithub package implements it over a real bare git repository for tests.
// No go-github type crosses this boundary, so a caller never learns which one
// it holds.
//
// Errors are part of the contract. Every adapter maps GitHub's responses onto
// the typed errors in errors.go, and callers branch on them with errors.Is and
// errors.As:
//
//   - [ErrHeadMismatch] ([*HeadMismatchError]): GitHub's 409, the PR's head is
//     not the sha the merge named. Never retried: the head moved, so the
//     reviewed and gated tree is no longer what would land.
//   - [ErrNotMergeable] ([*NotMergeableError]): GitHub's 405, the PR is closed
//     or conflicts with its base.
//   - [ErrAlreadyMerged] ([*AlreadyMergedError]): the PR was merged before this
//     call. A lander retrying a merge whose response it lost treats this as
//     success, after checking the merged head and merge commit it carries.
//   - [ErrRateLimited] ([*RateLimitError]) and [ErrTransient]
//     ([*TransientError]): retry, at or after [Retryable]'s time.
//   - [ErrNotFound], [ErrAlreadyExists], [ErrNotFastForward]: ref and object
//     outcomes the caller usually decides on (a delete of a missing branch is
//     done; a create of an existing one is not).
package ghport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Repo names a GitHub repository.
type Repo struct {
	Owner string
	Name  string
}

// String returns "owner/name".
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// ParseRepo parses "owner/name".
func ParseRepo(s string) (Repo, error) {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Repo{}, fmt.Errorf("ghport: repository %q is not owner/name", s)
	}
	return Repo{Owner: owner, Name: name}, nil
}

// BranchRef returns the fully qualified ref of a branch: "refs/heads/<branch>".
func BranchRef(branch string) string { return "refs/heads/" + branch }

// Ref is a git reference and the object it points at.
type Ref struct {
	// Name is fully qualified: "refs/heads/main".
	Name string
	SHA  string
}

// PRState is a pull request's open/closed state. A merged PR is closed, with
// [PullRequest.Merged] set.
type PRState string

const (
	PRStateOpen   PRState = "open"
	PRStateClosed PRState = "closed"
)

// Mergeability is GitHub's view of whether a PR's head merges cleanly into its
// base. GitHub computes it asynchronously, so a fresh read may be unknown.
type Mergeability string

const (
	MergeableUnknown     Mergeability = "unknown"
	MergeableClean       Mergeability = "clean"
	MergeableConflicting Mergeability = "conflicting"
)

// PullRequest is the subset of a pull request the queue reads.
type PullRequest struct {
	Number int
	Title  string
	State  PRState
	// HeadSHA is the PR's current head commit.
	HeadSHA string
	// HeadRef is the head branch's name ("feature/x"), not a full ref.
	HeadRef string
	// BaseRef is the base branch's name ("main"), not a full ref.
	BaseRef   string
	Mergeable Mergeability
	Merged    bool
	// MergeCommitSHA is set when Merged: for a squash merge, the one commit
	// GitHub wrote onto the base.
	MergeCommitSHA string
}

// MergeMethod is how GitHub lands a PR. The queue uses squash only (owner
// decision D1); the others exist so an adapter can say what it refuses.
type MergeMethod string

const (
	MergeSquash MergeMethod = "squash"
	MergeMerge  MergeMethod = "merge"
	MergeRebase MergeMethod = "rebase"
)

// MergeRequest is a call to land a PR: PUT /repos/{o}/{r}/pulls/{n}/merge.
type MergeRequest struct {
	Method MergeMethod
	// ExpectedHeadSHA binds the merge to the head that was reviewed and gated
	// (GitHub's `sha` parameter). Empty means "whatever the head is", which
	// the queue never sends.
	ExpectedHeadSHA string
	// CommitTitle defaults, on GitHub, to "<PR title> (#<n>)".
	CommitTitle   string
	CommitMessage string
}

// MergeResult is a landed merge.
type MergeResult struct {
	// SHA is the merge commit written onto the base branch.
	SHA string
}

// CheckStatus is a check run's lifecycle state.
type CheckStatus string

const (
	CheckQueued     CheckStatus = "queued"
	CheckInProgress CheckStatus = "in_progress"
	CheckCompleted  CheckStatus = "completed"
)

// CheckConclusion is a completed check run's result.
type CheckConclusion string

const (
	ConclusionSuccess        CheckConclusion = "success"
	ConclusionFailure        CheckConclusion = "failure"
	ConclusionNeutral        CheckConclusion = "neutral"
	ConclusionCancelled      CheckConclusion = "cancelled"
	ConclusionSkipped        CheckConclusion = "skipped"
	ConclusionTimedOut       CheckConclusion = "timed_out"
	ConclusionActionRequired CheckConclusion = "action_required"
)

// CheckOutput is a check run's rendered text.
type CheckOutput struct {
	Title   string
	Summary string
	Text    string
}

// CheckRun is one check run on a commit.
type CheckRun struct {
	ID         int64
	Name       string
	HeadSHA    string
	Status     CheckStatus
	Conclusion CheckConclusion
	Output     CheckOutput
	ExternalID string
	DetailsURL string
}

// CreateCheckRun is a new check run on HeadSHA. Status defaults to queued;
// a Conclusion requires (and implies) completed.
type CreateCheckRun struct {
	Name       string
	HeadSHA    string
	Status     CheckStatus
	Conclusion CheckConclusion
	Output     CheckOutput
	ExternalID string
	DetailsURL string
}

// UpdateCheckRun changes an existing check run. Zero fields are left as they
// are; Output replaces the whole output when non-nil.
type UpdateCheckRun struct {
	Status     CheckStatus
	Conclusion CheckConclusion
	Output     *CheckOutput
	DetailsURL string
}

// ListCheckRuns narrows a listing. A zero value lists every check run on the
// sha.
type ListCheckRuns struct {
	// Name keeps only runs with this exact name.
	Name string
}

// Commit is a commit's identity and shape.
type Commit struct {
	SHA     string
	TreeSHA string
	// Parents are in order; a squash commit has exactly one.
	Parents []string
	Message string
}

// NewCommit is a commit object to write: POST /repos/{o}/{r}/git/commits.
// The lander writes one per revert, whose tree is a tree the gate already
// passed.
type NewCommit struct {
	Message string
	TreeSHA string
	Parents []string
}

// IssueComment is one comment in a PR's conversation.
type IssueComment struct {
	ID   int64
	Body string
}

// Client is everything the merge queue asks of GitHub. Every method takes the
// context first and the repository second; refs are fully qualified
// ("refs/heads/main"). An implementation is safe for concurrent use.
type Client interface {
	// GetRef reads a ref. A missing ref is [ErrNotFound].
	GetRef(ctx context.Context, repo Repo, ref string) (Ref, error)
	// CreateRef creates a ref at sha. An existing ref is [ErrAlreadyExists].
	CreateRef(ctx context.Context, repo Repo, ref, sha string) error
	// UpdateRef moves an existing ref to sha. Without force, a move that is
	// not a fast-forward is [ErrNotFastForward]. A missing ref is
	// [ErrNotFound].
	UpdateRef(ctx context.Context, repo Repo, ref, sha string, force bool) error
	// DeleteRef deletes a ref. A missing ref is [ErrNotFound]; see
	// [DeleteBranch] for the idempotent form.
	DeleteRef(ctx context.Context, repo Repo, ref string) error

	// GetPullRequest reads a PR. A missing PR is [ErrNotFound].
	GetPullRequest(ctx context.Context, repo Repo, number int) (PullRequest, error)
	// MergePullRequest lands a PR. See the package doc for its errors: a
	// head that moved, a PR that cannot merge, and a PR already merged are
	// each distinct.
	MergePullRequest(ctx context.Context, repo Repo, number int, req MergeRequest) (MergeResult, error)

	// CreateCheckRun creates a check run and returns it with its ID.
	CreateCheckRun(ctx context.Context, repo Repo, in CreateCheckRun) (CheckRun, error)
	// UpdateCheckRun changes a check run. A missing ID is [ErrNotFound].
	UpdateCheckRun(ctx context.Context, repo Repo, id int64, in UpdateCheckRun) (CheckRun, error)
	// ListCheckRuns lists the check runs on sha, newest first.
	ListCheckRuns(ctx context.Context, repo Repo, sha string, opts ListCheckRuns) ([]CheckRun, error)

	// GetCommit reads a commit. A missing commit is [ErrNotFound].
	GetCommit(ctx context.Context, repo Repo, sha string) (Commit, error)
	// CreateCommit writes a commit object (no ref moves). A tree or parent
	// that does not exist is a [ValidationError].
	CreateCommit(ctx context.Context, repo Repo, in NewCommit) (Commit, error)

	// CreateIssueComment comments on a PR's conversation.
	CreateIssueComment(ctx context.Context, repo Repo, number int, body string) (IssueComment, error)
	// ListIssueComments lists a PR's conversation comments, oldest first. A
	// missing PR is [ErrNotFound].
	ListIssueComments(ctx context.Context, repo Repo, number int) ([]IssueComment, error)
}

// DeleteBranch deletes a branch and treats a branch that is already gone as
// done, which is what landing wants after a merge (the `--delete-branch` half
// of `gh pr merge --squash --delete-branch --match-head-commit`).
func DeleteBranch(ctx context.Context, c Client, repo Repo, branch string) error {
	err := c.DeleteRef(ctx, repo, BranchRef(branch))
	if err == nil || IsNotFound(err) {
		return nil
	}
	return err
}

// ValidateRef reports whether ref is a fully qualified ref name the port
// accepts.
func ValidateRef(ref string) error {
	if !strings.HasPrefix(ref, "refs/") || len(ref) == len("refs/") {
		return &ValidationError{Field: "ref", Reason: fmt.Sprintf("%q is not a fully qualified ref (refs/...)", ref)}
	}
	if strings.Contains(ref, "..") || strings.HasSuffix(ref, "/") || strings.ContainsAny(ref, " ~^:?*[\\") {
		return &ValidationError{Field: "ref", Reason: fmt.Sprintf("%q is not a valid ref name", ref)}
	}
	return nil
}

// ValidateCheckRun checks the status/conclusion pairing GitHub enforces: a
// conclusion is set exactly when the run is completed.
func ValidateCheckRun(status CheckStatus, conclusion CheckConclusion) error {
	switch status {
	case "", CheckQueued, CheckInProgress, CheckCompleted:
	default:
		return &ValidationError{Field: "status", Reason: fmt.Sprintf("unknown check status %q", status)}
	}
	switch conclusion {
	case "", ConclusionSuccess, ConclusionFailure, ConclusionNeutral, ConclusionCancelled,
		ConclusionSkipped, ConclusionTimedOut, ConclusionActionRequired:
	default:
		return &ValidationError{Field: "conclusion", Reason: fmt.Sprintf("unknown check conclusion %q", conclusion)}
	}
	if status == CheckCompleted && conclusion == "" {
		return &ValidationError{Field: "conclusion", Reason: "a completed check run needs a conclusion"}
	}
	if conclusion != "" && status != "" && status != CheckCompleted {
		return &ValidationError{Field: "status", Reason: fmt.Sprintf("a check run with conclusion %q must be completed, not %q", conclusion, status)}
	}
	return nil
}

// Retryable reports whether err is worth retrying, and the earliest time to
// retry when GitHub said (a rate limit's reset). A zero time means "back off
// as you see fit". Every other error, a head mismatch above all, is final.
func Retryable(err error) (retry bool, notBefore time.Time) {
	var rl *RateLimitError
	if errors.As(err, &rl) {
		return true, rl.Reset
	}
	if errors.Is(err, ErrTransient) {
		return true, time.Time{}
	}
	return false, time.Time{}
}
