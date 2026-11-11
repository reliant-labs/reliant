// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// A trigger the system paused because its creator left its org is resumed
// the normal way, and resuming re-checks membership: refused while the
// creator is still out, and once they rejoin it resumes and the reason is
// cleared.
func TestSetTriggerEnabled_ResumesACreatorLeftOrgPauseAfterRejoining(t *testing.T) {
	t.Parallel()
	env := setupTriggerTest(t).withOrgs(t)
	acme := "acme"
	created, err := env.createIn(env.ctx, env.orgProject(t, &acme), nil)
	require.NoError(t, err)
	paused, err := env.repo.PauseTrigger(context.Background(), created.GetId(), core.TriggerPausedCreatorLeftOrg)
	require.NoError(t, err)
	require.True(t, paused)

	resume := func(ctx context.Context) error {
		_, err := env.svc.SetTriggerEnabled(ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: true}))
		return err
	}
	stored := func() *core.Trigger {
		t.Helper()
		row, err := env.repo.GetTrigger(context.Background(), created.GetId())
		require.NoError(t, err)
		return row
	}

	removed := orgs.New(leftAcmeSource{})
	env.configure(t, func(d *Deps) { d.Orgs = removed })
	err = resume(authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, env.userID), removed))
	requireDenied(t, err, connect.CodeNotFound, "")
	assert.False(t, stored().Enabled)
	assert.Equal(t, core.TriggerPausedCreatorLeftOrg, stored().PausedReason, "a refused resume leaves the reason")

	live := orgs.New(triggerOrgSource{})
	env.configure(t, func(d *Deps) { d.Orgs = live })
	require.NoError(t, resume(authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, env.userID), live)))
	assert.True(t, stored().Enabled)
	assert.Empty(t, stored().PausedReason)
}

// A person pausing a trigger is not the system: the reason is cleared, so the
// trigger reads as a plain pause.
func TestSetTriggerEnabled_APersonsPauseClearsTheReason(t *testing.T) {
	t.Parallel()
	env := setupTriggerTest(t).withOrgs(t)
	acme := "acme"
	created, err := env.createIn(env.ctx, env.orgProject(t, &acme), nil)
	require.NoError(t, err)
	_, err = env.repo.PauseTrigger(context.Background(), created.GetId(), core.TriggerPausedCreatorLeftOrg)
	require.NoError(t, err)

	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: false}))
	require.NoError(t, err)
	row, err := env.repo.GetTrigger(context.Background(), created.GetId())
	require.NoError(t, err)
	assert.False(t, row.Enabled)
	assert.Empty(t, row.PausedReason)
}
