// Copyright (c) 2025 Reliant Labs
//
// internal/app/providers.go — YOURS. forge writes it once and never again.
//
// The infrastructure every server root shares, as providers: each func shaped
// `func New<X>(<X>Deps) (T, error)` is a node of the composition graph. forge
// resolves each <X>Deps field by type (another node, or one of the graph's
// inputs: g.Ctx, g.Log, g.ServerCfg, g.Probes, and the configs the root's
// command loads — g.ServerProcessConfig, g.DatabaseConfig, g.GatewayConfig,
// g.WorkerConfig),
// and compose.go holds the value expressions. Nothing here opens anything
// eagerly: a process builds only what its root reaches.
//
// A provider whose value has Run, Drain, Close or Ready (func(context.Context)
// error) is supervised: started after everything it depends on, drained and
// closed after everything that depends on it, and its Ready joins /readyz. So
// the wrapper types below exist for their lifecycle methods: *db.Repo's
// Close() and *nats.Conn's Close() take no context and report readiness
// through methods of other names.

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"go.temporal.io/sdk/client"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/certs"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/debugserver"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	"github.com/reliant-labs/reliant/internal/temporal/stopnudge"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/vault"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	wfruntime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/threadcancel"
	"github.com/reliant-labs/reliant/pkg/config"
)

// DBRepo is the database's LIFECYCLE: it opens the Repository over the
// serving pool (and the session pool, when DATABASE_DIRECT_URL names one),
// gates /readyz on it, and closes it after every node that reads it. Nothing
// but Database reads it: every consumer takes the Repository itself.
type DBRepo struct{ Repo *db.Repo }

// DBRepoDeps is the database config and the data directory.
type DBRepoDeps struct {
	Database *config.DatabaseConfig
	DataDir  string
}

