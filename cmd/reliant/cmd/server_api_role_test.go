// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
	"github.com/reliant-labs/reliant/pkg/config"
)

// apiOptions is what `reliant server api` hands its root — the configs
// roots_gen.go loads, as the process policy (serverkitConfig) and the api's
// providers (internal/app) read them — laid out as the Options serverapi.Run
// took before the api became a root, so the role-surface golden shows the
// same settings landing in the same places, plus what the root added: the
// probe address and paths.
type apiOptions struct {
	GRPCPort    int
	PprofPort   int
	BindAddress string

	DatabaseDriver    string
	DatabaseURL       string
	DatabaseDirectURL string
	DatabasePool      db.Pool
	DataDir           string

	TemporalHost      string
	TemporalPort      int
	TemporalNamespace string

	NATS            natsutil.Endpoint
	StreamingDriver string

	HealthAddr     string
	LivenessPaths  []string
	ReadinessPaths []string

	PreStopDelay    time.Duration
	ShutdownTimeout time.Duration

	CORSAllowedOrigins  []string
	AllowedEmailDomains []string
	DisableForgeAPI     bool

	TLSCertFile string
	TLSKeyFile  string
	DisableTLS  bool

	JWTPublicKey     string
	JWTPublicKeyFile string
	JWKSURL          string

	AuthMode                  string
	AuthAPIKey                string
	RequireEmailVerifiedClaim bool

	AuthorizationServer string
	OAuthProvider       auth.Provider
	OAuthIssuers        []string
	MaxUploadSize       int64
	WorktreeAutoArchive worktreesweep.AutoArchiveMode

	Log                logging.Settings
	ControlPlane       controlplane.Endpoint
	AllowPrivateEgress bool
	PublicURL          string
	VaultKey           string
	Webhooks           webhook.RegistryConfig
	GitHubAppSlug      string
	GitHubBot          github.OurBot
	Observability      observability.Settings
	Analytics          analytics.Settings
	LLM                drivers.Settings

	PayloadGCHorizon time.Duration
	Preview          tools.PreviewSettings
}

// loadServerAPIConfig loads the api's configs as its command does — the root
// config, the `server` subtree configs and its own ApiServerConfig — and
// fills opts as its policy and providers read them, with the first error any
// of those would stop the process with, in the order the api root builds
// them (internal/app/roots_api.go). The JWT key file is named, not read: its
// contents are NewAPISettings'.
func loadServerAPIConfig(cmd *cobra.Command, opts *apiOptions) error {
	base, err := loadRootConfig(cmd)
	if err != nil {
		return err
	}
	process, err := config.LoadServerProcessConfig(cmd)
	if err != nil {
		return err
	}
	database, err := config.LoadDatabaseConfig(cmd)
	if err != nil {
		return err
	}
	api, err := config.LoadApiServerConfig(cmd)
	if err != nil {
		return err
	}
	server, err := serverkitConfig(base, rootConfigs{
		DatabaseConfig: database, ServerProcessConfig: process, ApiServerConfig: api,
	})
	if err != nil {
		return err
	}
	// What the api root's providers read; a value they cannot use was
	// refused by the loads above.
	in := app.Integrations(process.GetIntegrations())
	opts.LLM = app.LLMSettings(process.GetLlm())
	opts.Preview = app.PreviewSettings(process.GetPreview())
	opts.DatabasePool = app.DatabasePool(database)
	opts.TemporalHost, opts.TemporalPort, opts.TemporalNamespace = app.TemporalEndpoint(process.GetTemporal())
	opts.GRPCPort = int(api.GetGrpcPort())
	opts.PprofPort = int(api.GetPprofPort())
	opts.BindAddress = api.GetBindAddress()
	opts.DatabaseDriver, opts.DatabaseURL, opts.DatabaseDirectURL = app.DatabaseURLs(database)
	opts.DataDir = process.GetDataDir()
	opts.NATS = app.NATSEndpoint(process.GetNats())
	opts.StreamingDriver = process.GetStreamingDriver()
	opts.HealthAddr = server.Addr
	opts.LivenessPaths, opts.ReadinessPaths = server.Probes.Live, server.Probes.Ready
	opts.PreStopDelay, opts.ShutdownTimeout = server.PreStopDelay, server.ShutdownTimeout
	opts.CORSAllowedOrigins = api.GetCorsAllowedOrigins()
	opts.AllowedEmailDomains = api.GetAllowedEmailDomains()
	opts.DisableForgeAPI = api.GetDisableForgeApi()
	tls := process.GetTls()
	opts.TLSCertFile, opts.TLSKeyFile, opts.DisableTLS = tls.GetCertFile(), tls.GetKeyFile(), tls.GetDisable()
	opts.JWTPublicKey = api.GetJwtPublicKey()
	opts.JWTPublicKeyFile = api.GetJwtPublicKeyFile()
	opts.JWKSURL = api.GetJwksUrl()
	opts.AuthMode = api.GetAuthMode()
	opts.AuthAPIKey = api.GetAuthApiKey()
	opts.RequireEmailVerifiedClaim = api.GetRequireEmailVerifiedClaim()
	opts.AuthorizationServer = strings.TrimSpace(api.GetAuthorizationServer())
	opts.OAuthProvider = auth.Provider{URL: api.GetAuthProviderUrl(), Key: api.GetAuthProviderKey()}
	opts.OAuthIssuers = api.GetMcpOauthIssuers()
	opts.MaxUploadSize = api.GetMaxUploadSize()
	opts.WorktreeAutoArchive = worktreesweep.ParseAutoArchiveMode(api.GetWorktreeAutoArchive())
	opts.Log = logSettings(base)
	opts.ControlPlane = app.ControlPlaneEndpoint(process.GetControlPlane())
	opts.AllowPrivateEgress = app.AllowPrivateEgress(process.GetNetwork())
	opts.PublicURL, opts.VaultKey, opts.Webhooks = in.PublicURL, in.VaultKey, in.Webhooks
	opts.GitHubAppSlug, opts.GitHubBot = in.GitHubAppSlug, in.GitHubBot
	opts.Observability = observabilitySettings(process.GetObservability(), base)
	opts.Analytics = app.AnalyticsSettings(process.GetAnalytics())
	opts.PayloadGCHorizon = api.GetPayloadBlobGcHorizon().AsDuration()
	return nil
}
