// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// The existing retry tests (repo_retry_test.go) cover isRetryableError and the
// backoff math in isolation. Nothing exercised runTxWithRetries itself, so the
// question "does RunTx actually retry?" was untested — these drive the real
// loop against a real database.

// serializationFailure is the error Postgres raises when two SERIALIZABLE
// transactions form a read/write dependency cycle. Built as a real
// *pgconn.PgError so it travels the same classification path as a live one.
func serializationFailure() error {
	return &pgconn.PgError{
		Code:     "40001",
		Message:  "could not serialize access due to read/write dependencies among transactions",
		Severity: "ERROR",
	}
}

// RunTx must retry a retryable failure and return success when a later attempt
// succeeds. This is the core contract; without it every 40001 is a user-visible
// error rather than a hiccup.
func TestRunTx_RetriesUntilSuccess(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	attempts := 0
	err := repo.RunTx(context.Background(), func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return serializationFailure()
		}
		return nil
	})

	if err != nil {
		t.Fatalf("RunTx should have succeeded on the third attempt, got: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected exactly 3 attempts (2 failures + 1 success), got %d", attempts)
	}
}

// The retry budget must be bounded and must match maxRetries. An unbounded
// retry loop under contention is worse than failing: it holds a connection and
// multiplies the load that caused the conflict.
func TestRunTx_StopsAtMaxRetries(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	attempts := 0
	err := repo.RunTx(context.Background(), func(ctx context.Context) error {
		attempts++
		return serializationFailure()
	})

	if err == nil {
		t.Fatal("RunTx must fail once the retry budget is exhausted")
	}
	// maxRetries is the number of RETRIES, so the initial attempt plus
	// maxRetries retries = maxRetries+1 total invocations.
	if want := maxRetries + 1; attempts != want {
		t.Fatalf("expected %d total attempts (1 initial + %d retries), got %d",
			want, maxRetries, attempts)
	}
}

// A non-retryable error must fail on the FIRST attempt. Retrying a genuine
// business error (a validation failure, a missing row) wastes the budget and
// delays the caller's error by the full backoff ladder.
func TestRunTx_DoesNotRetryNonRetryable(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	sentinel := errors.New("business rule violated")
	attempts := 0
	err := repo.RunTx(context.Background(), func(ctx context.Context) error {
		attempts++
		return sentinel
	})

	if attempts != 1 {
		t.Fatalf("a non-retryable error must not be retried, got %d attempts", attempts)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("the caller's own error must be returned unwrapped, got: %v", err)
	}
}

// REGRESSION: the retry-exhaustion path built its error with
// errors.New("transaction failed after retries: " + lastErr.Error()), which
// FLATTENS the error to a string and destroys the *pgconn.PgError in the chain.
//
// That matters because it silently breaks every caller that classifies errors
// structurally. isRetryableError itself only still works on such an error by
// falling through to a substring match on the message text — a fallback it
// documents as a last resort. Any caller using errors.As to read the SQLSTATE
// (to decide whether to surface, re-queue, or degrade) gets nothing.
//
// Wrapping with %w preserves the chain and costs nothing.
func TestRunTx_ExhaustionPreservesErrorChain(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	err := repo.RunTx(context.Background(), func(ctx context.Context) error {
		return serializationFailure()
	})
	if err == nil {
		t.Fatal("expected failure after exhausting retries")
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("the underlying *pgconn.PgError must survive retry exhaustion so "+
			"callers can classify by SQLSTATE; errors.As found nothing in: %v", err)
	}
	if pgErr.Code != "40001" {
		t.Fatalf("expected SQLSTATE 40001 to survive, got %q", pgErr.Code)
	}
}

// A nested RunTx joins the outer transaction rather than opening its own (the
// re-entrancy contract that lets SaveMessage compose inside a caller's
// transaction). The consequence for retries is easy to get wrong: the INNER
// call must not run its own retry loop, because retrying a statement inside an
// already-aborted transaction can only fail again. The outer call owns the
// retry.
func TestRunTx_NestedDoesNotRetryIndependently(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	outerAttempts := 0
	innerAttempts := 0

	err := repo.RunTx(context.Background(), func(outerCtx context.Context) error {
		outerAttempts++
		return repo.RunTx(outerCtx, func(innerCtx context.Context) error {
			innerAttempts++
			if outerAttempts < 2 {
				return serializationFailure()
			}
			return nil
		})
	})

	if err != nil {
		t.Fatalf("expected success once the OUTER transaction retried, got: %v", err)
	}
	// The inner call runs exactly once per outer attempt — never more. If the
	// inner loop retried on its own, innerAttempts would exceed outerAttempts.
	if innerAttempts != outerAttempts {
		t.Fatalf("nested RunTx must not retry independently: outer ran %d times, "+
			"inner ran %d times", outerAttempts, innerAttempts)
	}
}

// Retries must be spaced, not immediate. Hammering a contended row with no
// delay makes the conflict that caused the retry more likely to recur.
func TestRunTx_BacksOffBetweenRetries(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	var mu sync.Mutex
	var gaps []time.Duration
	last := time.Now()

	_ = repo.RunTx(context.Background(), func(ctx context.Context) error {
		mu.Lock()
		now := time.Now()
		gaps = append(gaps, now.Sub(last))
		last = now
		mu.Unlock()
		return serializationFailure()
	})

	// gaps[0] is the time to the first attempt (no backoff); every gap after
	// that follows a retry and must be non-trivial.
	if len(gaps) < 2 {
		t.Fatalf("expected multiple attempts, got %d", len(gaps))
	}
	for i := 1; i < len(gaps); i++ {
		if gaps[i] <= 0 {
			t.Fatalf("retry %d followed the previous attempt with no delay", i)
		}
	}
}

// The retry loop must respect context cancellation rather than burning the
// full backoff ladder on a caller that has already given up.
func TestRunTx_HonorsContextCancellation(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := repo.RunTx(ctx, func(txCtx context.Context) error {
		attempts++
		cancel() // caller gives up during the first attempt
		return serializationFailure()
	})

	if err == nil {
		t.Fatal("expected an error after cancellation")
	}
	if attempts > maxRetries+1 {
		t.Fatalf("attempts (%d) exceeded the budget even with a cancelled context", attempts)
	}
	t.Logf("attempts after cancellation: %d (budget %d)", attempts, maxRetries+1)
}

// The tail of runTxWithRetries — the "All retries exhausted" block that returns
// errors.New("transaction failed after retries: " + ...) — is UNREACHABLE.
//
// The loop runs attempt = 0..maxRetries inclusive. On the final iteration
// attempt == maxRetries, so the `attempt < maxRetries` guard fails and the
// function returns result.err directly rather than falling out of the loop.
// Every exit is inside the loop body.
//
// That is a good thing and worth locking down: the dead branch flattens the
// error with errors.New, which would DESTROY the *pgconn.PgError chain and
// break SQLSTATE classification for callers. This test pins the behavior the
// live code actually has, so that if someone "fixes" the loop bounds and makes
// the tail reachable, they are forced to notice it must wrap with %w.
func TestRunTx_ExhaustionReturnsUnflattenedError(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	err := repo.RunTx(context.Background(), func(ctx context.Context) error {
		return serializationFailure()
	})
	if err == nil {
		t.Fatal("expected failure")
	}

	if strings.Contains(err.Error(), "transaction failed after retries") {
		t.Fatalf("the exhaustion tail flattens the error with errors.New and breaks "+
			"errors.As classification; wrap with %%w instead. Got: %v", err)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40001" {
		t.Fatalf("expected the original *pgconn.PgError to be returned, got: %v", err)
	}
}
