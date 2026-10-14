// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// hosted is a deployment with a control plane to mint the bot's tokens from.
func (e *triggerTestEnv) hosted(t *testing.T) *triggerTestEnv {
	t.Helper()
	e.configure(t, func(d *Deps) { d.ControlPlane = &fakeControlPlaneClient{} })
	return e
}

func (e *triggerTestEnv) storedIdentity(t *testing.T, id string) core.ActingIdentity {
	t.Helper()
	stored, err := e.repo.GetTrigger(context.Background(), id)
	require.NoError(t, err)
	return stored.ActingIdentity
}

func asApp(d *reliantv1.TriggerDefinition) {
	d.ActingIdentity = reliantv1.ActingIdentity_ACTING_IDENTITY_APP
}

// Nothing chose an identity: the activation is the owner's, exactly as every
// activation was before the field existed.
func TestCreateTrigger_ActingIdentityDefaultsToTheOwner(t *testing.T) {
	env := setupTriggerTest(t)
	got := env.create(t, env.definition(nil))
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, got.GetActingIdentity(), "never UNSPECIFIED on a stored trigger")
	assert.Equal(t, core.ActingIdentityOwner, env.storedIdentity(t, got.GetId()))
}

func TestCreateTrigger_ActingAsTheAppIsStoredAndReturned(t *testing.T) {
	env := setupTriggerTest(t).hosted(t)
	got := env.create(t, env.definition(asApp))
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, got.GetActingIdentity())
	assert.Equal(t, core.ActingIdentityApp, env.storedIdentity(t, got.GetId()))

	read, err := env.svc.GetTrigger(env.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: got.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, read.Msg.GetTrigger().GetActingIdentity())
}

// Self-hosted: no control plane, so no App key to mint from. The activation is
// refused when it is written rather than held as one that could never act.
func TestCreateTrigger_AppIdentityIsRefusedWithoutAControlPlane(t *testing.T) {
	env := setupTriggerTest(t) // no control plane
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: env.definition(asApp)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "control plane")
	assert.Zero(t, env.backend.syncCount(), "a refused activation must not touch the schedule backend")
}

// The owner identity is always available, hosted or not.
func TestCreateTrigger_ExplicitOwnerWorksEverywhere(t *testing.T) {
	env := setupTriggerTest(t)
	got := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) {
		d.ActingIdentity = reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER
	}))
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, got.GetActingIdentity())
}

// An update that does not mention the identity leaves it alone, so a client that
// predates the field cannot flip an activation back to the owner by omission.
func TestUpdateTrigger_OmittedIdentityIsUnchanged(t *testing.T) {
	env := setupTriggerTest(t).hosted(t)
	created := env.create(t, env.definition(asApp))

	_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: created.GetId(), Trigger: env.definition(func(d *reliantv1.TriggerDefinition) { d.Message = "A new prompt." }),
	}))
	require.NoError(t, err)
	assert.Equal(t, core.ActingIdentityApp, env.storedIdentity(t, created.GetId()), "an omitted identity must not reset to the owner")
}

func TestUpdateTrigger_IdentityCanBeChangedBothWays(t *testing.T) {
	env := setupTriggerTest(t).hosted(t)
	created := env.create(t, env.definition(nil))
	require.Equal(t, core.ActingIdentityOwner, env.storedIdentity(t, created.GetId()))

	update := func(identity reliantv1.ActingIdentity) {
		t.Helper()
		_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
			Id: created.GetId(), Trigger: env.definition(func(d *reliantv1.TriggerDefinition) { d.ActingIdentity = identity }),
		}))
		require.NoError(t, err)
	}
	update(reliantv1.ActingIdentity_ACTING_IDENTITY_APP)
	assert.Equal(t, core.ActingIdentityApp, env.storedIdentity(t, created.GetId()))
	update(reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER)
	assert.Equal(t, core.ActingIdentityOwner, env.storedIdentity(t, created.GetId()))
}

// Choosing the bot on an existing activation is refused where it could never
// work, as at create.
func TestUpdateTrigger_AppIdentityIsRefusedWithoutAControlPlane(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))
	_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: created.GetId(), Trigger: env.definition(asApp),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, core.ActingIdentityOwner, env.storedIdentity(t, created.GetId()))
}

func TestListTriggers_CarriesTheIdentity(t *testing.T) {
	env := setupTriggerTest(t).hosted(t)
	owner := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) { d.Name = "mine" }))
	bot := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) { d.Name = "the bot's"; asApp(d) }))

	resp, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	got := map[string]reliantv1.ActingIdentity{}
	for _, tr := range resp.Msg.GetTriggers() {
		got[tr.GetId()] = tr.GetActingIdentity()
	}
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, got[owner.GetId()])
	assert.Equal(t, reliantv1.ActingIdentity_ACTING_IDENTITY_APP, got[bot.GetId()])
}
