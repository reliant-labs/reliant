// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
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

// recordRetryBackoff makes repo's RunTx record each backoff it would sleep
// instead of sleeping it, and returns the recording. Exhausting the retry
// budget is otherwise ~2.5s of real sleep per test, spent proving nothing the
// recording does not. RunTx retries sequentially, so no lock is needed.
func recordRetryBackoff(repo *Repo) *[]time.Duration {
	var backoffs []time.Duration
	repo.retrySleep = func(d time.Duration) { backoffs = append(backoffs, d) }
	return &backoffs
}

// RunTx must retry a retryable failure and return success when a later attempt
// succeeds. This is the core contract; without it every 40001 is a user-visible
// error rather than a hiccup.
//
// This test keeps the REAL sleep (two short backoffs, ~150ms), so the
// production time.Sleep path is exercised somewhere.
func TestRunTx_RetriesUntilSuccess(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	attempts := 0
	start := time.Now()
	err := repo.RunTx(context.Background(), orm.Serializable, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return serializationFailure()
		}
		return nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunTx should have succeeded on the third attempt, got: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected exactly 3 attempts (2 failures + 1 success), got %d", attempts)
	}
	// A lower bound only: load can stretch a sleep but never shorten it. Two
	// backoffs of at least 3/4 of baseRetryDelay and 2*baseRetryDelay.
	if minSlept := 3 * (baseRetryDelay + 2*baseRetryDelay) / 4; elapsed < minSlept {
		t.Fatalf("two retries took %v, less than the %v their backoff sleeps alone take", elapsed, minSlept)
	}
}

// A transaction whose caller's context has ended is a cancellation, not a
// database failure: RunTx stops at once instead of spending its retry budget
// (each attempt would fail the same way), and returns the context's error so
// the caller can tell what happened. In prod (2026-10-23) every starved
// activity logged "Failed to begin transaction" at ERROR while Postgres idled.
func TestRunTx_StopsWhenTheCallersContextEnds(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	backoffs := recordRetryBackoff(repo)

	t.Run("already expired: BEGIN fails, nothing runs", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		ran := false
		err := repo.RunTx(ctx, orm.Serializable, func(context.Context) error { ran = true; return nil })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RunTx on an expired context = %v, want context.DeadlineExceeded", err)
		}
		if ran {
			t.Fatal("the transaction body ran on an expired context")
		}
	})

	t.Run("ends mid-transaction: one attempt, no retries", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		attempts := 0
		err := repo.RunTx(ctx, orm.Serializable, func(context.Context) error {
			attempts++
			cancel()
			// A retryable error, so only the ended context stops the ladder.
			return serializationFailure()
		})
		if err == nil {
			t.Fatal("RunTx must fail")
		}
		if attempts != 1 {
			t.Fatalf("a transaction whose context ended was attempted %d times, want 1", attempts)
		}
	})
	if len(*backoffs) != 0 {
		t.Fatalf("RunTx backed off %d times for a context that had ended", len(*backoffs))
	}
}

// The retry budget must be bounded and must match orm.Serializable's — the
// same maxRetries+1 attempts RunTx ran before it took a level. An unbounded
// retry loop under contention is worse than failing: it holds a connection and
// multiplies the load that caused the conflict.
func TestRunTx_StopsAtMaxRetries(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	recordRetryBackoff(repo)

	attempts := 0
	err := repo.RunTx(context.Background(), orm.Serializable, func(ctx context.Context) error {
		attempts++
		return serializationFailure()
	})

	if err == nil {
		t.Fatal("RunTx must fail once the retry budget is exhausted")
	}
	// maxRetries is the number of RETRIES, so the initial attempt plus
	// maxRetries retries = maxRetries+1 total invocations: the budget
	// orm.Serializable carries.
	if maxRetries+1 != orm.DefaultTxMaxAttempts {
		t.Fatalf("orm.Serializable carries %d attempts, but this Repository sized its ladder at %d", orm.DefaultTxMaxAttempts, maxRetries+1)
	}
	if want := maxRetries + 1; attempts != want {
		t.Fatalf("expected %d total attempts (1 initial + %d retries), got %d",
			want, maxRetries, attempts)
	}
}

