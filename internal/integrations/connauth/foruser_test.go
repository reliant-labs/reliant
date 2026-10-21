// Copyright (c) 2025 Reliant Labs

package connauth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// ForUser is the credential source for a user's own reads outside any run: a
// picker. These pin that it is exactly as strict as a run's, with the owner
// being the authenticated caller.

func githubSpec(t *testing.T) httpaction.CredentialRequest {
	t.Helper()
	specs := specs(t, "github")
	return httpaction.CredentialRequest{IntegrationID: "github", Connection: specs["github"], ServerPlaced: true, ActionID: "labels.list"}
}

func credErr(t *testing.T, err error) *httpaction.CredentialError {
	t.Helper()
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce), "want a CredentialError, got %v", err)
	return ce
}

func TestForUserResolvesTheUsersDefaultConnectionAndRecordsTheUse(t *testing.T) {
	e := newEnv(t)
	pat := e.githubPAT("alice")
	ctx := context.Background()

	cred, err := e.source.ForUser("alice").Credential(ctx, githubSpec(t))
	require.NoError(t, err)
	assert.Equal(t, pat, cred.ConnectionID())

	events, err := e.repo.Connections().ListConnectionEvents(ctx, "alice", pat, 100, 0)
	require.NoError(t, err)
	var uses []core.ConnectionEvent
	for _, ev := range events {
		if ev.Kind == core.ConnectionEventUsed {
			uses = append(uses, ev)
		}
	}
	require.Len(t, uses, 1)
	assert.Equal(t, "user:alice", uses[0].Actor, "the use is attributed to the person, not a run")
	assert.Empty(t, uses[0].RunID)
}

func TestForUserNamedConnectionMustBeTheUsersOwn(t *testing.T) {
	e := newEnv(t)
	alices := e.githubPAT("alice")
	ctx := context.Background()

	req := githubSpec(t)
	req.ConnectionID = alices
	_, err := e.source.ForUser("alice").Credential(ctx, req)
	require.NoError(t, err)

	// bob names alice's connection: it reads as no connection at all, and
	// does NOT fall through to a default or a delegated authority.
	e.githubPAT("bob")
	_, err = e.source.ForUser("bob").Credential(ctx, req)
	require.Error(t, err)
	assert.Equal(t, httpaction.CodeFailedPrecondition, credErr(t, err).Code)
	assert.Equal(t, "no github connection", credErr(t, err).Message, "no hint that the id exists")
}

func TestForUserRefusesAConnectionOfAnotherIntegration(t *testing.T) {
	e := newEnv(t)
	httpKey := e.apiKey("alice", "http", "k", "")
	req := githubSpec(t)
	req.ConnectionID = httpKey
	_, err := e.source.ForUser("alice").Credential(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, credErr(t, err).Message, "not \"github\"")
}

func TestForUserWithoutAConnectionFallsBackToTheDelegatedBroker(t *testing.T) {
	e := newEnv(t)
	broker := &ownerBroker{connected: map[string]bool{"alice": true}}
	src := e.withBroker(broker)

	cred, err := src.ForUser("alice").Credential(context.Background(), githubSpec(t))
	require.NoError(t, err)
	assert.Equal(t, "delegated", cred.ConnectionID())
	assert.Equal(t, []string{"alice"}, broker.asked, "the broker is asked for the authenticated user, not for anybody else")

	_, err = src.ForUser("bob").Credential(context.Background(), githubSpec(t))
	require.Error(t, err)
	assert.Contains(t, credErr(t, err).Message, "not connected")
}

// An explicit connection that does not resolve is not redirected to the
// broker: the caller named one, and it does not exist for them.
func TestForUserNamedConnectionNeverFallsBackToTheBroker(t *testing.T) {
	e := newEnv(t)
	broker := &ownerBroker{connected: map[string]bool{"bob": true}}
	src := e.withBroker(broker)
	req := githubSpec(t)
	req.ConnectionID = "no-such-connection"
	_, err := src.ForUser("bob").Credential(context.Background(), req)
	require.Error(t, err)
	assert.Empty(t, broker.asked)
}

func TestForUserRefusesAWriteADaemonPlacementAndNoUser(t *testing.T) {
	e := newEnv(t)
	e.githubPAT("alice")
	ctx := context.Background()

	req := githubSpec(t)
	req.Mutates = true
	_, err := e.source.ForUser("alice").Credential(ctx, req)
	require.Error(t, err)
	assert.Equal(t, httpaction.CodeInvalidArgument, credErr(t, err).Code, "a picker never writes")

	req = githubSpec(t)
	req.ServerPlaced = false
	_, err = e.source.ForUser("alice").Credential(ctx, req)
	require.Error(t, err)
	assert.Equal(t, httpaction.CodeFailedPrecondition, credErr(t, err).Code)

	_, err = e.source.ForUser("").Credential(ctx, githubSpec(t))
	require.Error(t, err, "an empty user resolves nothing, not the first user's connection")
}

// A read keeps the user's own access whoever the automation will act as: an
// acting identity on the request is not honoured.
func TestForUserIgnoresTheActingIdentityForAListing(t *testing.T) {
	e := newEnv(t)
	pat := e.githubPAT("alice")
	req := githubSpec(t)
	req.ActingIdentity = httpaction.IdentityApp
	cred, err := e.source.ForUser("alice").Credential(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, pat, cred.ConnectionID(), "the user's own credential, not the bot's")
}

func TestForUserWithoutAResolverRefuses(t *testing.T) {
	src := connauth.New(connauth.Deps{})
	_, err := src.ForUser("alice").Credential(context.Background(), githubSpec(t))
	require.Error(t, err)
	assert.Equal(t, httpaction.CodeFailedPrecondition, credErr(t, err).Code)
}
