// Copyright (c) 2025 Reliant Labs
package services

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func (e *triggerTestEnv) createNamed(t *testing.T, name string) string {
	t.Helper()
	resp, err := e.svc.CreateTrigger(e.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: e.definition(func(d *reliantv1.TriggerDefinition) { d.Name = name }),
	}))
	require.NoError(t, err)
	return resp.Msg.GetTrigger().GetId()
}

func (e *triggerTestEnv) stored(t *testing.T) (string, error) {
	return e.repo.GetProviderAPIKey(e.ctx, e.userID, automationcred.Provider(e.daemonID))
}

func TestTriggerCreateMintsDaemonBoundResumeToken(t *testing.T) {
	env := setupTriggerTest(t)
	auth.SetUserJWT(env.userID, "jwt-1")
	t.Cleanup(func() { auth.SetUserJWT(env.userID, "") })
	cp := &fakeControlPlaneClient{}
	env.svc.WithControlPlaneClient(cp)

	env.createNamed(t, "a")

	require.Len(t, cp.mints, 1)
	assert.Equal(t, mintCall{JWT: "jwt-1", DaemonID: env.daemonID, Name: automationcred.KeyName}, cp.mints[0])
	got, err := env.stored(t)
	require.NoError(t, err)
	assert.Equal(t, "rlat_resume_"+env.daemonID, got)

	// Never visible as a provider credential.
	all, err := env.repo.GetProviderAPIKeys(env.ctx, env.userID)
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestTriggerCreateSelfHostedMintsNothing(t *testing.T) {
	env := setupTriggerTest(t) // no control-plane client
	auth.SetUserJWT(env.userID, "jwt-1")
	t.Cleanup(func() { auth.SetUserJWT(env.userID, "") })
	env.createNamed(t, "a")
	_, err := env.stored(t)
	assert.Error(t, err, "no token is stored without a control plane")
}

func TestTriggerCreateSurvivesMintFailure(t *testing.T) {
	env := setupTriggerTest(t)
	auth.SetUserJWT(env.userID, "jwt-1")
	t.Cleanup(func() { auth.SetUserJWT(env.userID, "") })
	env.svc.WithControlPlaneClient(&fakeControlPlaneClient{mintErr: errors.New("unknown scope")})
	id := env.createNamed(t, "a") // must not fail
	tr, err := env.repo.GetTrigger(env.ctx, id)
	require.NoError(t, err)
	assert.True(t, tr.Enabled)
}

func TestTriggerRevokesOnlyWhenNoEnabledTriggerRemains(t *testing.T) {
	env := setupTriggerTest(t)
	auth.SetUserJWT(env.userID, "jwt-1")
	t.Cleanup(func() { auth.SetUserJWT(env.userID, "") })
	cp := &fakeControlPlaneClient{}
	env.svc.WithControlPlaneClient(cp)

	first := env.createNamed(t, "a")
	second := env.createNamed(t, "b")

	_, err := env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: first, Enabled: false}))
	require.NoError(t, err)
	assert.Empty(t, cp.revokes, "another enabled trigger still names the daemon")

	_, err = env.svc.DeleteTrigger(env.ctx, connect.NewRequest(&reliantv1.DeleteTriggerRequest{Id: second}))
	require.NoError(t, err)
	assert.Equal(t, []string{env.daemonID}, cp.revokes)
	_, err = env.stored(t)
	assert.Error(t, err, "the stored token is forgotten with the revoke")
}

func TestTriggerDisableRevokes(t *testing.T) {
	env := setupTriggerTest(t)
	auth.SetUserJWT(env.userID, "jwt-1")
	t.Cleanup(func() { auth.SetUserJWT(env.userID, "") })
	cp := &fakeControlPlaneClient{}
	env.svc.WithControlPlaneClient(cp)
	id := env.createNamed(t, "a")

	_, err := env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: false}))
	require.NoError(t, err)
	assert.Equal(t, []string{env.daemonID}, cp.revokes)

	_, err = env.svc.SetTriggerEnabled(env.ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: true}))
	require.NoError(t, err)
	assert.Len(t, cp.mints, 2, "re-enabling mints again")
	_ = core.TriggerKindSchedule
}
