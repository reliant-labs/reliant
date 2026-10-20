// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"strings"
	"time"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/worktreesweep"

	"github.com/reliant-labs/reliant/internal/serverapi"
	"github.com/reliant-labs/reliant/internal/servergateway"
	"github.com/reliant-labs/reliant/internal/serverworker"
	"github.com/reliant-labs/reliant/pkg/config"
	"github.com/spf13/cobra"
)

func newServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run cloud server components",
		Long: `Run Reliant cloud server components. Each subcommand starts a specific
server role for split-deployment mode.`,
	}

	cmd.AddCommand(newServerAPICmd())
	cmd.AddCommand(newServerWorkerCmd())
	cmd.AddCommand(newServerGatewayCmd())

	return cmd
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

// loadServerAPIConfig fills opts with what `server api` reads through typed
// config: the root config, and its own ApiServerConfig.
func loadServerAPIConfig(cmd *cobra.Command, opts *serverapi.Options) error {
	base, err := loadRootConfig(cmd)
	if err != nil {
		return err
	}
	api, err := config.LoadApiServerConfig(cmd)
	if err != nil {
		return err
	}
	if err := config.ValidateApiServerConfig(api); err != nil {
		return err
	}
	if err := checkServerPorts(
		serverPort{"GRPC_PORT (--grpc-port)", api.GetGrpcPort()},
		serverPort{"HEALTH_PORT (--health-port)", api.GetHealthPort()},
		serverPort{"TEMPORAL_PORT (--temporal-port)", api.GetTemporal().GetPort()},
	); err != nil {
		return err
	}
	if err := checkPprofPort(api.GetPprofPort()); err != nil {
		return err
	}
	drain := drainBudget(api.GetDrain())
	opts.Log = logSettings(base)
	opts.GRPCPort = int(api.GetGrpcPort())
	opts.HealthPort = int(api.GetHealthPort())
	opts.PprofPort = int(api.GetPprofPort())
	opts.BindAddress = api.GetBindAddress()
	opts.DataDir = api.GetDataDir()
	opts.DatabaseDriver, opts.DatabaseURL, opts.DatabaseDirectURL = databaseSettings(api.GetDatabase())
	if opts.DatabasePool, err = databasePool(api.GetDatabase()); err != nil {
		return err
	}
	opts.TemporalHost, opts.TemporalPort, opts.TemporalNamespace = temporalSettings(api.GetTemporal())
	opts.NATS = natsEndpoint(api.GetNats())
	opts.StreamingDriver = api.GetStreamingDriver()
	opts.CORSAllowedOrigins = api.GetCorsAllowedOrigins()
	opts.TLSCertFile, opts.TLSKeyFile, opts.DisableTLS = tlsSettings(api.GetTls())
	opts.PreStopDelay, opts.ShutdownTimeout = drain.preStopDelay, drain.shutdownTimeout
	opts.JWTPublicKey = api.GetJwtPublicKey()
	opts.JWTPublicKeyFile = api.GetJwtPublicKeyFile()
	opts.JWKSURL = api.GetJwksUrl()
	opts.AllowedEmailDomains = api.GetAllowedEmailDomains()
	opts.DisableForgeAPI = api.GetDisableForgeApi()
	opts.ControlPlane = controlPlaneEndpoint(api.GetControlPlane())
	opts.AuthMode = api.GetAuthMode()
	opts.AuthAPIKey = api.GetAuthApiKey()
	opts.RequireEmailVerifiedClaim = api.GetRequireEmailVerifiedClaim()
	opts.AuthorizationServer = strings.TrimSpace(api.GetAuthorizationServer())
	opts.OAuthIssuers = api.GetMcpOauthIssuers()
	opts.MaxUploadSize = api.GetMaxUploadSize()
	opts.WorktreeAutoArchive = worktreesweep.ParseAutoArchiveMode(api.GetWorktreeAutoArchive())
	in, err := integrations(api.GetIntegrations())
	if err != nil {
		return err
	}
	opts.PublicURL, opts.VaultKey, opts.Webhooks = in.publicURL, in.vaultKey, in.webhooks
	opts.GitHubAppSlug, opts.GitHubBot = in.gitHubAppSlug, in.gitHubBot
	opts.Analytics = analyticsSettings(api.GetAnalytics())
	if opts.PayloadGCHorizon = api.GetPayloadBlobGcHorizon().AsDuration(); opts.PayloadGCHorizon <= 0 {
		return fmt.Errorf("config: RELIANT_PAYLOAD_BLOB_GC_HORIZON = %s is not a positive duration", opts.PayloadGCHorizon)
	}
	opts.Observability = observabilitySettings(api.GetObservability(), base)
	if opts.Sentry, err = sentrySettings(api.GetSentry(), api.GetObservability()); err != nil {
		return err
	}
	if opts.LLM, err = llmSettings(api.GetLlm()); err != nil {
		return err
	}
	if opts.Preview, err = previewSettings(api.GetPreview()); err != nil {
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

// loadServerWorkerConfig fills opts with what `server worker` reads through
// typed config: the root config, and its own WorkerConfig.
func loadServerWorkerConfig(cmd *cobra.Command, opts *serverworker.Options) error {
	base, err := loadRootConfig(cmd)
	if err != nil {
		return err
	}
	worker, err := config.LoadWorkerConfig(cmd)
	if err != nil {
		return err
	}
	if err := config.ValidateWorkerConfig(worker); err != nil {
		return err
	}
	if err := checkServerPorts(
		serverPort{"HEALTH_PORT (--health-port)", worker.GetHealthPort()},
		serverPort{"TEMPORAL_PORT (--temporal-port)", worker.GetTemporal().GetPort()},
	); err != nil {
		return err
	}
	if err := checkPprofPort(worker.GetPprofPort()); err != nil {
		return err
	}
	drain := drainBudget(worker.GetDrain())
	opts.Log = logSettings(base)
	opts.HealthPort = int(worker.GetHealthPort())
	opts.PprofPort = int(worker.GetPprofPort())
	opts.DataDir = worker.GetDataDir()
	opts.DatabaseDriver, opts.DatabaseURL, opts.DatabaseDirectURL = databaseSettings(worker.GetDatabase())
	if opts.DatabasePool, err = databasePool(worker.GetDatabase()); err != nil {
		return err
	}
	opts.TemporalHost, opts.TemporalPort, opts.TemporalNamespace = temporalSettings(worker.GetTemporal())
	opts.NATS = natsEndpoint(worker.GetNats())
	opts.StreamingDriver = worker.GetStreamingDriver()
	opts.PreStopDelay, opts.ShutdownTimeout = drain.preStopDelay, drain.shutdownTimeout
	opts.ControlPlane = controlPlaneEndpoint(worker.GetControlPlane())
	in, err := integrations(worker.GetIntegrations())
	if err != nil {
		return err
	}
	opts.PublicURL, opts.VaultKey, opts.Webhooks = in.publicURL, in.vaultKey, in.webhooks
	opts.GitHubAppSlug, opts.GitHubBot = in.gitHubAppSlug, in.gitHubBot
	opts.Analytics = analyticsSettings(worker.GetAnalytics())
	opts.Observability = observabilitySettings(worker.GetObservability(), base)
	if opts.Sentry, err = sentrySettings(worker.GetSentry(), worker.GetObservability()); err != nil {
		return err
	}
	if opts.LLM, err = llmSettings(worker.GetLlm()); err != nil {
		return err
	}
	opts.Preview, err = previewSettings(worker.GetPreview())
	return err
}

// loadServerGatewayConfig fills opts with what `server gateway` reads through
// typed config: the root config, and its own GatewayConfig.
func loadServerGatewayConfig(cmd *cobra.Command, opts *servergateway.Options) error {
	base, err := loadRootConfig(cmd)
	if err != nil {
		return err
	}
	gateway, err := config.LoadGatewayConfig(cmd)
	if err != nil {
		return err
	}
	if err := config.ValidateGatewayConfig(gateway); err != nil {
		return err
	}
	if err := checkServerPorts(
		serverPort{"TOOLS_DAEMON_PORT (--daemon-port)", gateway.GetDaemonPort()},
		serverPort{"HEALTH_PORT (--health-port)", gateway.GetHealthPort()},
	); err != nil {
		return err
	}
	drain := drainBudget(gateway.GetDrain())
	opts.Log = logSettings(base)
	opts.ToolsDaemonPort = int(gateway.GetDaemonPort())
	opts.HealthPort = int(gateway.GetHealthPort())
	opts.BindAddress = gateway.GetBindAddress()
	opts.DataDir = gateway.GetDataDir()
	opts.PprofAddr = gateway.GetPprofAddr()
	opts.DatabaseDriver, opts.DatabaseURL, opts.DatabaseDirectURL = databaseSettings(gateway.GetDatabase())
	if opts.DatabasePool, err = databasePool(gateway.GetDatabase()); err != nil {
		return err
	}
	opts.NATS = natsEndpoint(gateway.GetNats())
	opts.TLSCertFile, opts.TLSKeyFile, opts.DisableTLS = tlsSettings(gateway.GetTls())
	opts.PreStopDelay, opts.ShutdownTimeout = drain.preStopDelay, drain.shutdownTimeout
	opts.ControlPlane = controlPlaneEndpoint(gateway.GetControlPlane())
	opts.Observability = observabilitySettings(gateway.GetObservability(), base)
	return nil
}

// controlPlaneEndpoint is a role config's ControlPlaneConfig in the shape the
// server packages take, trimmed as every reader of these variables trimmed
// them.
func controlPlaneEndpoint(cfg *configv1.ControlPlaneConfig) controlplane.Endpoint {
	return controlplane.Endpoint{
		URL:                   strings.TrimSpace(cfg.GetUrl()),
		InternalServiceSecret: strings.TrimSpace(cfg.GetInternalServiceSecret()),
	}
}

// databaseSettings is a role's DatabaseConfig in the shape the server
// packages' Options take: driver, URL, direct (session) URL. The URLs are
// trimmed, as the database layer trimmed them.
func databaseSettings(cfg *configv1.DatabaseConfig) (driver, url, directURL string) {
	return cfg.GetDriver(), strings.TrimSpace(cfg.GetUrl()), strings.TrimSpace(cfg.GetDirectUrl())
}

// databasePool is a role's DatabaseConfig pool and wait settings in the shape
// the database layer takes. A size or a wait that cannot be honoured is
// refused rather than quietly replaced by its default.
func databasePool(cfg *configv1.DatabaseConfig) (db.Pool, error) {
	if n := cfg.GetMaxOpenConns(); n < 1 {
		return db.Pool{}, fmt.Errorf("config: RELIANT_DB_MAX_OPEN_CONNS = %d must be at least 1", n)
	}
	if n := cfg.GetMaxIdleConns(); n < 1 {
		return db.Pool{}, fmt.Errorf("config: RELIANT_DB_MAX_IDLE_CONNS = %d must be at least 1", n)
	}
	if s := cfg.GetSchemaWaitSeconds(); s < 1 {
		return db.Pool{}, fmt.Errorf("config: RELIANT_DB_SCHEMA_WAIT_SECONDS = %d must be at least 1", s)
	}
	pool := db.Pool{
		MaxOpenConns: int(cfg.GetMaxOpenConns()),
		MaxIdleConns: int(cfg.GetMaxIdleConns()),
		SchemaWait:   time.Duration(cfg.GetSchemaWaitSeconds()) * time.Second,
	}
	if d := cfg.GetConnectTimeout(); d != nil {
		if pool.ConnectTimeout = d.AsDuration(); pool.ConnectTimeout <= 0 {
			return db.Pool{}, fmt.Errorf("config: DB_CONNECT_TIMEOUT = %s is not a positive duration", pool.ConnectTimeout)
		}
	}
	return pool, nil
}

// temporalSettings is a role's TemporalConfig in the shape the server
// packages' Options take.
func temporalSettings(cfg *configv1.TemporalConfig) (host string, port int, namespace string) {
	return cfg.GetHost(), int(cfg.GetPort()), cfg.GetNamespace()
}

// tlsSettings is a role's ServerTLSConfig in the shape the server packages'
// Options take.
func tlsSettings(cfg *configv1.ServerTLSConfig) (certFile, keyFile string, disable bool) {
	return cfg.GetCertFile(), cfg.GetKeyFile(), cfg.GetDisable()
}

// natsEndpoint is a role's NatsConfig as natsutil connects to it.
func natsEndpoint(cfg *configv1.NatsConfig) natsutil.Endpoint {
	return natsutil.Endpoint{URL: cfg.GetUrl(), User: cfg.GetUser(), Password: cfg.GetPassword()}
}

// serverDrain is a role's rollout drain budget in the terms serverkit.Drainer
// takes it.
type serverDrain struct {
	preStopDelay    time.Duration
	shutdownTimeout time.Duration
}

// drainBudget is a role's DrainConfig (PRE_STOP_DELAY, SHUTDOWN_TIMEOUT) in
// the terms serverkit.Drainer takes it, under the rule forge's grace-period
// arithmetic applies to the same variables (pkg/deploy drainSeconds), so the
// two can never disagree about the budget: zero is serverkit's default
// (5s / 30s) and negative is none. A value that is not a duration has already
// stopped the server at load.
func drainBudget(cfg *configv1.DrainConfig) serverDrain {
	return serverDrain{
		preStopDelay:    cfg.GetPreStopDelay().AsDuration(),
		shutdownTimeout: cfg.GetShutdownTimeout().AsDuration(),
	}
}

// serverPort is a port setting, named as an operator sets it.
type serverPort struct {
	name string
	port int32
}

// checkServerPorts refuses a port setting that is not a TCP port: the loader
// has parsed it as an integer, but 0 or 70000 would fail later, at bind time,
// as something less clear.
func checkServerPorts(ports ...serverPort) error {
	for _, p := range ports {
		if p.port < 1 || p.port > 65535 {
			return fmt.Errorf("config: %s = %d is not a TCP port (1-65535)", p.name, p.port)
		}
	}
	return nil
}

// checkPprofPort is checkServerPorts for PPROF_PORT, where 0 is a setting
// rather than a mistake: no diagnostics listener (debugserver.Start serves
// none on port 0), as an empty PPROF_ADDR is for the gateway.
func checkPprofPort(port int32) error {
	if port == 0 {
		return nil
	}
	return checkServerPorts(serverPort{"PPROF_PORT (--pprof-port)", port})
}

func newServerWorkerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run the Temporal workflow worker",
		Long: `Starts a Temporal worker that processes workflow executions. Connects to
an external Temporal server and executes workflow activities (LLM inference,
tool execution routing, etc.).

Designed to run as N replicas for horizontal scaling.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var opts serverworker.Options
			if err := loadServerWorkerConfig(cmd, &opts); err != nil {
				return err
			}
			return serverworker.Run(cmd.Context(), opts)
		},
	}

	// Every flag and variable `server worker` reads: proto/config/v1
	// WorkerConfig.
	config.RegisterWorkerConfigFlags(cmd)

	return cmd
}

func newServerGatewayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Run the daemon connection gateway",
		Long: `Starts the daemon gateway that manages bidirectional gRPC streams to
tools-daemon processes. Routes tool execution requests from API servers
and Temporal workers to the correct daemon via NATS.

Designed to run as few stateful replicas.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var opts servergateway.Options
			if err := loadServerGatewayConfig(cmd, &opts); err != nil {
				return err
			}
			return servergateway.Run(cmd.Context(), opts)
		},
	}

	// Every flag and variable `server gateway` reads: proto/config/v1
	// GatewayConfig.
	config.RegisterGatewayConfigFlags(cmd)

	return cmd
}
