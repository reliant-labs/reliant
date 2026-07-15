// Copyright (c) 2025 Reliant Labs
package db

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "concurrent update",
			err:      errors.New("concurrent update detected"),
			expected: true,
		},
		{
			name:     "could not serialize",
			err:      errors.New("could not serialize access"),
			expected: true,
		},
		{
			name:     "deadlock",
			err:      errors.New("deadlock detected"),
			expected: true,
		},
		{
			name:     "transaction conflict",
			err:      errors.New("transaction conflict occurred"),
			expected: true,
		},
		{
			name:     "non-retryable error",
			err:      errors.New("constraint violation"),
			expected: false,
		},
		{
			name:     "not found error",
			err:      errors.New("not found"),
			expected: false,
		},
		{
			name:     "syntax error",
			err:      errors.New("syntax error"),
			expected: false,
		},
		{
			name:     "case insensitive - DEADLOCK",
			err:      errors.New("Deadlock Detected"),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRetryableError(tt.err)
			if result != tt.expected {
				t.Errorf("expected %v, got %v for error: %v", tt.expected, result, tt.err)
			}
		})
	}
}

func TestIsRetryableErrorSQLState(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			// 40001 must be retryable regardless of message text — this
			// message contains none of the legacy substrings.
			name:     "pgconn 40001 with non-standard message",
			err:      &pgconn.PgError{Code: "40001", Message: "canceling statement due to conflict with recovery"},
			expected: true,
		},
		{
			name:     "pgconn 40001 wrapped through fmt.Errorf chain",
			err:      fmt.Errorf("failed to create chat_update: %w", fmt.Errorf("failed to get max sequence: %w", &pgconn.PgError{Code: "40001", Message: "whatever"})),
			expected: true,
		},
		{
			name:     "pgconn 40P01 deadlock",
			err:      &pgconn.PgError{Code: "40P01", Message: "deadlock detected"},
			expected: true,
		},
		{
			name:     "pgconn 25P02 aborted transaction",
			err:      &pgconn.PgError{Code: "25P02", Message: "current transaction is aborted"},
			expected: true,
		},
		{
			name:     "pgconn 23505 unique violation",
			err:      &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"},
			expected: true,
		},
		{
			name:     "pgconn 42601 syntax error is not retryable",
			err:      &pgconn.PgError{Code: "42601", Message: "syntax error at or near SELECT"},
			expected: false,
		},
		{
			// Error flattened to a string (pgconn error no longer in chain)
			// but still carrying the pgx-rendered SQLSTATE suffix.
			name:     "flattened string with SQLSTATE 40001",
			err:      errors.New("failed to get max sequence: ERROR: some message (SQLSTATE 40001)"),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRetryableError(tt.err)
			if result != tt.expected {
				t.Errorf("expected %v, got %v for error: %v", tt.expected, result, tt.err)
			}
		})
	}
}

func TestCalculateBackoff(t *testing.T) {
	tests := []struct {
		name          string
		attempt       int
		expectedMinMs int64
		expectedMaxMs int64
	}{
		{
			name:          "first attempt",
			attempt:       0,
			expectedMinMs: 37, // 50ms base - 25% jitter = ~37ms
			expectedMaxMs: 75, // 50ms base + 25% jitter = ~75ms
		},
		{
			name:          "second attempt",
			attempt:       1,
			expectedMinMs: 75,  // 100ms base - 25% jitter
			expectedMaxMs: 150, // 100ms base + 25% jitter
		},
		{
			name:          "third attempt",
			attempt:       2,
			expectedMinMs: 150, // 200ms base - 25% jitter
			expectedMaxMs: 300, // 200ms base + 25% jitter
		},
		{
			name:          "attempt beyond max delay",
			attempt:       10,
			expectedMinMs: 750,  // maxRetryDelay (1s) - 25% jitter
			expectedMaxMs: 1500, // maxRetryDelay (1s) + 25% jitter
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay := calculateBackoff(tt.attempt)
			delayMs := delay.Milliseconds()

			if delayMs < tt.expectedMinMs || delayMs > tt.expectedMaxMs {
				t.Errorf("expected delay between %dms and %dms, got %dms",
					tt.expectedMinMs, tt.expectedMaxMs, delayMs)
			}
		})
	}
}

