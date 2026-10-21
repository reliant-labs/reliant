// Copyright (c) 2025 Reliant Labs

package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
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

// The server configs (proto/config/v1 ServerProcessConfig, DatabaseConfig and
// each role's command config) in the shapes the packages they configure
// take. The providers read them, and so does `reliant server api`, whose
// hand-written command loads the same configs until it is a root too.

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
// database layer takes. A size or a wait that cannot be honoured is refused
// rather than quietly replaced by its default.
func DatabasePool(cfg *configv1.DatabaseConfig) (db.Pool, error) {
	if n := cfg.GetDbMaxOpenConns(); n < 1 {
		return db.Pool{}, fmt.Errorf("config: RELIANT_DB_MAX_OPEN_CONNS = %d must be at least 1", n)
	}
	if n := cfg.GetDbMaxIdleConns(); n < 1 {
		return db.Pool{}, fmt.Errorf("config: RELIANT_DB_MAX_IDLE_CONNS = %d must be at least 1", n)
	}
	if s := cfg.GetDbSchemaWaitSeconds(); s < 1 {
		return db.Pool{}, fmt.Errorf("config: RELIANT_DB_SCHEMA_WAIT_SECONDS = %d must be at least 1", s)
	}
	pool := db.Pool{
		MaxOpenConns: int(cfg.GetDbMaxOpenConns()),
		MaxIdleConns: int(cfg.GetDbMaxIdleConns()),
		SchemaWait:   time.Duration(cfg.GetDbSchemaWaitSeconds()) * time.Second,
	}
	if d := cfg.GetDbConnectTimeout(); d != nil {
		if pool.ConnectTimeout = d.AsDuration(); pool.ConnectTimeout <= 0 {
			return db.Pool{}, fmt.Errorf("config: DB_CONNECT_TIMEOUT = %s is not a positive duration", pool.ConnectTimeout)
		}
	}
	return pool, nil
}

// NATSEndpoint is the shared NatsConfig as natsutil connects to it.
func NATSEndpoint(cfg *configv1.NatsConfig) natsutil.Endpoint {
	return natsutil.Endpoint{URL: cfg.GetUrl(), User: cfg.GetUser(), Password: cfg.GetPassword()}
}

// Port is a port setting, named as an operator sets it.
type Port struct {
	Name string
	Port int32
}

// CheckPorts refuses a port setting that is not a TCP port: the loader has
// parsed it as an integer, but 0 or 70000 would fail later, at bind time, as
// something less clear.
func CheckPorts(ports ...Port) error {
	for _, p := range ports {
		if p.Port < 1 || p.Port > 65535 {
			return fmt.Errorf("config: %s = %d is not a TCP port (1-65535)", p.Name, p.Port)
		}
	}
	return nil
}

// CheckPprofPort is CheckPorts for PPROF_PORT, where 0 is a setting rather
// than a mistake: no diagnostics listener (debugserver serves none on port
// 0), as an empty PPROF_ADDR is for the gateway.
func CheckPprofPort(port int32) error {
	if port == 0 {
		return nil
	}
	return CheckPorts(Port{Name: "PPROF_PORT (--pprof-port)", Port: port})
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
func Integrations(cfg *configv1.IntegrationsConfig) (IntegrationSettings, error) {
	if id := cfg.GetGithubBotUserId(); id < 0 {
		return IntegrationSettings{}, fmt.Errorf("config: RELIANT_GITHUB_BOT_USER_ID = %d is not a GitHub user id", id)
	}
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
	}, nil
}

// LLMSettings is the shared LLMConfig in the shape the LLM driver layer
// takes. A stream deadline that is not positive is refused here: zero would
// silently take the default and a negative one would never fire, so either
// misreads what the operator asked for.
func LLMSettings(cfg *configv1.LLMConfig) (drivers.Settings, error) {
	var errs []error
	positive := func(name string, d *durationpb.Duration) time.Duration {
		if d == nil {
			return 0 // unset: the LLM layer's default (mid-block: derived)
		}
		v := d.AsDuration()
		if v <= 0 {
			errs = append(errs, fmt.Errorf("config: %s = %s is not a positive duration", name, v))
		}
		return v
	}
	timeouts := llm.StreamTimeouts{
		Idle:         positive("RELIANT_LLM_STREAM_IDLE_TIMEOUT", cfg.GetStreamIdleTimeout()),
		ContentStall: positive("RELIANT_LLM_STREAM_CONTENT_STALL_TIMEOUT", cfg.GetStreamContentStallTimeout()),
		Progress:     positive("RELIANT_LLM_STREAM_PROGRESS_TIMEOUT", cfg.GetStreamProgressTimeout()),
		MidBlock:     positive("RELIANT_LLM_STREAM_MID_BLOCK_STALL_TIMEOUT", cfg.GetStreamMidBlockStallTimeout()),
	}
	if err := errors.Join(errs...); err != nil {
		return drivers.Settings{}, err
	}
	return drivers.Settings{
		StreamTimeouts:            timeouts,
		ReliantBaseURL:            cfg.GetReliantApiBaseUrl(),
		VertexProject:             cfg.GetVertexaiProject(),
		VertexLocation:            cfg.GetVertexaiLocation(),
		DebugOpenAIResponsesInput: cfg.GetDebugOpenaiResponsesInput(),
		DebugOpenAIToolSchemas:    cfg.GetDebugOpenaiToolSchemas(),
	}, nil
}

// PreviewSettings is the shared PreviewConfig in the shape the tools package
// takes.
func PreviewSettings(cfg *configv1.PreviewConfig) (tools.PreviewSettings, error) {
	if p := cfg.GetWorkspaceUrlPort(); p < 0 || p > 65535 {
		return tools.PreviewSettings{}, fmt.Errorf("config: WORKSPACE_URL_PORT = %d is not a TCP port", p)
	}
	return tools.PreviewSettings{
		ProxyHost:           strings.TrimSpace(cfg.GetProxyHost()),
		WorkspaceBaseDomain: strings.TrimSpace(cfg.GetWorkspaceBaseDomain()),
		WorkspaceURLScheme:  cfg.GetWorkspaceUrlScheme(),
		WorkspaceURLPort:    int(cfg.GetWorkspaceUrlPort()),
	}, nil
}

// AnalyticsSettings is the shared AnalyticsConfig in the shape
// internal/analytics takes.
func AnalyticsSettings(cfg *configv1.AnalyticsConfig) analytics.Settings {
	return analytics.Settings{Disabled: cfg.GetDisabled()}
}

// SentrySettings is the shared SentryConfig in the shape internal/telemetry
// takes. Events are filed under the deployment environment the deploy states
// (SENTRY_ENVIRONMENT, else ENVIRONMENT).
func SentrySettings(cfg *configv1.SentryConfig, obs *configv1.ObservabilityConfig) (telemetry.SentryConfig, error) {
	rate := cfg.GetTracesSampleRate()
	if rate < 0 || rate > 1 {
		return telemetry.SentryConfig{}, fmt.Errorf("config: SENTRY_TRACES_SAMPLE_RATE = %v is not a share between 0 and 1", rate)
	}
	return telemetry.SentryConfig{
		Enabled:          cfg.GetEnabled(),
		DSN:              cfg.GetDsn(),
		Environment:      obs.GetEnvironment(),
		TracesSampleRate: rate,
	}, nil
}

// TemporalEndpoint is the shared TemporalConfig: host, port and namespace.
func TemporalEndpoint(cfg *configv1.TemporalConfig) (host string, port int, namespace string) {
	return cfg.GetHost(), int(cfg.GetPort()), cfg.GetNamespace()
}
