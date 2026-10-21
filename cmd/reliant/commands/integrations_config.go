// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
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
// IntegrationsConfig, trimmed as every reader of these variables trimmed them.
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
			OAuthClients:          connections.OAuthClientsFromConfig(cfg.GetOauthClientIds(), cfg.GetOauthClientSecrets()),
			PublicURL:             publicURL,
			TestIntegrationSecret: cfg.GetTestIntegrationSecret(),
		},
		gitHubAppSlug: strings.TrimSpace(cfg.GetGithubAppSlug()),
		gitHubBot:     github.BotFrom(cfg.GetGithubBotUserId(), cfg.GetGithubBotLogin()),
	}, nil
}
