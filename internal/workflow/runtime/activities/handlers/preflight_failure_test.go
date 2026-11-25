// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/errclass"
)

// The workflow retries PreflightDaemonCheck a few times so one lost heartbeat
// does not fail a run. What the check decides itself must not ride that
// ladder: a missing machine retried is the same error card three times, and
// the message has already been queued for the machine.
func TestPreflightFailure_VerdictIsNonRetryable(t *testing.T) {
	t.Parallel()

	verdict := svcerr.WithClass(errors.New("This chat's machine was removed"), svcerr.ClassUser)
	err := preflightFailure(verdict)

	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr))
	assert.True(t, appErr.NonRetryable())
	assert.Equal(t, PreflightDaemonCheckFailedErrType, appErr.Type())
	assert.Equal(t, "This chat's machine was removed", appErr.Message(), "the user-facing text is unchanged")
	assert.Equal(t, temporal.ApplicationErrorCategoryBenign, errclass.TemporalCategory(err),
		"a user error still crosses the activity boundary as benign")
}

// A database that did not answer is retried, and a cancellation stays a
// cancellation.
func TestPreflightFailure_UnavailableAndCancelPassThrough(t *testing.T) {
	t.Parallel()

	assert.NoError(t, preflightFailure(nil))

	dbDown := fmt.Errorf("failed to get chat for preflight check: %w", &pgconn.ConnectError{})
	assert.Same(t, dbDown, preflightFailure(dbDown), "left to classifyError, which types it DatabaseUnavailable (retryable)")

	cancelled := fmt.Errorf("check: %w", context.Canceled)
	assert.Same(t, cancelled, preflightFailure(cancelled))
}

// End to end through the activity: a refused wake fails non-retryable.
func TestPreflightWait_HardWakeErrorIsNotRetried(t *testing.T) {
	t.Parallel()

	router := &waitFakeRouter{wakeErr: errors.New("no daemon registered for this account")}
	a, _, chatID := newWaitFixture(t, false, router)
	_, err := runPreflight(t, a, PreflightDaemonCheckInput{ChatID: chatID, WaitSeconds: 5})
	require.Error(t, err)

	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr), "got %T: %v", err, err)
	assert.True(t, appErr.NonRetryable())
	assert.Equal(t, PreflightDaemonCheckFailedErrType, appErr.Type())
	assert.Contains(t, err.Error(), "no daemon registered for this account")
}
