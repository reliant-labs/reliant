// Copyright (c) 2025 Reliant Labs

package app

import (
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// The server configs (proto/reliant/config/v1 ServerProcessConfig, DatabaseConfig and
// each role's command config) in the shapes the packages they configure
// take. The providers read them. A value no process can run with is refused
// by the config protos' buf.validate rules as the config loads, before the
// process does anything (proto/reliant/config/v1/config.proto), so none is checked
// here.

// ControlPlaneEndpoint is the shared ControlPlaneConfig in the shape the
// server packages take, trimmed as every reader of these variables trimmed
// them. The zero value is self-hosted.
func ControlPlaneEndpoint(cfg *configv1.ControlPlaneConfig) controlplane.Endpoint {
	return controlplane.Endpoint{
		URL:                   strings.TrimSpace(cfg.GetUrl()),
		InternalServiceSecret: strings.TrimSpace(cfg.GetInternalServiceSecret()),
	}
}

// DatabaseURLs is the DatabaseConfig's driver, URL and direct (session) URL.
// The URLs are trimmed, as the database layer trimmed them.
func DatabaseURLs(cfg *configv1.DatabaseConfig) (driver, url, directURL string) {
	return cfg.GetDatabaseDriver(), strings.TrimSpace(cfg.GetDatabaseUrl()), strings.TrimSpace(cfg.GetDatabaseDirectUrl())
}

// DatabasePool is the DatabaseConfig pool and wait settings in the shape the
// database layer takes. A size or a wait that cannot be honoured never gets
// here: db_config.proto's rules refuse it as the config loads.
func DatabasePool(cfg *configv1.DatabaseConfig) db.Pool {
	pool := db.Pool{
		MaxOpenConns: int(cfg.GetDbMaxOpenConns()),
		MaxIdleConns: int(cfg.GetDbMaxIdleConns()),
		SchemaWait:   time.Duration(cfg.GetDbSchemaWaitSeconds()) * time.Second,
	}
	if d := cfg.GetDbConnectTimeout(); d != nil {
		pool.ConnectTimeout = d.AsDuration()
	}
	return pool
}

// NATSEndpoint is the shared NatsConfig as natsutil connects to it.
func NATSEndpoint(cfg *configv1.NatsConfig) natsutil.Endpoint {
	return natsutil.Endpoint{URL: cfg.GetUrl(), User: cfg.GetUser(), Password: cfg.GetPassword()}
}

// IntegrationSettings is the shared IntegrationsConfig in the shape the
// server packages take.
type IntegrationSettings struct {
	// PublicURL is this deployment's externally reachable base URL
	// (PUBLIC_URL); empty reports webhook URLs as bare paths.
	PublicURL string
	// VaultKey is the credential vault's key (RELIANT_VAULT_KEY); see
	// vault.Boot.
	VaultKey string
	// Webhooks decides which integrations deliver events here, with the
	// deployment's OAuth clients, which the connections registry reads too.
	Webhooks webhook.RegistryConfig
	// GitHubAppSlug is the GitHub App Reliant's own writes appear as
	// (RELIANT_GITHUB_APP_SLUG); empty attributes no event to the App.
	GitHubAppSlug string
	// GitHubBot is that App's bot as GitHub names it in a webhook's sender.
	GitHubBot github.OurBot
}

// Integrations is the shared IntegrationsConfig, trimmed as every reader of
// these variables trimmed them.
func Integrations(cfg *configv1.IntegrationsConfig) IntegrationSettings {
	publicURL := strings.TrimSpace(cfg.GetPublicUrl())
	return IntegrationSettings{
		PublicURL: publicURL,
		VaultKey:  strings.TrimSpace(cfg.GetVaultKey()),
		Webhooks: webhook.RegistryConfig{
			SlackSigningSecret:    cfg.GetSlackSigningSecret(),
			GitHubWebhookSecret:   cfg.GetGithubWebhookSecret(),
			OAuthClients:          connections.OAuthClientsFromConfig(cfg.GetOauthClientIds(), cfg.GetOauthClientSecrets()),
			PublicURL:             publicURL,
			TestIntegrationSecret: cfg.GetTestIntegrationSecret(),
		},
		GitHubAppSlug: strings.TrimSpace(cfg.GetGithubAppSlug()),
		GitHubBot:     github.BotFrom(cfg.GetGithubBotUserId(), cfg.GetGithubBotLogin()),
	}
}

// LLMSettings is the shared LLMConfig in the shape the LLM driver layer
// takes. An unset stream deadline is the LLM layer's default (mid-block:
// derived); llm_config.proto's rules refuse one that is not positive as the
// config loads, since zero would silently take the default and a negative
// one would never fire.
func LLMSettings(cfg *configv1.LLMConfig) drivers.Settings {
	timeout := func(d *durationpb.Duration) time.Duration {
		if d == nil {
			return 0
		}
		return d.AsDuration()
	}
	return drivers.Settings{
		StreamTimeouts: llm.StreamTimeouts{
			Idle:         timeout(cfg.GetStreamIdleTimeout()),
			ContentStall: timeout(cfg.GetStreamContentStallTimeout()),
			Progress:     timeout(cfg.GetStreamProgressTimeout()),
			MidBlock:     timeout(cfg.GetStreamMidBlockStallTimeout()),
		},
		ReliantBaseURL:            cfg.GetReliantApiBaseUrl(),
		VertexProject:             cfg.GetVertexaiProject(),
		VertexLocation:            cfg.GetVertexaiLocation(),
		DebugOpenAIResponsesInput: cfg.GetDebugOpenaiResponsesInput(),
		DebugOpenAIToolSchemas:    cfg.GetDebugOpenaiToolSchemas(),
	}
}

// PreviewSettings is the shared PreviewConfig in the shape the tools package
// takes.
func PreviewSettings(cfg *configv1.PreviewConfig) tools.PreviewSettings {
	return tools.PreviewSettings{
		ProxyHost:           strings.TrimSpace(cfg.GetProxyHost()),
		WorkspaceBaseDomain: strings.TrimSpace(cfg.GetWorkspaceBaseDomain()),
		WorkspaceURLScheme:  cfg.GetWorkspaceUrlScheme(),
		WorkspaceURLPort:    int(cfg.GetWorkspaceUrlPort()),
	}
}

// AnalyticsSettings is the shared AnalyticsConfig in the shape
// internal/analytics takes.
func AnalyticsSettings(cfg *configv1.AnalyticsConfig) analytics.Settings {
	return analytics.Settings{Disabled: cfg.GetDisabled()}
}

// SentrySettings is the shared SentryConfig in the shape internal/telemetry
// takes. Events are filed under the deployment environment the deploy states
// (SENTRY_ENVIRONMENT, else ENVIRONMENT).
func SentrySettings(cfg *configv1.SentryConfig, obs *configv1.ObservabilityConfig) telemetry.SentryConfig {
	return telemetry.SentryConfig{
		Enabled:          cfg.GetEnabled(),
		DSN:              cfg.GetDsn(),
		Environment:      obs.GetEnvironment(),
		TracesSampleRate: cfg.GetTracesSampleRate(),
	}
}

// TemporalEndpoint is the shared TemporalConfig: host, port and namespace.
func TemporalEndpoint(cfg *configv1.TemporalConfig) (host string, port int, namespace string) {
	return cfg.GetHost(), int(cfg.GetPort()), cfg.GetNamespace()
}
