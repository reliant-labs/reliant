// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/dbconn"
	"github.com/reliant-labs/reliant/internal/errclass/temporalclass"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The workflow retries PreflightDaemonCheck a few times so one lost heartbeat
// does not fail a run. What the check decides itself must not ride that
// ladder: a missing machine retried is the same error card three times, and
// the message has already been queued for the machine.
func TestPreflightVerdict_IsNonRetryable(t *testing.T) {
	t.Parallel()

	verdict := svcerr.WithClass(errors.New("This chat's machine was removed"), svcerr.ClassUser)
	err := preflightVerdict(verdict)

	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr))
	assert.True(t, appErr.NonRetryable())
	assert.Equal(t, PreflightDaemonCheckFailedErrType, appErr.Type())
	assert.Equal(t, "This chat's machine was removed", appErr.Message(), "the user-facing text is unchanged")
	assert.Equal(t, temporal.ApplicationErrorCategoryBenign, temporalclass.Category(err),
		"a user error still crosses the activity boundary as benign")
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

// A database that did not answer, at any read the check returns an error
// from, is left to classifyError — which types it DatabaseUnavailable, and
// retries it — rather than made a verdict.
func TestPreflight_DatabaseOutageStaysRetryable(t *testing.T) {
	t.Parallel()

	outage := &pgconn.ConnectError{}
	for name, fail := range map[string]func(*preflightFailingStore, *preflightFailingRows){
		"GetChat":    func(s *preflightFailingStore, _ *preflightFailingRows) { s.chatErr = outage },
		"GetProject": func(_ *preflightFailingStore, r *preflightFailingRows) { r.projectErr = outage },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &preflightFailingStore{}
			rows := &preflightFailingRows{}
			fail(store, rows)
			a := NewPreflightDaemonCheckActivity(store, nil, rows, toolexec.NewRemoteExecutor(&waitFakeRouter{}))

			_, err := a.Execute(context.Background(), PreflightDaemonCheckInput{ChatID: "c1"})

			require.Error(t, err)
			assert.True(t, dbconn.Unavailable(err), "the outage keeps its identity for classifyError: %v", err)
			var appErr *temporal.ApplicationError
			assert.False(t, errors.As(err, &appErr) && appErr.NonRetryable(), "an outage is not a verdict: %v", err)
		})
	}
}

// preflightFailingStore is the check's store, reading one chat and failing
// GetChat with chatErr when set.
type preflightFailingStore struct {
	PreflightDaemonCheckStore
	chatErr error
}

func (s *preflightFailingStore) GetChat(context.Context, string) (*core.Chat, error) {
	if s.chatErr != nil {
		return nil, s.chatErr
	}
	return &core.Chat{ID: "c1", ProjectID: "p1"}, nil
}

// preflightFailingRows fails GetProject with projectErr.
type preflightFailingRows struct {
	WorkspaceRows
	projectErr error
}

func (r *preflightFailingRows) GetProject(context.Context, string) (*db.Project, error) {
	return nil, r.projectErr
}