// The policy's attempt budget bounds the ladder at every level, and the
// ladder decides what spends it: a unique violation (23505) is retried at
// READ COMMITTED when the caller asked for attempts, and one attempt returns
// the raw conflict verbatim — what a contention measurement needs.
func TestRunTx_BudgetFollowsThePolicy(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	recordRetryBackoff(repo)

	uniqueViolation := func() error {
		return &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint", Severity: "ERROR"}
	}
	for _, tc := range []struct {
		name   string
		policy orm.TxPolicy
		fail   func() error
		want   int
	}{
		{"serializable", orm.Serializable, serializationFailure, orm.DefaultTxMaxAttempts},
		{"serializable, one attempt", orm.Serializable.WithMaxAttempts(1), serializationFailure, 1},
		{"read committed", orm.ReadCommitted, serializationFailure, 1},
		{"read committed, 3 attempts", orm.ReadCommitted.WithMaxAttempts(3), uniqueViolation, 3},
		{"read committed, the full budget", orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), uniqueViolation, orm.DefaultTxMaxAttempts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			var last error
			err := repo.RunTx(context.Background(), tc.policy, func(ctx context.Context) error {
				attempts++
				last = tc.fail()
				return last
			})
			if attempts != tc.want {
				t.Fatalf("ran %d attempts, want %d", attempts, tc.want)
			}
			if err != last {
				t.Fatalf("the last attempt's error must come back verbatim, got %v", err)
			}
		})
	}
}

// A policy that names no level is refused before anything is begun, naming
// the line that called RunTx.
func TestRunTx_RefusesAPolicyWithNoLevel(t *testing.T) {
	t.Parallel()

	ran := false
	err := (&Repo{}).RunTx(context.Background(), orm.Isolation(0), func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, orm.ErrInvalidTxOptions) {
		t.Fatalf("RunTx(unset level) = %v, want orm.ErrInvalidTxOptions", err)
	}
	if !strings.Contains(err.Error(), "repo_runtx_retry_test.go") {
		t.Errorf("err = %q, want it to name this file's call site", err)
	}
	if ran {
		t.Fatal("f ran despite a policy with no level")
	}
}

// A non-retryable error must fail on the FIRST attempt. Retrying a genuine
// business error (a validation failure, a missing row) wastes the budget and
// delays the caller's error by the full backoff ladder.
func TestRunTx_DoesNotRetryNonRetryable(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	sentinel := errors.New("business rule violated")
	attempts := 0
	err := repo.RunTx(context.Background(), orm.Serializable, func(ctx context.Context) error {
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

type declinedForTest struct{}

func (declinedForTest) Error() string { return "declined" }
func (declinedForTest) TxOutcome()    {}

// A body that rolls back ON PURPOSE (a declined launch, a missing row) is
// control flow, not a transaction failure, and must not be logged at ERROR:
// prod's error tracker turned every "waiting for workflow capacity" decline
// into a "Transaction failed" issue (*launch.DeclinedError, 85+ events/4h).
// A TxOutcome is recognised through wrapping; anything else stays a failure.
func TestIsExpectedTxOutcome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"no rows", sql.ErrNoRows, true},
		{"wrapped no rows", fmt.Errorf("lookup: %w", sql.ErrNoRows), true},
		{"tx outcome", declinedForTest{}, true},
		{"wrapped tx outcome", fmt.Errorf("launch: %w", declinedForTest{}), true},
		{"business failure", errors.New("business rule violated"), false},
		{"database failure", serializationFailure(), false},
	} {
		if got := isExpectedTxOutcome(tc.err); got != tc.want {
			t.Errorf("%s: isExpectedTxOutcome(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
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
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	recordRetryBackoff(repo)

	err := repo.RunTx(context.Background(), orm.Serializable, func(ctx context.Context) error {
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
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	outerAttempts := 0
	innerAttempts := 0

	err := repo.RunTx(context.Background(), orm.Serializable, func(outerCtx context.Context) error {
		outerAttempts++
		return repo.RunTx(outerCtx, orm.Serializable, func(innerCtx context.Context) error {
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
//
// The backoffs are recorded rather than slept (see recordRetryBackoff), which
// pins more than wall-clock gaps could: exactly one backoff before each retry,
// none before the first attempt, each on the capped exponential ladder.
func TestRunTx_BacksOffBetweenRetries(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	backoffs := recordRetryBackoff(repo)

	_ = repo.RunTx(context.Background(), orm.Serializable, func(ctx context.Context) error {
		return serializationFailure()
	})

	if len(*backoffs) != maxRetries {
		t.Fatalf("expected one backoff before each of the %d retries, got %d: %v", maxRetries, len(*backoffs), *backoffs)
	}
	for attempt, got := range *backoffs {
		// calculateBackoff: baseRetryDelay doubling per attempt, capped at
		// maxRetryDelay, with jitter in [-25%, +25%).
		want := min(baseRetryDelay<<attempt, maxRetryDelay)
		if got < want*3/4 || got >= want*5/4 {
			t.Fatalf("backoff before retry %d = %v, want %v ±25%%", attempt+1, got, want)
		}
	}
}

// The retry loop must respect context cancellation rather than burning the
// full backoff ladder on a caller that has already given up.
func TestRunTx_HonorsContextCancellation(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
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
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	recordRetryBackoff(repo)

	err := repo.RunTx(context.Background(), orm.Serializable, func(ctx context.Context) error {
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
