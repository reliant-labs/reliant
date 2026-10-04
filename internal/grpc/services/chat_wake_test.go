// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// wakeRecordingRouter is a daemon router that can also wake, recording each
// EnsureAwake with the JWT the user held at that moment.
type wakeRecordingRouter struct {
	*fakeDaemonRouter
	mu        sync.Mutex
	selectors []*toolexec.DaemonSelector
	jwts      []string
	userIDs   []string
	err       error
}

func (r *wakeRecordingRouter) EnsureAwake(_ context.Context, userID string, selector *toolexec.DaemonSelector) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	jwt, _ := auth.GetUserJWT(userID)
	r.selectors = append(r.selectors, selector)
	r.jwts = append(r.jwts, jwt)
	r.userIDs = append(r.userIDs, userID)
	return "daemon-pinned", r.err
}

func newAttendedSendFixture(t *testing.T, wakeErr error) (*ChatService, *wakeRecordingRouter, context.Context, string) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	pinned := "daemon-pinned"
	require.NoError(t, repo.UpdateChatActiveDaemon(ctx, fx.chatID, &pinned))

	auth.SetUserJWT("test-user", "user-jwt")
	t.Cleanup(func() { auth.SetUserJWT("test-user", "") })

	temporal := &wakeTestTemporalClient{absorbTestTemporalClient: absorbTestTemporalClient{
		exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED}}
	router := &wakeRecordingRouter{fakeDaemonRouter: &fakeDaemonRouter{}, err: wakeErr}
	svc := &ChatService{
		database: repo, tempClient: temporal, daemonRouter: router,
		runs: runs.NewService(repo, temporal, nil),
	}
	return svc, router, ctx, fx.chatID
}

func TestSendMessageWakesTheChatsPinnedDaemonOnceWithTheUsersJWT(t *testing.T) {
	svc, router, ctx, chatID := newAttendedSendFixture(t, nil)

	_, err := svc.SendMessage(ctx, sendMessageRequest(t, chatID, "are you there"))
	require.NoError(t, err)

	require.Len(t, router.selectors, 1, "exactly one wake per attended send")
	require.NotNil(t, router.selectors[0])
	assert.Equal(t, "daemon-pinned", router.selectors[0].ID)
	assert.Equal(t, "user-jwt", router.jwts[0])
	assert.Equal(t, "test-user", router.userIDs[0])
}

func TestSendMessageStillAcceptsTheMessageWhenTheWakeFails(t *testing.T) {
	svc, router, ctx, chatID := newAttendedSendFixture(t, errors.New("control plane down"))

	_, err := svc.SendMessage(ctx, sendMessageRequest(t, chatID, "are you there"))
	require.NoError(t, err, "a failed wake must not fail the send")
	assert.Len(t, router.selectors, 1)
}