// NewDBRepo opens the database, retrying the first connect for up to
// DB_CONNECT_TIMEOUT, and waits for the schema the migrating process (the
// api-server, or `reliant db migrate up`) applies: a server role never races
// it (db.MigrationPolicy).
func NewDBRepo(d DBRepoDeps) (*DBRepo, error) {
	driver, url, directURL := DatabaseURLs(d.Database)
	if url == "" {
		return nil, errors.New("config: DATABASE_URL is required")
	}
	dbDriver, err := db.ParseDatabaseDriver(driver)
	if err != nil {
		return nil, fmt.Errorf("invalid DATABASE_DRIVER %q: %w", driver, err)
	}
	repo, err := db.NewRepoFromConfig(db.DatabaseConfig{
		Driver:    dbDriver,
		DataDir:   d.DataDir,
		URL:       url,
		DirectURL: directURL,
		Pool:      DatabasePool(d.Database),
		Migrate:   db.MigrateWait,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	logging.Info("Database initialized", "driver", driver)
	return &DBRepo{Repo: repo}, nil
}

// Ready pings the database: /readyz fails while it is unreachable.
func (r *DBRepo) Ready(ctx context.Context) error { return r.Repo.Ping(ctx) }

// Close closes the pools, after every node that reads them has stopped.
func (r *DBRepo) Close(context.Context) error {
	logging.Info("Closing database")
	return r.Repo.Close()
}

// DatabaseDeps is the lifecycle node that owns the pools.
type DatabaseDeps struct{ Pool *DBRepo }

// NewDatabase is the database's VALUE: the persistence API every consumer's
// role interface is filled with. It is the only node producing *db.Repo, so
// forge fills a persistence field it resolves by type with g.Database(), and
// TestPersistenceRolesAreTheDatabase holds every hand-written one to that.
// Reaching it reaches DBRepo, so whatever reads the database still stops
// before the pools close.
func NewDatabase(d DatabaseDeps) (*db.Repo, error) { return d.Pool.Repo, nil }

// NATSConn is the process's NATS connection.
type NATSConn struct{ Conn *nats.Conn }

// NATSConnDeps is the NATS server, and the boot context the first connect
// waits under.
type NATSConnDeps struct {
	Ctx  context.Context
	NATS *configv1.NatsConfig
}

// NewNATSConn connects to NATS. A server that is not up YET is a wait, not a
// crash: the first connect retries with backoff (serverkit.RetryConnect), and
// /readyz names "nats" as pending meanwhile. Once connected, the client
// reconnects for ever (natsutil.Connect).
func NewNATSConn(d NATSConnDeps) (*NATSConn, error) {
	ep := NATSEndpoint(d.NATS)
	if ep.URL == "" {
		return nil, errors.New("config: NATS_URL is required")
	}
	var nc *nats.Conn
	err := serverkit.RetryConnect(d.Ctx, serverkit.ConnectPolicy{Name: "nats", Logger: slog.Default()}, func(context.Context) error {
		c, err := natsutil.Connect(ep)
		if err != nil {
			return err
		}
		nc = c
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}
	logging.Info("Connected to NATS", "url", ep.URL)
	return &NATSConn{Conn: nc}, nil
}

// Ready fails while the connection is down (the client is reconnecting).
func (n *NATSConn) Ready(context.Context) error {
	if !n.Conn.IsConnected() {
		return errors.New("disconnected")
	}
	return nil
}

// Close closes the connection, after every node that publishes on it.
func (n *NATSConn) Close(context.Context) error {
	n.Conn.Close()
	return nil
}

// JetStreamDeps is the connection JetStream runs over.
type JetStreamDeps struct {
	NATS *NATSConn
}

// NewJetStream is the JetStream context over the process's NATS connection.
func NewJetStream(d JetStreamDeps) (jetstream.JetStream, error) {
	js, err := jetstream.New(d.NATS.Conn)
	if err != nil {
		return nil, fmt.Errorf("failed to create JetStream context: %w", err)
	}
	return js, nil
}

// UserUpdateHub is the user-updates hub (ephemeral, never-persisted updates
// to a user's web clients) on NATS subject user.updates, the one the
// api-server's StreamUserUpdates subscribes to.
type UserUpdateHub struct {
	*streaming.NATSUpdateHub[db.UserUpdate]
}

// UserUpdateHubDeps is the connection the hub publishes on.
type UserUpdateHubDeps struct {
	NATS *NATSConn
}

// NewUserUpdateHub builds the user-updates hub.
func NewUserUpdateHub(d UserUpdateHubDeps) (*UserUpdateHub, error) {
	return &UserUpdateHub{streaming.NewNATSUpdateHub[db.UserUpdate](d.NATS.Conn, "user.updates", "UserUpdate")}, nil
}

// Close closes the hub, before the connection it publishes on.
func (h *UserUpdateHub) Close(context.Context) error { return h.NATSUpdateHub.Close() }

// TokenAuthorityDeps is what picks the deployment's token authority: the
// control plane, when one is configured, else reliant's own store.
type TokenAuthorityDeps struct {
	ControlPlane *configv1.ControlPlaneConfig
	DB           *db.Repo
}

// NewTokenAuthority is the ONE machine-credential authority (`rlat_`):
// control-plane's Introspect when hosted, reliant's own store when
// self-hosted (tokenauthority). Uncached here: a revoked credential is
// refused on its next check.
func NewTokenAuthority(d TokenAuthorityDeps) (tokenauthority.Authority, error) {
	authority, mode, err := tokenauthority.New(tokenauthority.Deps{ControlPlane: d.ControlPlane, DB: d.DB.ORM()})
	if err != nil {
		return nil, fmt.Errorf("token authority: %w", err)
	}
	logging.Info("token authority initialized", "authority", mode)
	return authority, nil
}

// TLSFiles is the certificate and key a listener serves TLS with; both empty
// serve plaintext (h2c).
type TLSFiles struct {
	CertFile string
	KeyFile  string
}

// TLSFilesDeps is the TLS config and the data directory self-signed
// certificates are kept under.
type TLSFilesDeps struct {
	TLS     *configv1.ServerTLSConfig
	DataDir string
}

// NewTLSFiles is the configured certificate and key, a self-signed pair under
// <data_dir>/certs when none is configured, or none at all with DISABLE_TLS.
func NewTLSFiles(d TLSFilesDeps) (*TLSFiles, error) {
	if d.TLS.GetDisable() {
		logging.Info("TLS disabled via DISABLE_TLS=true")
		return &TLSFiles{}, nil
	}
	files := &TLSFiles{CertFile: d.TLS.GetCertFile(), KeyFile: d.TLS.GetKeyFile()}
	if files.CertFile == "" || files.KeyFile == "" {
		paths, err := certs.EnsureCerts(filepath.Join(d.DataDir, "certs"))
		if err != nil {
			return nil, fmt.Errorf("failed to ensure TLS certificates: %w", err)
		}
		files.CertFile, files.KeyFile = paths.CertFile, paths.KeyFile
	}
	logging.Info("TLS enabled", "cert", files.CertFile)
	return files, nil
}

// PprofDeps is the pprof endpoint's address (serverkit.Config.PprofAddr).
type PprofDeps struct {
	ServerCfg serverkit.Config
	Logger    *slog.Logger
}

// NewPprof is net/http/pprof on a listener of its own, never a port the
// process serves traffic on. An empty address serves nothing; a bind failure
// is logged and skipped, never fatal (serverkit.Pprof). It is off unless the
// role's config names an address: it serves a debug surface with no auth.
func NewPprof(d PprofDeps) (*serverkit.Pprof, error) {
	return serverkit.NewPprof(d.ServerCfg, d.Logger), nil
}

// DebugServer is the localhost-only diagnostics listener (debugserver: pprof,
// the DB write-queue gauges, metrics) as a node: it serves until the stop
// walk reaches it.
type DebugServer struct {
	addr string
	srv  *http.Server
}

// DebugServerDeps is the diagnostics port (PPROF_PORT).
type DebugServerDeps struct {
	Port int32
}

// NewDebugServer is the listener on 127.0.0.1:PPROF_PORT. PPROF_PORT=0 serves
// none: the node is absent.
func NewDebugServer(d DebugServerDeps) (*DebugServer, error) {
	if d.Port == 0 {
		logging.Info("Debug server (pprof) disabled", "pprof_port", 0)
		return nil, nil
	}
	addr := debugserver.Addr(int(d.Port))
	return &DebugServer{addr: addr, srv: &http.Server{Addr: addr, Handler: debugserver.Handler(), ReadHeaderTimeout: 10 * time.Second}}, nil
}

// Run serves until ctx ends. A bind failure is logged, not returned: a
// process without a profiler still does its job, and failing it over one
// would be the wrong trade.
func (s *DebugServer) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		logging.ErrorContext(ctx, "debug server failed", "address", s.addr, "error", err)
		<-ctx.Done()
		return nil
	}
	logging.InfoContext(ctx, "Starting debug server (pprof)", "address", s.addr)
	stop := context.AfterFunc(ctx, func() { _ = s.srv.Close() })
	defer stop()
	if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logging.ErrorContext(ctx, "debug server failed", "address", s.addr, "error", err)
	}
	return nil
}

