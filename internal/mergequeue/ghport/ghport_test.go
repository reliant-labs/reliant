// Copyright (c) 2025 Reliant Labs
package ghport

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestTypedErrorsUnwrapToTheirSentinel(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err  error
		want error
	}{
		{&NotFoundError{What: "ref x"}, ErrNotFound},
		{&HeadMismatchError{Expected: "a", Actual: "b"}, ErrHeadMismatch},
		{&NotMergeableError{Reason: NotMergeableConflict}, ErrNotMergeable},
		{&AlreadyMergedError{MergeCommitSHA: "m", HeadSHA: "h"}, ErrAlreadyMerged},
		{&RateLimitError{}, ErrRateLimited},
		{&TransientError{Status: 502}, ErrTransient},
		{&ValidationError{Field: "ref"}, ErrValidation},
	} {
		wrapped := fmt.Errorf("landing: %w", c.err)
		if !errors.Is(wrapped, c.want) {
			t.Errorf("%T does not unwrap to %v", c.err, c.want)
		}
		for _, other := range []error{ErrNotFound, ErrHeadMismatch, ErrNotMergeable, ErrAlreadyMerged, ErrRateLimited, ErrTransient, ErrValidation} {
			if other != c.want && errors.Is(wrapped, other) {
				t.Errorf("%T also matches %v", c.err, other)
			}
		}
	}
}

func TestRetryable(t *testing.T) {
	t.Parallel()
	reset := time.Unix(1_900_000_000, 0)
	if retry, at := Retryable(fmt.Errorf("x: %w", &RateLimitError{Reset: reset})); !retry || !at.Equal(reset) {
		t.Fatalf("rate limit: %v %v", retry, at)
	}
	if retry, at := Retryable(&TransientError{Status: 503}); !retry || !at.IsZero() {
		t.Fatalf("5xx: %v %v", retry, at)
	}
	for _, err := range []error{&HeadMismatchError{}, &NotMergeableError{}, &AlreadyMergedError{}, ErrNotFound, errors.New("other"), nil} {
		if retry, _ := Retryable(err); retry {
			t.Errorf("Retryable(%v) = true", err)
		}
	}
}

func TestValidateRef(t *testing.T) {
	t.Parallel()
	for ref, ok := range map[string]bool{
		"refs/heads/main":             true,
		"refs/heads/reliant-queue/42": true,
		"refs/tags/v1.2.3":            true,
		"heads/main":                  false,
		"main":                        false,
		"refs/":                       false,
		"refs/heads/a..b":             false,
		"refs/heads/x/":               false,
		"refs/heads/has space":        false,
		"refs/heads/x~1":              false,
	} {
		if err := ValidateRef(ref); (err == nil) != ok {
			t.Errorf("ValidateRef(%q) = %v, want ok=%v", ref, err, ok)
		}
	}
}

func TestValidateCheckRun(t *testing.T) {
	t.Parallel()
	ok := []struct {
		s CheckStatus
		c CheckConclusion
	}{{"", ""}, {CheckQueued, ""}, {CheckInProgress, ""}, {CheckCompleted, ConclusionFailure}, {"", ConclusionSuccess}}
	for _, c := range ok {
		if err := ValidateCheckRun(c.s, c.c); err != nil {
			t.Errorf("ValidateCheckRun(%q, %q) = %v", c.s, c.c, err)
		}
	}
	bad := []struct {
		s CheckStatus
		c CheckConclusion
	}{{CheckCompleted, ""}, {CheckQueued, ConclusionSuccess}, {"running", ""}, {CheckCompleted, "great"}}
	for _, c := range bad {
		if err := ValidateCheckRun(c.s, c.c); !errors.Is(err, ErrValidation) {
			t.Errorf("ValidateCheckRun(%q, %q) = %v, want a validation error", c.s, c.c, err)
		}
	}
}

func TestParseRepo(t *testing.T) {
	t.Parallel()
	r, err := ParseRepo("reliant-labs/reliant")
	if err != nil || r != (Repo{Owner: "reliant-labs", Name: "reliant"}) || r.String() != "reliant-labs/reliant" {
		t.Fatalf("ParseRepo = %+v, %v", r, err)
	}
	for _, bad := range []string{"", "x", "/x", "x/", "a/b/c"} {
		if _, err := ParseRepo(bad); err == nil {
			t.Errorf("ParseRepo(%q) accepted", bad)
		}
	}
	if BranchRef("main") != "refs/heads/main" {
		t.Fatal(BranchRef("main"))
	}
}
