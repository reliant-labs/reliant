// Copyright (c) 2025 Reliant Labs

package connauth_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// ownerBroker is a delegated authority that knows which owners are connected.
type ownerBroker struct {
	connected map[string]bool
	err       error
	asked     []string
}

type brokerCred struct{}

func (brokerCred) ConnectionID() string      { return "delegated" }
func (brokerCred) Apply(*http.Request) error { return nil }
func (brokerCred) Scrub(s string) string     { return s }
func (brokerCred) Params() map[string]string { return nil }

func (b *ownerBroker) Credential(_ context.Context, owner string) (httpaction.Credential, error) {
	b.asked = append(b.asked, owner)
	if b.err != nil {
		return nil, b.err
	}
	if !b.connected[owner] {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "GitHub is not connected"}
	}
	return brokerCred{}, nil
}

// specs is the connection spec of each named builtin integration, as the
// tools package passes them.
func specs(t *testing.T, ids ...string) map[string]*reliantv1.ConnectionSpec {
	t.Helper()
	out := map[string]*reliantv1.ConnectionSpec{}
	for _, m := range catalog.MustBuiltin().Manifests() {
		for _, id := range ids {
			if m.GetId() == id {
				out[id] = m.GetConnection()
			}
		}
	}
	require.Len(t, out, len(ids))
	return out
}

func (e *env) withBroker(b connauth.DelegatedBroker) *connauth.Source {
	e.t.Helper()
	brokers := connauth.NewBrokers()
	require.NoError(e.t, brokers.Register("controlplane-github", b))
	return e.source.WithBrokers(brokers)
}

// A saved default connection makes its integration usable for the owner's
// runs, and only theirs: the owner comes from the run record.
func TestUsableIntegrations_SavedConnectionIsUsableForItsOwnersRuns(t *testing.T) {
	e := newEnv(t)
	e.githubPAT("alice")
	ctx := context.Background()

	got, err := e.source.UsableIntegrations(ctx, e.newRun("alice"), specs(t, "github", "slack"))
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"github": true}, got, "github is connected, slack is not")

	got, err = e.source.UsableIntegrations(ctx, e.newRun("bob"), specs(t, "github"))
	require.NoError(t, err)
	assert.Empty(t, got, "alice's connection is not usable from bob's run")
}

// Checking is not using: no `used` event, no last-used bump, so asking before
// every model turn leaves the connection's audit trail alone.
func TestUsableIntegrations_RecordsNoUse(t *testing.T) {
	e := newEnv(t)
	pat := e.githubPAT("alice")
	ctx := context.Background()
	uses := func() int {
		events, err := e.repo.Connections().ListConnectionEvents(ctx, "alice", pat, 100, 0)
		require.NoError(t, err)
		n := 0
		for _, ev := range events {
			if ev.Kind == core.ConnectionEventUsed {
				n++
			}
		}
		return n
	}
	lastUsed := func() any {
		conn, err := e.repo.Connections().GetConnection(ctx, "alice", pat)
		require.NoError(t, err)
		return conn.LastUsedAt
	}
	usesBefore, lastUsedBefore := uses(), lastUsed()

	got, err := e.source.UsableIntegrations(ctx, e.newRun("alice"), specs(t, "github"))
	require.NoError(t, err)
	require.True(t, got["github"])

	assert.Equal(t, usesBefore, uses(), "a check recorded a use")
	assert.Equal(t, lastUsedBefore, lastUsed(), "a check bumped last_used_at")
}

// A connection that needs re-auth is not usable, and — as at call time — the
// owner's delegated authority is NOT asked instead.
func TestUsableIntegrations_NeedsReauthIsNotUsableAndDoesNotFallBack(t *testing.T) {
	e := newEnv(t)
	pat := e.githubPAT("alice")
	require.NoError(t, e.repo.Connections().WithSecretsLock(context.Background(), "alice", pat, func(tx core.SecretsTx) error {
		return tx.MarkStatus(context.Background(), core.ConnectionStatusNeedsReauth, "invalid_grant")
	}))
	broker := &ownerBroker{connected: map[string]bool{"alice": true}}

	got, err := e.withBroker(broker).UsableIntegrations(context.Background(), e.newRun("alice"), specs(t, "github"))
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, broker.asked, "Credential does not fall back past a saved connection, so neither does this")
}

// With no saved connection, an integration a delegated authority backs is
// usable exactly when that authority has the owner's credential.
func TestUsableIntegrations_DelegatedAuthorityDecidesWithoutASavedConnection(t *testing.T) {
	e := newEnv(t)
	broker := &ownerBroker{connected: map[string]bool{"alice": true}}
	source := e.withBroker(broker)
	ctx := context.Background()

	got, err := source.UsableIntegrations(ctx, e.newRun("alice"), specs(t, "github", "slack"))
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"github": true}, got, "slack has no delegated method")
	assert.Equal(t, []string{"alice"}, broker.asked, "the broker is asked for the run's owner, once")

	got, err = source.UsableIntegrations(ctx, e.newRun("bob"), specs(t, "github"))
	require.NoError(t, err)
	assert.Empty(t, got, "bob has not connected GitHub at the authority")

	broker.err = errors.New("control-plane unreachable")
	got, err = source.UsableIntegrations(ctx, e.newRun("alice"), specs(t, "github"))
	require.NoError(t, err)
	assert.Empty(t, got, "an authority that cannot answer offers nothing: absent beats failing")
}

// A run whose owner cannot be read makes nothing usable, and says why.
func TestUsableIntegrations_UnknownOwnerMakesNothingUsable(t *testing.T) {
	e := newEnv(t)
	e.githubPAT("alice")
	broker := &ownerBroker{connected: map[string]bool{"alice": true}}

	got, err := e.withBroker(broker).UsableIntegrations(context.Background(), "no-such-run", specs(t, "github"))
	assert.Empty(t, got)
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
	assert.Empty(t, broker.asked)
}

// A process without connections answers nothing usable rather than erroring
// every model turn.
func TestUsableIntegrations_WithoutAResolverIsEmpty(t *testing.T) {
	got, err := connauth.New(nil).UsableIntegrations(context.Background(), "run", specs(t, "github"))
	require.NoError(t, err)
	assert.Empty(t, got)
}
