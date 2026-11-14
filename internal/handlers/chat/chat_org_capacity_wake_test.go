// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/capacitywake"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// An org policy change wakes every chat of THAT org a refusal may have
// blocked (capacitywake.WakeOrg over db.Repo.ListOrgCapacityWakeTargets),
// whoever's chat it is, and no chat of another org or of no org.
func TestWakeOrg_FansOutToTheOrgsBlockedChats(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	inOrg := func(user, org string, status db.WorkflowStatus) string {
		ctx := context.WithValue(context.Background(), auth.UserIDContextKey, user)
		id := seedChatRun(t, repo, ctx, user, status)
		if org != "" {
			_, err := repo.ORM().Bun().NewRaw("UPDATE chats SET org_id = ? WHERE id = ?", org, id).Exec(ctx)
			require.NoError(t, err)
		}
		return id
	}
	alicePaused := inOrg("alice-wake-org", "org-acme", db.Paused())
	bobPaused := inOrg("bob-wake-org", "org-acme", db.Paused())
	_ = inOrg("carol-wake-org", "org-acme", db.Active())    // nothing blocked
	_ = inOrg("dave-wake-org", "org-globex", db.Paused())   // another org
	_ = inOrg("erin-wake-org", "", db.Paused())             // no org
	_ = inOrg("frank-wake-org", "org-acme", db.Completed()) // ended

	temporal := &usageLimitTemporalClient{}
	failed, err := capacitywake.WakeOrg(context.Background(), repo, temporal, "org-acme", "the organization's policy changed")
	require.NoError(t, err)
	assert.Empty(t, failed)
	assert.ElementsMatch(t, []string{
		alicePaused + ":" + v2.CapacityWakeSignalName,
		bobPaused + ":" + v2.CapacityWakeSignalName,
	}, temporal.signals)
	failed, err = capacitywake.WakeOrg(context.Background(), repo, temporal, "", "x")
	assert.NoError(t, err)
	assert.Empty(t, failed, "no org wakes nothing")
	assert.Len(t, temporal.signals, 2, "no org wakes nothing")
}
