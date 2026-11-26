// Copyright (c) 2025 Reliant Labs
package ghport

import (
	"errors"
	"fmt"
	"time"
)

// Sentinels. Each typed error below unwraps to exactly one of them, so
// errors.Is answers "which outcome" and errors.As recovers the detail.
var (
	ErrNotFound       = errors.New("ghport: not found")
	ErrAlreadyExists  = errors.New("ghport: already exists")
	ErrNotFastForward = errors.New("ghport: update is not a fast-forward")
	ErrHeadMismatch   = errors.New("ghport: pull request head does not match the expected sha")
	ErrNotMergeable   = errors.New("ghport: pull request is not mergeable")
	ErrAlreadyMerged  = errors.New("ghport: pull request is already merged")
	ErrRateLimited    = errors.New("ghport: rate limited")
	ErrTransient      = errors.New("ghport: transient server error")
	ErrValidation     = errors.New("ghport: invalid request")
)

// IsNotFound reports whether err is [ErrNotFound].
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// NotFoundError names what was missing.
type NotFoundError struct {
	// What is a short description: "ref refs/heads/x", "pull request #3".
	What string
}

func (e *NotFoundError) Error() string { return "ghport: " + e.What + " not found" }
func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// HeadMismatchError is GitHub's 409 on a merge: the PR's head moved since the
// caller read it. The queue never retries one; the entry goes back to review.
type HeadMismatchError struct {
	Expected string
	Actual   string
}

func (e *HeadMismatchError) Error() string {
	return fmt.Sprintf("ghport: pull request head is %s, not the expected %s", e.Actual, e.Expected)
}
func (e *HeadMismatchError) Unwrap() error { return ErrHeadMismatch }

// NotMergeableReason says why GitHub refused a merge with 405.
type NotMergeableReason string

const (
	NotMergeableClosed   NotMergeableReason = "closed"
	NotMergeableConflict NotMergeableReason = "conflict"
	// NotMergeableBlocked covers what GitHub refuses for policy (rulesets,
	// required checks, a draft). The adapter cannot always tell these apart.
	NotMergeableBlocked NotMergeableReason = "blocked"
)

// NotMergeableError is GitHub's 405 on a merge for an open-or-closed PR that
// is not merged.
type NotMergeableError struct {
	Reason  NotMergeableReason
	Message string
}

func (e *NotMergeableError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("ghport: pull request is not mergeable (%s): %s", e.Reason, e.Message)
	}
	return fmt.Sprintf("ghport: pull request is not mergeable (%s)", e.Reason)
}
func (e *NotMergeableError) Unwrap() error { return ErrNotMergeable }

// AlreadyMergedError is a merge call for a PR that is already merged. It is
// not a failure in itself: a lander retrying after a lost response gets this,
// and treats the merge as done when HeadSHA is the head it meant to land.
type AlreadyMergedError struct {
	MergeCommitSHA string
	// HeadSHA is the head the PR had when it merged.
	HeadSHA string
}

func (e *AlreadyMergedError) Error() string {
	return fmt.Sprintf("ghport: pull request already merged as %s (head %s)", e.MergeCommitSHA, e.HeadSHA)
}
func (e *AlreadyMergedError) Unwrap() error { return ErrAlreadyMerged }

// RateLimitError is a primary or secondary rate limit (403/429).
type RateLimitError struct {
	// Reset is when GitHub said calls may resume: the X-RateLimit-Reset or
	// Retry-After time. Zero when GitHub did not say.
	Reset     time.Time
	Secondary bool
}

func (e *RateLimitError) Error() string {
	kind := "primary"
	if e.Secondary {
		kind = "secondary"
	}
	if e.Reset.IsZero() {
		return fmt.Sprintf("ghport: %s rate limit", kind)
	}
	return fmt.Sprintf("ghport: %s rate limit until %s", kind, e.Reset.UTC().Format(time.RFC3339))
}
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// TransientError is a 5xx (or a connection failure the adapter maps to one).
// The request may or may not have taken effect, which is why every write the
// queue makes is idempotent on retry.
type TransientError struct {
	Status  int
	Message string
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("ghport: transient server error %d: %s", e.Status, e.Message)
}
func (e *TransientError) Unwrap() error { return ErrTransient }

// ValidationError is a request GitHub would reject with 422 for its shape.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("ghport: invalid %s: %s", e.Field, e.Reason)
}
func (e *ValidationError) Unwrap() error { return ErrValidation }
