// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"os"
	"strings"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
)

// integrationSettings is a server role's IntegrationsConfig in the shape the
// server packages' Options take.
type integrationSettings struct {
	publicURL     string
	vaultKey      string
	webhooks      webhook.RegistryConfig
	gitHubAppSlug string
	gitHubBot     github.OurBot
}

// integrations builds a role's integration settings from its
// IntegrationsConfig plus this process's integration OAuth clients, trimmed as
// every reader of these variables trimmed them.
func integrations(cfg *configv1.IntegrationsConfig) (integrationSettings, error) {
	if id := cfg.GetGithubBotUserId(); id < 0 {
		return integrationSettings{}, fmt.Errorf("config: RELIANT_GITHUB_BOT_USER_ID = %d is not a GitHub user id", id)
	}
	publicURL := strings.TrimSpace(cfg.GetPublicUrl())
	return integrationSettings{
		publicURL: publicURL,
		vaultKey:  strings.TrimSpace(cfg.GetVaultKey()),
		webhooks: webhook.RegistryConfig{
			SlackSigningSecret:    cfg.GetSlackSigningSecret(),
			GitHubWebhookSecret:   cfg.GetGithubWebhookSecret(),
			OAuthClients:          processOAuthClients(),
			PublicURL:             publicURL,
			TestIntegrationSecret: cfg.GetTestIntegrationSecret(),
		},
		gitHubAppSlug: strings.TrimSpace(cfg.GetGithubAppSlug()),
		gitHubBot:     github.BotFrom(cfg.GetGithubBotUserId(), cfg.GetGithubBotLogin()),
	}, nil
}

// processOAuthClients is the deployment's integration OAuth clients,
// RELIANT_OAUTH_<ID>_CLIENT_ID / _CLIENT_SECRET. The family is keyed by the
// integration catalog — an open set of names no config message can declare
// field by field, and forge config has no prefix-keyed map — so this one
// lookup over the process environment is the entrypoint's to build and hand
// down.
func processOAuthClients() connections.OAuthClients {
	return connections.OAuthClientsFromEnv(os.Getenv) //nolint:forbidigo // open RELIANT_OAUTH_<ID>_* family keyed by the integration catalog; forge config has no prefix-keyed map
}