func TestCalculateBackoffMonotonicity(t *testing.T) {
	// Test that backoff generally increases with attempts (allowing for jitter)
	prevMin := int64(0)

	for attempt := 0; attempt < 5; attempt++ {
		// Run multiple times to account for jitter
		minDelay := time.Duration(1<<63 - 1)
		for i := 0; i < 10; i++ {
			delay := calculateBackoff(attempt)
			if delay < minDelay {
				minDelay = delay
			}
		}

		// The minimum delay should generally increase (with some tolerance for jitter)
		if attempt > 0 && minDelay.Milliseconds() < prevMin {
			t.Errorf("backoff decreased: attempt %d had min %dms, previous was %dms",
				attempt, minDelay.Milliseconds(), prevMin)
		}
		prevMin = minDelay.Milliseconds()
	}
}

func TestCalculateBackoffMaxDelay(t *testing.T) {
	// Test that backoff never exceeds a reasonable maximum
	for attempt := 0; attempt < 20; attempt++ {
		delay := calculateBackoff(attempt)
		// Max should be maxRetryDelay (1s) + jitter (25%) = 1.25s
		if delay > 1500*time.Millisecond {
			t.Errorf("backoff exceeded reasonable maximum: attempt %d had delay %v",
				attempt, delay)
		}
	}
}

func TestCalculateBackoffJitter(t *testing.T) {
	// Test that jitter is actually applied (delays should vary)
	delays := make(map[time.Duration]bool)

	for i := 0; i < 20; i++ {
		delay := calculateBackoff(3)
		delays[delay] = true
	}

	// With jitter, we should see multiple different delay values
	if len(delays) < 5 {
		t.Errorf("expected at least 5 different delays due to jitter, got %d", len(delays))
	}
}

func TestRetryLogicConstants(t *testing.T) {
	// Verify that retry constants are sensible
	if maxRetries < 1 {
		t.Error("maxRetries should be at least 1")
	}
	if maxRetries > 10 {
		t.Error("maxRetries should not be excessive (> 10)")
	}

	if baseRetryDelay < 10*time.Millisecond {
		t.Error("baseRetryDelay should be at least 10ms")
	}
	if baseRetryDelay > 1*time.Second {
		t.Error("baseRetryDelay should not exceed 1 second")
	}

	if maxRetryDelay < baseRetryDelay {
		t.Error("maxRetryDelay should be >= baseRetryDelay")
	}
	if maxRetryDelay > 30*time.Second {
		t.Error("maxRetryDelay should not exceed 30 seconds")
	}

	if defaultDBTimeout < 1*time.Second {
		t.Error("defaultDBTimeout should be at least 1 second")
	}
}

func TestRetryErrorMessages(t *testing.T) {
	// Test that various Postgres error message formats are recognized as retryable
	retryableErrors := []string{
		"could not serialize access due to concurrent update",
		"deadlock detected",
		"current transaction is aborted",
	}

	for _, errMsg := range retryableErrors {
		t.Run(errMsg, func(t *testing.T) {
			err := errors.New(errMsg)
			if !isRetryableError(err) {
				t.Errorf("expected error to be retryable: %s", errMsg)
			}
		})
	}
}

func TestNonRetryableErrors(t *testing.T) {
	// Test that common non-retryable errors are correctly identified
	nonRetryableErrors := []string{
		"no such table: users",
		"no such column: invalid_column",
		"syntax error near SELECT",
		"datatype mismatch",
	}

	for _, errMsg := range nonRetryableErrors {
		t.Run(errMsg, func(t *testing.T) {
			err := errors.New(errMsg)
			if isRetryableError(err) {
				t.Errorf("expected error to NOT be retryable: %s", errMsg)
			}
		})
	}
}

func TestRetryableConstraintErrors(t *testing.T) {
	// Constraint violations are retryable because during parallel operations
	// another transaction may have succeeded, and we should retry to find the record
	retryableErrors := []string{
		"UNIQUE constraint failed: users.email",
		"NOT NULL constraint failed: users.name",
		"FOREIGN KEY constraint failed",
		"CHECK constraint failed",
	}

	for _, errMsg := range retryableErrors {
		t.Run(errMsg, func(t *testing.T) {
			err := errors.New(errMsg)
			if !isRetryableError(err) {
				t.Errorf("expected error to be retryable: %s", errMsg)
			}
		})
	}
}
