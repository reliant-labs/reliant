// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"fmt"
	"strings"
	"time"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/worktreesweep"

	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/serverapi"
	"github.com/reliant-labs/reliant/pkg/config"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/durationpb"
)

// mergeServerRoles completes the generated `server` command (roots_gen.go:
// the gateway and worker roots' parent, with ServerProcessConfig's and
// DatabaseConfig's flags persistent on it, and its help from
// internal/app/roots.go's //forge:group) with the role that is not a root
// yet, `server api`.
func mergeServerRoles(cmd *cobra.Command) {
	cmd.AddCommand(newServerAPICmd())
}

func newServerAPICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Run the stateless HTTP + gRPC API server",
		Long: `Starts the Reliant API server for cloud deployments. This is a stateless
server that handles HTTP REST and gRPC/ConnectRPC requests, connecting to
external Temporal, Postgres, and NATS services.

Designed to run as N replicas behind a load balancer.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var opts serverapi.Options
			if err := loadServerAPIConfig(cmd, &opts); err != nil {
				return err
			}
			return serverapi.Run(cmd.Context(), opts)
		},
	}

	// Every flag and variable `server api` reads: proto/config/v1
	// ApiServerConfig.
	config.RegisterApiServerConfigFlags(cmd)

	return cmd
}

// serverConfigs is what every server role loads before its own command
// config: the root config, and the `server` subtree configs.
type serverConfigs struct {
	base     *config.ReliantConfig
	process  *config.ServerProcessConfig
	database *config.DatabaseConfig
}

// loadServerConfigs loads the configs every server role shares.
func loadServerConfigs(cmd *cobra.Command) (serverConfigs, error) {
	base, err := loadRootConfig(cmd)
	if err != nil {
		return serverConfigs{}, err
	}
	process, err := config.LoadServerProcessConfig(cmd)
	if err != nil {
		return serverConfigs{}, err
	}
	database, err := config.LoadDatabaseConfig(cmd)
	if err != nil {
		return serverConfigs{}, err
	}
	return serverConfigs{base: base, process: process, database: database}, nil
}

// loadServerAPIConfig fills opts with what `server api` reads through typed
// config: the configs every role shares, and its own ApiServerConfig.
func loadServerAPIConfig(cmd *cobra.Command, opts *serverapi.Options) error {
	shared, err := loadServerConfigs(cmd)
	if err != nil {
		return err
	}
	base, process := shared.base, shared.process
	api, err := config.LoadApiServerConfig(cmd)
	if err != nil {
		return err
	}
	if err := app.CheckPorts(
		app.Port{Name: "GRPC_PORT (--grpc-port)", Port: api.GetGrpcPort()},
		app.Port{Name: "HEALTH_PORT (--health-port)", Port: api.GetHealthPort()},
		app.Port{Name: "TEMPORAL_PORT (--temporal-port)", Port: process.GetTemporal().GetPort()},
	); err != nil {
		return err
	}
	if err := app.CheckPprofPort(api.GetPprofPort()); err != nil {
		return err
	}
	opts.Log = logSettings(base)
	opts.GRPCPort = int(api.GetGrpcPort())
	opts.HealthPort = int(api.GetHealthPort())
	opts.PprofPort = int(api.GetPprofPort())
	opts.BindAddress = api.GetBindAddress()
	opts.DataDir = process.GetDataDir()
	opts.DatabaseDriver, opts.DatabaseURL, opts.DatabaseDirectURL = app.DatabaseURLs(shared.database)
	if opts.DatabasePool, err = app.DatabasePool(shared.database); err != nil {
		return err
	}
	opts.TemporalHost, opts.TemporalPort, opts.TemporalNamespace = app.TemporalEndpoint(process.GetTemporal())
	opts.NATS = app.NATSEndpoint(process.GetNats())
	opts.StreamingDriver = process.GetStreamingDriver()
	opts.CORSAllowedOrigins = api.GetCorsAllowedOrigins()
	opts.TLSCertFile, opts.TLSKeyFile, opts.DisableTLS = tlsSettings(process.GetTls())
	opts.PreStopDelay, opts.ShutdownTimeout = drainBudget(api)
	opts.JWTPublicKey = api.GetJwtPublicKey()
	opts.JWTPublicKeyFile = api.GetJwtPublicKeyFile()
	opts.JWKSURL = api.GetJwksUrl()
	opts.AllowedEmailDomains = api.GetAllowedEmailDomains()
	opts.DisableForgeAPI = api.GetDisableForgeApi()
	opts.ControlPlane = app.ControlPlaneEndpoint(process.GetControlPlane())
	opts.AuthMode = api.GetAuthMode()
	opts.AuthAPIKey = api.GetAuthApiKey()
	opts.RequireEmailVerifiedClaim = api.GetRequireEmailVerifiedClaim()
	opts.AuthorizationServer = strings.TrimSpace(api.GetAuthorizationServer())
	opts.OAuthProvider = auth.Provider{URL: api.GetAuthProviderUrl(), Key: api.GetAuthProviderKey()}
	opts.OAuthIssuers = api.GetMcpOauthIssuers()
	opts.MaxUploadSize = api.GetMaxUploadSize()
	opts.WorktreeAutoArchive = worktreesweep.ParseAutoArchiveMode(api.GetWorktreeAutoArchive())
	in, err := app.Integrations(process.GetIntegrations())
	if err != nil {
		return err
	}
	opts.PublicURL, opts.VaultKey, opts.Webhooks = in.PublicURL, in.VaultKey, in.Webhooks
	opts.GitHubAppSlug, opts.GitHubBot = in.GitHubAppSlug, in.GitHubBot
	opts.Analytics = app.AnalyticsSettings(process.GetAnalytics())
	if opts.PayloadGCHorizon = api.GetPayloadBlobGcHorizon().AsDuration(); opts.PayloadGCHorizon <= 0 {
		return fmt.Errorf("config: RELIANT_PAYLOAD_BLOB_GC_HORIZON = %s is not a positive duration", opts.PayloadGCHorizon)
	}
	opts.Observability = observabilitySettings(process.GetObservability(), base)
	if opts.Sentry, err = app.SentrySettings(process.GetSentry(), process.GetObservability()); err != nil {
		return err
	}
	if opts.LLM, err = app.LLMSettings(process.GetLlm()); err != nil {
		return err
	}
	if opts.Preview, err = app.PreviewSettings(process.GetPreview()); err != nil {
		return err
	}
	return validateServerAPIAuth(opts)
}

// validateServerAPIAuth refuses an auth configuration the API server cannot
// serve with, at load rather than when the first request arrives.
func validateServerAPIAuth(opts *serverapi.Options) error {
	if opts.AuthMode == auth.ModeAPIKey && opts.AuthAPIKey == "" {
		return fmt.Errorf("config: AUTH_MODE=apikey requires AUTH_API_KEY")
	}
	if opts.MaxUploadSize <= 0 {
		return fmt.Errorf("config: MAX_UPLOAD_SIZE = %d is not a positive byte count", opts.MaxUploadSize)
	}
	return nil
}

// tlsSettings is the shared ServerTLSConfig in the shape the server packages'
// Options take.
func tlsSettings(cfg *configv1.ServerTLSConfig) (certFile, keyFile string, disable bool) {
	return cfg.GetCertFile(), cfg.GetKeyFile(), cfg.GetDisable()
}

// roleDrain is a role config's rollout drain budget: each role declares its
// own PRE_STOP_DELAY and SHUTDOWN_TIMEOUT.
type roleDrain interface {
	GetPreStopDelay() *durationpb.Duration
	GetShutdownTimeout() *durationpb.Duration
}

// drainBudget is a role's drain budget (pre-stop delay, shutdown timeout) in
// the terms serverkit.Drainer takes it, under the rule forge's grace-period
// arithmetic applies to the same variables (pkg/deploy drainSeconds), so the
// two can never disagree about the budget: zero is serverkit's default
// (5s / 30s) and negative is none. A value that is not a duration has already
// stopped the server at load.
func drainBudget(cfg roleDrain) (preStopDelay, shutdownTimeout time.Duration) {
	return cfg.GetPreStopDelay().AsDuration(), cfg.GetShutdownTimeout().AsDuration()
}
