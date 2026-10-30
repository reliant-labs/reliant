// Copyright (c) 2025 Reliant Labs

package connauth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// actingBroker is a GitHub delegated authority that also has the App's bot.
type actingBroker struct {
	ownerBroker
	acted []string
}

type botCred struct{ brokerCred }

func (botCred) ConnectionID() string { return "bot" }

func (b *actingBroker) ActingCredential(_ context.Context, owner string, _ httpaction.CredentialRequest) (httpaction.Credential, error) {
	b.acted = append(b.acted, owner)
	return botCred{}, nil
}

func botWrite(t *testing.T, e *env, integration, connectionID string) httpaction.CredentialRequest {
	t.Helper()
	return httpaction.CredentialRequest{
		RunID: e.newRun("alice"), IntegrationID: integration, Connection: specs(t, integration)[integration],
		ConnectionID: connectionID, ServerPlaced: true, Mutates: true, ActingIdentity: httpaction.IdentityApp,
		ActionID: "issue.comment", Params: map[string]any{"owner": "acme", "repo": "api"},
	}
}

// The bot is every automation's default, so a write to an integration that has
// no bot (Slack, plain HTTP) must still work: it goes through the owner's own
// connection, as it always did, because there is no other identity it could mean.
func TestBotAutomationWritesToAnIntegrationWithoutABotAsTheOwner(t *testing.T) {
	e := newEnv(t)
	key := e.apiKey("alice", "http", "k", "")
	broker := &actingBroker{}

	cred, err := e.withBroker(broker).Credential(context.Background(), botWrite(t, e, "http", key))
	require.NoError(t, err)
	assert.Equal(t, key, cred.ConnectionID(), "the owner's saved connection")
	assert.Empty(t, broker.acted, "an integration with no bot never asks for one")

	cred, err = e.withBroker(broker).Credential(context.Background(), botWrite(t, e, "http", ""))
	require.NoError(t, err, "the owner's default connection, as for any owner write")
	assert.Equal(t, key, cred.ConnectionID())
}

// GitHub has the bot: a bot automation's write is the bot's, never the owner's.
func TestBotAutomationWritesToGitHubAsTheBot(t *testing.T) {
	e := newEnv(t)
	e.githubPAT("alice") // a saved owner connection must not win over the bot
	broker := &actingBroker{}

	cred, err := e.withBroker(broker).Credential(context.Background(), botWrite(t, e, "github", ""))
	require.NoError(t, err)
	assert.Equal(t, "bot", cred.ConnectionID())
	assert.Equal(t, []string{"alice"}, broker.acted)
}

// GitHub without a broker that can act: the write fails, it is never the owner's.
func TestBotAutomationWriteToGitHubWithoutTheBotFailsClosed(t *testing.T) {
	e := newEnv(t)
	pat := e.githubPAT("alice")
	for name, source := range map[string]connauth.Service{
		"no broker":         e.source,
		"broker cannot act": e.withBroker(&ownerBroker{connected: map[string]bool{"alice": true}}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := source.Credential(context.Background(), botWrite(t, e, "github", ""))
			require.Error(t, err)
			assert.Equal(t, httpaction.CodeFailedPrecondition, credErr(t, err).Code)
		})
	}

	// A GitHub write that names the owner's saved connection contradicts the bot.
	_, err := e.withBroker(&actingBroker{}).Credential(context.Background(), botWrite(t, e, "github", pat))
	require.Error(t, err)
	assert.Equal(t, httpaction.CodeFailedPrecondition, credErr(t, err).Code)
}
