// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func memoTestTrigger(t *testing.T, userID string) *core.Trigger {
	t.Helper()
	cfg, err := json.Marshal(core.IntegrationConfig{Integration: "gh"})
	require.NoError(t, err)
	return &core.Trigger{ID: "t-" + userID, UserID: userID, Kind: core.TriggerKindIntegration, Enabled: true, Config: cfg}
}

// Under a request's memo, one owner's access to one integration is re-read
// once however many of their triggers the request refreshes; another owner's
// is still read.
func TestRefreshTriggerAccessRunsOncePerOwnerUnderTheMemo(t *testing.T) {
	t.Parallel()

	access := &fakeAccess{}
	s := &Service{deps: Deps{Access: map[string]IntegrationAccess{"gh": access}}}
	ctx := withAccessRefreshMemo(context.Background())
	assert.Same(t, accessRefreshMemoFrom(ctx), accessRefreshMemoFrom(withAccessRefreshMemo(ctx)), "a nested scope reuses the request's memo")

	for range 4 {
		require.NoError(t, s.refreshTriggerAccess(ctx, memoTestTrigger(t, "alice")))
	}
	require.NoError(t, s.refreshTriggerAccess(ctx, memoTestTrigger(t, "bob")))
	assert.Equal(t, []string{"alice", "bob"}, access.users())

	// Without a memo every call refreshes, as before.
	require.NoError(t, s.refreshTriggerAccess(context.Background(), memoTestTrigger(t, "alice")))
	require.NoError(t, s.refreshTriggerAccess(context.Background(), memoTestTrigger(t, "alice")))
	assert.Equal(t, []string{"alice", "bob", "alice", "alice"}, access.users())
}

// A failed refresh is remembered too: the request's later triggers get the
// same refusal, classified the same way, without asking the provider again.
func TestRefreshTriggerAccessMemoKeepsTheFailure(t *testing.T) {
	t.Parallel()

	access := &fakeAccess{err: errors.New("GitHub is not connected"), permanent: true}
	s := &Service{deps: Deps{Access: map[string]IntegrationAccess{"gh": access}}}
	ctx := withAccessRefreshMemo(context.Background())

	for range 3 {
		err := s.refreshTriggerAccess(ctx, memoTestTrigger(t, "alice"))
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	}
	assert.Equal(t, []string{"alice"}, access.users())
}
