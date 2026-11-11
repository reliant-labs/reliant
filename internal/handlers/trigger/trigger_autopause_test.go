// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// A trigger the system paused because its creator left its org is resumed
// the normal way, and resuming re-checks membership: refused while the
// creator is still out, and once they rejoin it resumes and the reason is
// cleared. A trigger written before orgs has no org of its own; its
// project's org is the one checked.
func TestSetTriggerEnabled_ResumesACreatorLeftOrgPauseAfterRejoining(t *testing.T) {
	t.Parallel()
	for name, ownOrg := range map[string]bool{"an org's trigger": true, "a trigger with no org of its own": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testResumeAfterRejoining(t, ownOrg)
		})
	}
}

func testResumeAfterRejoining(t *testing.T, ownOrg bool) {
	env := setupTriggerTest(t).withOrgs(t)
	acme := "acme"
	created, err := env.createIn(env.ctx, env.orgProject(t, &acme), nil)
	require.NoError(t, err)
	if !ownOrg {
		_, err := env.repo.ORM().Bun().NewRaw(`UPDATE triggers SET org_id = NULL WHERE id = ?`, created.GetId()).Exec(context.Background())
		require.NoError(t, err)
	}
	paused, err := env.repo.PauseTrigger(context.Background(), created.GetId(), core.TriggerPausedCreatorLeftOrg)
	require.NoError(t, err)
	require.True(t, paused)
	got, err := env.svc.GetTrigger(env.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: created.GetId()}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetTrigger().GetEnabled())
	assert.Equal(t, core.TriggerPausedCreatorLeftOrg, got.Msg.GetTrigger().GetPausedReason(), "the API says why")

	resume := func(ctx context.Context) (*reliantv1.Trigger, error) {
		resp, err := env.svc.SetTriggerEnabled(ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: true}))
		if err != nil {
			return nil, err
		}
		return resp.Msg.GetTrigger(), nil
	}
	stored := func() *core.Trigger {
		t.Helper()
		row, err := env.repo.GetTrigger(context.Background(), created.GetId())
		require.NoError(t, err)
		return row
	}

	removed := orgs.New(leftAcmeSource{})
	env.configure(t, func(d *Deps) { d.Orgs = removed })
	_, err = resume(authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, env.userID), removed))
	requireDenied(t, err, connect.CodeNotFound, "")
	assert.False(t, stored().Enabled)
	assert.Equal(t, core.TriggerPausedCreatorLeftOrg, stored().PausedReason, "a refused resume leaves the reason")

	// An outage is not an answer either way: the resume is refused as
	// unavailable, and nothing changes.
	outage := orgs.New(downSource{})
	env.configure(t, func(d *Deps) { d.Orgs = outage })
	_, err = resume(authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, env.userID), outage))
	require.Error(t, err)
	if ownOrg {
		// The trigger's own check meets it first, and fails closed as it
		// does for any refused caller.
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "error: %v", err)
	} else {
		assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err), "error: %v", err)
	}
	assert.False(t, stored().Enabled)

	live := orgs.New(triggerOrgSource{})
	env.configure(t, func(d *Deps) { d.Orgs = live })
	resumed, err := resume(authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, env.userID), live))
	require.NoError(t, err)
	assert.True(t, resumed.GetEnabled())
	assert.Empty(t, resumed.GetPausedReason())
	assert.True(t, stored().Enabled)
	assert.Empty(t, stored().PausedReason)
}

// downSource is control-plane unavailable.
type downSource struct{}

func (downSource) ResolveOrgMembership(context.Context, string, string) (accesstokenclient.OrgMembership, error) {
	return accesstokenclient.OrgMembership{}, errors.New("control-plane unavailable")
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