// Close stops the listener. A profile in flight is cut off: nothing here is
// worth holding the process's stop for.
func (s *DebugServer) Close(context.Context) error { return s.srv.Close() }

// IntegrationSettingsDeps is the shared integrations config.
type IntegrationSettingsDeps struct {
	Integrations *configv1.IntegrationsConfig
}

// NewIntegrationSettings is the integrations config as the server packages
// take it (Integrations): read once, for every node that needs a part of it —
// the vault key, the webhook registry, the OAuth clients, the GitHub App.
func NewIntegrationSettings(d IntegrationSettingsDeps) (*IntegrationSettings, error) {
	s := Integrations(d.Integrations)
	return &s, nil
}

// ProcessSettings records that the process-wide settings that need nothing
// but config are installed: what the LLM layer, the preview URLs, the model
// registry and the GitHub App bot read from package state. A root lists it
// as its LAST field, so it is built before any other node: none can observe
// the settings unset.
type ProcessSettings struct{}

// ProcessSettingsDeps is the config the process-wide settings come from.
type ProcessSettingsDeps struct {
	DataDir      string
	LLM          *configv1.LLMConfig
	Preview      *configv1.PreviewConfig
	Integrations *IntegrationSettings
}

// NewProcessSettings installs the config-only process globals.
func NewProcessSettings(d ProcessSettingsDeps) (*ProcessSettings, error) {
	llmSettings := LLMSettings(d.LLM)
	preview := PreviewSettings(d.Preview)
	// Activities locate files under the data directory through it.
	if err := os.Setenv("RELIANT_DATA_DIR", d.DataDir); err != nil {
		logging.Warn("Failed to set RELIANT_DATA_DIR env var", "error", err)
	}
	// The LLM layer's process settings, before anything can call a model.
	drivers.Configure(llmSettings)
	tools.ConfigurePreviewURLs(preview)
	if err := models.InitGlobalRegistryWithUserConfig(nil); err != nil {
		return nil, fmt.Errorf("failed to initialize model registry: %w", err)
	}
	// Which integration events are our own writes.
	github.ConfigureBot(d.Integrations.GitHubBot)
	return &ProcessSettings{}, nil
}

