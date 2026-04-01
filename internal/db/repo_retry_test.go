// Copyright (c) 2025 Reliant Labs
package db

import (
	"errors"
	"testing"
	"time"
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
			name:     "database is locked",
			err:      errors.New("database is locked"),
			expected: true,
		},
		{
			name:     "database table is locked",
			err:      errors.New("database table is locked"),
			expected: true,
		},
		{
			name:     "database schema is locked",
			err:      errors.New("database schema is locked"),
			expected: true,
		},
		{
			name:     "busy error",
			err:      errors.New("database is busy"),
			expected: true,
		},
		{
			name:     "sqlite_busy error code",
			err:      errors.New("SQLITE_BUSY: cannot commit"),
			expected: true,
		},
		{
			name:     "sqlite_locked error code",
			err:      errors.New("SQLITE_LOCKED: table locked"),
			expected: true,
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
			name:     "case insensitive - BUSY",
			err:      errors.New("Database Is BUSY"),
			expected: true,
		},
		{
			name:     "case insensitive - LOCKED",
			err:      errors.New("Database TABLE Is LOCKED"),
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
	// Test that various SQLite error message formats are recognized
	sqliteErrors := []string{
		"Error 5: database is locked",
		"sqlite3: database is locked",
		"SQLITE_BUSY (5)",
		"database table is locked: users",
		"database schema is locked",
		"(5) database is locked",
	}

	for _, errMsg := range sqliteErrors {
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
	// Note: Constraint violations ARE now considered retryable to handle
	// parallel operations where another transaction may have succeeded.
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
