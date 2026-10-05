// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// fakeAccess stands in for the GitHub access refresher: it records whose
// access was refreshed and answers with a configured error.
type fakeAccess struct {
	mu        sync.Mutex
	refreshed []string
	err       error
	permanent bool
}

func (f *fakeAccess) Refresh(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed = append(f.refreshed, userID)
	return f.err
}

func (f *fakeAccess) IsPermanent(error) bool { return f.permanent }

func (f *fakeAccess) users() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.refreshed...)
}

func setupAccessTriggerTest(t *testing.T) (*triggerTestEnv, *fakeAccess) {
	t.Helper()
	env, _ := setupInboundTriggerTest(t)
	access := &fakeAccess{}
	env.svc.WithInbound(InboundOptions{
		PublicURL: "https://api.example.com/",
		Sealer:    fakeSealer{},
		Catalog:   fakeCatalog{"test": true, "gh": true},
		Intake:    &fakeIntake{},
		Access:    map[string]IntegrationAccess{"gh": access},
	})
	return env, access
}

func ghDefinition(env *triggerTestEnv, mutate func(*reliantv1.TriggerDefinition)) *reliantv1.TriggerDefinition {
	return env.definition(func(d *reliantv1.TriggerDefinition) {
		d.Name = "on issue " + uuid.NewString()[:6]
		d.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
			Integration: "gh", Events: []string{"issues.opened"}, Match: map[string]string{"repository": "acme/app"},
		}}
		if mutate != nil {
			mutate(d)
		}
	})
}

// An access-gated integration (GitHub, hosted) has no connection row: its
// token lives at control-plane. Creating a trigger needs no connection, and
// refreshes the owner's access so the first event routes.
func TestAccessGatedTriggerNeedsNoConnectionAndRefreshesAccess(t *testing.T) {
	env, access := setupAccessTriggerTest(t)
	created := env.create(t, ghDefinition(env, nil))
	assert.Empty(t, created.GetConnectionId(), "delegated: no connection row")
	assert.Equal(t, []string{env.userID}, access.users(), "the OWNER's access, refreshed at activation")

	stored, err := env.repo.GetTrigger(context.Background(), created.GetId())
	require.NoError(t, err)
	assert.Nil(t, stored.ConnectionID)
}

// With a saved connection the trigger still binds it (self-hosted), and an
// explicitly named one must be the caller's.
func TestAccessGatedTriggerBindsASavedConnectionWhenThereIsOne(t *testing.T) {
	env, _ := setupAccessTriggerTest(t)
	mine := env.createConnection(t, env.userID, "gh", "11", core.ConnectionStatusActive)
	created := env.create(t, ghDefinition(env, nil))
	assert.Equal(t, mine.ID, created.GetConnectionId())

	theirs := env.createConnection(t, uuid.NewString(), "gh", "22", core.ConnectionStatusActive)
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: ghDefinition(env, func(d *reliantv1.TriggerDefinition) { d.ConnectionId = &theirs.ID }),
	}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// A permanent refresh failure (GitHub not connected, or a token that cannot
// list installations) refuses the write with the reason; nothing is stored.
// A transient one is Unavailable.
func TestAccessGatedTriggerRefusesWhenAccessCannotBeRead(t *testing.T) {
	env, access := setupAccessTriggerTest(t)
	access.err, access.permanent = fmt.Errorf("GitHub is not connected: connect GitHub in Settings"), true
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: ghDefinition(env, nil)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "connect GitHub")

	access.err, access.permanent = errors.New("control-plane unreachable"), false
	_, err = env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: ghDefinition(env, nil)}))
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	list, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	assert.Empty(t, list.Msg.GetTriggers(), "a refused write stores nothing")
}

// Re-enabling and editing refresh too: either can be the first activation
// after access changed.
func TestAccessGatedTriggerRefreshesOnUpdateAndEnable(t *testing.T) {
	env, access := setupAccessTriggerTest(t)
	created := env.create(t, ghDefinition(env, nil))
	_, err := env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: false}))
	require.NoError(t, err)
	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: true}))
	require.NoError(t, err)
	_, err = env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: created.GetId(), Trigger: ghDefinition(env, nil),
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{env.userID, env.userID, env.userID}, access.users(),
		"create, enable and update refresh; disabling does not")

	access.err, access.permanent = errors.New("not connected"), true
	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: true}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// Integrations that are not access-gated keep requiring a connection.
func TestNonAccessGatedIntegrationStillRequiresAConnection(t *testing.T) {
	env, access := setupAccessTriggerTest(t)
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
				Integration: "test", Events: []string{"thing.created"},
			}}
		}),
	}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Empty(t, access.users())
}