// ProcessRuntime records that the process-wide state that needs the
// database is installed: the LLM drivers' per-user API keys and Reliant-key
// healer, product analytics and the error reporter.
type ProcessRuntime struct {
	// KeyHealer re-mints a user's Reliant key the control plane refused; nil
	// self-hosted. Only the api-server runs its background sweep.
	KeyHealer *drivers.ReliantKeyHealer
}

// ProcessRuntimeDeps is what that state reads. Settings orders it after the
// config-only globals (drivers.Configure before the key provider).
type ProcessRuntimeDeps struct {
	Settings     *ProcessSettings
	DB           *db.Repo
	ControlPlane *configv1.ControlPlaneConfig
	Network      *configv1.NetworkConfig
	Analytics    *configv1.AnalyticsConfig
}

// NewProcessRuntime installs the database-backed process globals.
func NewProcessRuntime(d ProcessRuntimeDeps) (*ProcessRuntime, error) {
	warnPrivateEgress(d.Network, slog.Default())
	cp := ControlPlaneEndpoint(d.ControlPlane)
	repo := d.DB
	// LLM drivers resolve per-user keys from the database.
	drivers.InitializeAPIKeyProvider(repo)
	healer := drivers.InstallReliantKeyHealer(repo, cp.URL, cp.InternalServiceSecret)
	analytics.Start(AnalyticsSettings(d.Analytics), repo)
	return &ProcessRuntime{KeyHealer: healer}, nil
}

// PayloadStoreDeps is the database the claim-checked payloads live in.
type PayloadStoreDeps struct {
	DB *db.Repo
}

// NewPayloadStore is where large Temporal payloads are claim-checked: the
// shared temporal_payload_blobs table (its GC runs in the api-server). Every
// process's Temporal client must use the same store, or histories become
// unreadable across them.
func NewPayloadStore(d PayloadStoreDeps) (*claimcheck.PostgresStore, error) {
	return claimcheck.NewPostgresStore(d.DB.ORM()), nil
}

// TemporalClient is the process's client of the external Temporal server, and
// the namespace it was dialed for (a raw workflowservice request, such as a
// reset, must name it itself).
type TemporalClient struct {
	Client    client.Client
	Namespace string
}

// TemporalClientDeps is the server, and the boot context the first dial
// waits under. NATS carries the stop nudges the client publishes.
type TemporalClientDeps struct {
	Ctx          context.Context
	Temporal     *configv1.TemporalConfig
	PayloadStore *claimcheck.PostgresStore
	NATS         *NATSConn
}

