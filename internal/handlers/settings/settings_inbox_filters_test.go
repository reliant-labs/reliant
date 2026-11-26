package settings

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// The Inbox filters are user-global booleans, off by default; an update
// writes the keys the inbox reads, never shows them twice in additional, and
// tells the person's clients to re-read the Inbox (its badge follows them).
func TestSettingsService_InboxFiltersRoundTrip(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	var refetched []core.RefetchType
	svc := newTestService(t, Deps{Repo: repo, Refetch: &MockUserRefetch{EmitUserRefetchFunc: func(_ context.Context, _ string, rt core.RefetchType, _ core.RefetchOpts) error {
		refetched = append(refetched, rt)
		return nil
	}}})
	ctx := newSettingsServiceTestContext()
	get := func() *reliantv1.GetPreferencesResponse {
		t.Helper()
		resp, err := svc.GetPreferences(ctx, connect.NewRequest(&reliantv1.GetPreferencesRequest{}))
		require.NoError(t, err)
		return resp.Msg
	}

	msg := get()
	assert.False(t, msg.InboxShowHousekeeping, "off by default")
	assert.False(t, msg.InboxShowEveryAutomationFailure, "off by default")

	for _, tc := range []struct {
		name                   string
		req                    *reliantv1.UpdatePreferencesRequest
		housekeeping, everyOne bool
		inboxRefetch           bool
	}{
		{"housekeeping on", &reliantv1.UpdatePreferencesRequest{InboxShowHousekeeping: ptr.Of(true)}, true, false, true},
		{"every failure on, housekeeping untouched", &reliantv1.UpdatePreferencesRequest{InboxShowEveryAutomationFailure: ptr.Of(true)}, true, true, true},
		{"both off", &reliantv1.UpdatePreferencesRequest{InboxShowHousekeeping: ptr.Of(false), InboxShowEveryAutomationFailure: ptr.Of(false)}, false, false, true},
		{"an unrelated preference does not refetch the inbox", &reliantv1.UpdatePreferencesRequest{HideBuiltinPresets: ptr.Of(true)}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refetched = nil
			_, err := svc.UpdatePreferences(ctx, connect.NewRequest(tc.req))
			require.NoError(t, err)
			msg := get()
			assert.Equal(t, tc.housekeeping, msg.InboxShowHousekeeping)
			assert.Equal(t, tc.everyOne, msg.InboxShowEveryAutomationFailure)
			assert.Equal(t, tc.inboxRefetch, contains(refetched, core.RefetchInbox), "inbox refetch: %v", refetched)
			for key := range msg.Additional {
				assert.NotContains(t, key, "inbox.", "a dedicated field is not repeated in additional")
			}
		})
	}

	stored, err := repo.GetSetting(ctx, "test-user", nil, inboxShowHousekeepingKey)
	require.NoError(t, err)
	assert.Equal(t, "false", stored.Value, "the key the inbox reads, user-global")
}

func contains(types []core.RefetchType, want core.RefetchType) bool {
	for _, rt := range types {
		if rt == want {
			return true
		}
	}
	return false
}
