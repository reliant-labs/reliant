// Copyright (c) 2025 Reliant Labs
package commands

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/serverapi"
	"github.com/reliant-labs/reliant/internal/serverworker"
)

// integrationsEnv is every variable IntegrationsConfig reads, plus the OAuth
// client family member the tests use.
var integrationsEnv = []string{
	"PUBLIC_URL", "RELIANT_VAULT_KEY", "RELIANT_SLACK_SIGNING_SECRET", "RELIANT_GITHUB_WEBHOOK_SECRET",
	"RELIANT_GITHUB_APP_SLUG", "RELIANT_GITHUB_BOT_USER_ID", "RELIANT_GITHUB_BOT_LOGIN",
	"RELIANT_TEST_INTEGRATION_SECRET", "RELIANT_OAUTH_GMAIL_CLIENT_ID", "RELIANT_OAUTH_GMAIL_CLIENT_SECRET",
}

func isolateIntegrationsEnv(t *testing.T) {
	t.Helper()
	isolateServerRoleEnv(t)
	for _, name := range integrationsEnv {
		t.Setenv(name, "") // restores the original afterwards
		require.NoError(t, os.Unsetenv(name))
	}
}

func serverCmd(t *testing.T, role string) *cobra.Command {
	t.Helper()
	cmd, _, err := NewRootCmd().Find([]string{"server", role})
	require.NoError(t, err)
	return cmd
}

// TestIntegrationsEnvContract pins the names control-plane sets on the API
// server and the worker (PUBLIC_URL, RELIANT_VAULT_KEY,
// RELIANT_GITHUB_APP_SLUG, the webhook secrets) to the settings both roles
// hand their packages — the same in each, so they agree on which
// integrations deliver events.
func TestIntegrationsEnvContract(t *testing.T) {
	isolateIntegrationsEnv(t)
	for name, value := range map[string]string{
		"PUBLIC_URL":                        " https://reliant.example ",
		"RELIANT_VAULT_KEY":                 "v1:a2V5",
		"RELIANT_SLACK_SIGNING_SECRET":      "slack-secret",
		"RELIANT_GITHUB_WEBHOOK_SECRET":     "gh-secret",
		"RELIANT_GITHUB_APP_SLUG":           "reliant-labs",
		"RELIANT_GITHUB_BOT_USER_ID":        "42",
		"RELIANT_TEST_INTEGRATION_SECRET":   "test-secret",
		"RELIANT_OAUTH_GMAIL_CLIENT_ID":     " gmail-id ",
		"RELIANT_OAUTH_GMAIL_CLIENT_SECRET": "gmail-secret",
	} {
		t.Setenv(name, value)
	}

	var api serverapi.Options
	require.NoError(t, loadServerAPIConfig(serverCmd(t, "api"), &api))
	var worker serverworker.Options
	require.NoError(t, loadServerWorkerConfig(serverCmd(t, "worker"), &worker))

	for role, got := range map[string]integrationSettings{
		"api":    {api.PublicURL, api.VaultKey, api.Webhooks, api.GitHubAppSlug, api.GitHubBot},
		"worker": {worker.PublicURL, worker.VaultKey, worker.Webhooks, worker.GitHubAppSlug, worker.GitHubBot},
	} {
		assert.Equal(t, "https://reliant.example", got.publicURL, role)
		assert.Equal(t, "v1:a2V5", got.vaultKey, role)
		assert.Equal(t, "slack-secret", got.webhooks.SlackSigningSecret, role)
		assert.Equal(t, "gh-secret", got.webhooks.GitHubWebhookSecret, role)
		assert.Equal(t, "https://reliant.example", got.webhooks.PublicURL, role)
		assert.Equal(t, "test-secret", got.webhooks.TestIntegrationSecret, role)
		assert.Equal(t, "reliant-labs", got.gitHubAppSlug, role)
		assert.Equal(t, github.OurBot{ID: 42}, got.gitHubBot, role)
		require.NotNil(t, got.webhooks.OAuthClients, role)
		id, secret := got.webhooks.OAuthClients("gmail")
		assert.Equal(t, "gmail-id", id, role)
		assert.Equal(t, "gmail-secret", secret, role)
	}
}

// Unset, the bot is the production App's; a malformed id stops the process
// (it used to fall back to the default silently).
func TestIntegrationsGitHubBot(t *testing.T) {
	isolateIntegrationsEnv(t)
	var api serverapi.Options
	require.NoError(t, loadServerAPIConfig(serverCmd(t, "api"), &api))
	assert.Equal(t, github.BotFrom(0, ""), api.GitHubBot)

	t.Setenv("RELIANT_GITHUB_BOT_LOGIN", "acme-app[bot]")
	require.NoError(t, loadServerAPIConfig(serverCmd(t, "api"), &api))
	assert.Equal(t, github.OurBot{Login: "acme-app[bot]"}, api.GitHubBot)

	for _, bad := range []string{"not-a-number", "-5"} {
		t.Setenv("RELIANT_GITHUB_BOT_USER_ID", bad)
		err := loadServerAPIConfig(serverCmd(t, "api"), &api)
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "RELIANT_GITHUB_BOT_USER_ID")
	}
}