// stopSignals are the signals that stop running activities of the workflow
// they are sent to: a pause, an interrupt of a thread, a cancel of a spawned
// thread. A successful one is nudged to the workers (stopnudge), so the
// activities it cancels stop within a second instead of a heartbeat window.
var stopSignals = []string{
	v2workflow.SignalPause,
	wfruntime.InterruptThreadSignalName,
	threadcancel.SignalName,
}

// NewTemporalClient dials Temporal. A server that is not up YET is a wait,
// not a crash: the first dial retries with backoff (serverkit.RetryConnect),
// and /readyz names "temporal" as pending meanwhile.
func NewTemporalClient(d TemporalClientDeps) (*TemporalClient, error) {
	return newTemporalClient(d, temporal.NewExternalClient)
}

// dialTemporalFunc connects the SDK client. Production dials
// (temporal.NewExternalClient); a test passes a lazy client, so it builds the
// process's client, decorators included, without a Temporal server.
type dialTemporalFunc func(context.Context, temporal.ExternalClientConfig) (client.Client, error)

func newTemporalClient(d TemporalClientDeps, dial dialTemporalFunc) (*TemporalClient, error) {
	host, port, namespace := TemporalEndpoint(d.Temporal)
	if host == "" {
		return nil, errors.New("config: TEMPORAL_HOST is required")
	}
	var c client.Client
	err := serverkit.RetryConnect(d.Ctx, serverkit.ConnectPolicy{Name: "temporal", Logger: slog.Default()}, func(ctx context.Context) error {
		dialed, err := dial(ctx, temporal.ExternalClientConfig{
			Host:         host,
			Port:         port,
			Namespace:    namespace,
			PayloadStore: d.PayloadStore,
		})
		if err != nil {
			return err
		}
		c = dialed
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Temporal: %w", err)
	}
	// Every stop this process requests (pause, interrupt, cancel, terminate,
	// reset) is also nudged to the workers running the workflow's activities.
	if d.NATS != nil {
		c = stopnudge.Wrap(c, d.NATS.Conn, stopSignals...)
	}
	logging.Info("Connected to external Temporal server", "host", host, "port", port)
	return &TemporalClient{Client: c, Namespace: namespace}, nil
}

// Ready fails while the Temporal server does not answer its health check.
func (t *TemporalClient) Ready(ctx context.Context) error {
	_, err := t.Client.CheckHealth(ctx, &client.CheckHealthRequest{})
	return err
}

// Close closes the client, after every node that reports through it.
func (t *TemporalClient) Close(context.Context) error {
	t.Client.Close()
	return nil
}

// StreamingHub is the streaming hub: ephemeral LLM deltas, from the worker
// that produces them to the api-server replica a client is attached to.
type StreamingHub struct{ Hub streaming.StreamingHub }

// StreamingHubDeps is the hub's transport. NATS orders it after the
// process's connection, whose first connect waits for the server.
type StreamingHubDeps struct {
	NATS   *NATSConn
	Server *configv1.NatsConfig
	Driver string
}

// NewStreamingHub builds the hub (STREAMING_DRIVER, on NATS_URL).
func NewStreamingHub(d StreamingHubDeps) (*StreamingHub, error) {
	driver, err := streaming.ParseStreamingDriver(d.Driver)
	if err != nil {
		return nil, fmt.Errorf("invalid STREAMING_DRIVER %q: %w", d.Driver, err)
	}
	hub, err := streaming.NewStreamingHub(streaming.StreamingConfig{Driver: driver, NATS: NATSEndpoint(d.Server)})
	if err != nil {
		return nil, fmt.Errorf("failed to create streaming hub: %w", err)
	}
	logging.Info("Streaming hub initialized", "driver", d.Driver)
	return &StreamingHub{Hub: hub}, nil
}

// Ready fails while the hub's transport is down.
func (s *StreamingHub) Ready(context.Context) error {
	if !s.Hub.IsConnected() {
		return errors.New("disconnected")
	}
	return nil
}

// Close closes the hub, after every node that streams through it.
func (s *StreamingHub) Close(context.Context) error { return s.Hub.Close() }

// ChatUpdateHub is the chat-updates hub on NATS subject chat.updates, the
// one the api-server's chat streams subscribe to.
type ChatUpdateHub struct {
	*streaming.NATSUpdateHub[db.ChatUpdate]
}

// ChatUpdateHubDeps is the connection the hub publishes on.
type ChatUpdateHubDeps struct {
	NATS *NATSConn
}

// NewChatUpdateHub builds the chat-updates hub.
func NewChatUpdateHub(d ChatUpdateHubDeps) (*ChatUpdateHub, error) {
	return &ChatUpdateHub{streaming.NewNATSUpdateHub[db.ChatUpdate](d.NATS.Conn, "chat.updates", "ChatUpdate")}, nil
}

// Close closes the hub, before the connection it publishes on.
func (h *ChatUpdateHub) Close(context.Context) error { return h.NATSUpdateHub.Close() }

// RepoUpdateFeed records that the repository publishes what it writes: each
// user and chat update goes out on its hub, to the web clients watching it.
type RepoUpdateFeed struct{}

// RepoUpdateFeedDeps is the repository and the hubs it publishes on.
type RepoUpdateFeedDeps struct {
	DB    *db.Repo
	Users *UserUpdateHub
	Chats *ChatUpdateHub
}

// NewRepoUpdateFeed wires the repository's update notifiers to the hubs.
func NewRepoUpdateFeed(d RepoUpdateFeedDeps) (*RepoUpdateFeed, error) {
	d.DB.SetUpdateNotifiers(
		func(update *db.UserUpdate) {
			d.Users.Publish(context.Background(), streaming.UpdateEvent[db.UserUpdate]{
				Key: update.UserID, SequenceNumber: update.SequenceNumber, Payload: *update,
			})
		},
		func(chatID string, seqNum int64, update db.ChatUpdate) {
			d.Chats.Publish(context.Background(), streaming.UpdateEvent[db.ChatUpdate]{
				Key: chatID, SequenceNumber: seqNum, Payload: update,
			})
		},
	)
	logging.Info("Update hubs initialized (NATS)")
	return &RepoUpdateFeed{}, nil
}

// VaultDeps is what boots the credential vault.
type VaultDeps struct {
	Ctx          context.Context
	DB           *db.Repo
	ControlPlane *configv1.ControlPlaneConfig
	DataDir      string
	Integrations *IntegrationSettings
}

// NewVault boots the credential vault: its key is required when hosted and
// generated when self-hosted. A missing or malformed key fails startup.
func NewVault(d VaultDeps) (vault.Service, error) {
	return db.BootVault(d.Ctx, d.DB, ControlPlaneEndpoint(d.ControlPlane).Hosted(), d.DataDir, d.Integrations.VaultKey)
}

// PauseServiceDeps is what pauses and resumes runs.
type PauseServiceDeps struct {
	Temporal *TemporalClient
	DB       *db.Repo
}

// NewPauseService pauses and resumes runs through their workflows.
func NewPauseService(d PauseServiceDeps) (*v2workflow.PauseService, error) {
	ps := v2workflow.NewPauseService(d.Temporal.Client, d.DB)
	ps.SetNamespace(d.Temporal.Namespace)
	return ps, nil
}

// TriggerPollersDeps is which integrations this deployment serves.
type TriggerPollersDeps struct {
	Integrations *IntegrationSettings
}

// NewTriggerPollers is the integration registry: which integrations are
// polled, and which deliver events at all. The api-server and the worker
// build the same one, so they agree.
func NewTriggerPollers(d TriggerPollersDeps) (*webhook.Registry, error) {
	registry, err := webhook.RegistryFor(d.Integrations.Webhooks)
	if err != nil {
		return nil, fmt.Errorf("integration pollers: %w", err)
	}
	return registry, nil
}
