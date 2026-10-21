// Copyright (c) 2025 Reliant Labs
//
// internal/app/providers.go — YOURS. forge writes it once and never again.
//
// The infrastructure every server root shares, as providers: each func shaped
// `func New<X>(<X>Deps) (T, error)` is a node of the composition graph. forge
// resolves each <X>Deps field by type (another node, or one of the graph's
// inputs: g.Ctx, g.Log, g.ServerCfg, g.Probes, and the configs the root's
// command loads — g.ServerProcessConfig, g.DatabaseConfig, g.GatewayConfig),
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
	"path/filepath"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/reliant-labs/forge/pkg/serverkit"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/internal/certs"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/pkg/config"
)

// DBRepo is reliant's database: the Repository over the serving pool (and the
// session pool, when DATABASE_DIRECT_URL names one).
type DBRepo struct{ Repo *db.Repo }

// DBRepoDeps is the database config and the data directory.
type DBRepoDeps struct {
	Database *config.DatabaseConfig
	DataDir  string
}

// NewDBRepo opens the database, retrying the first connect for up to
// DB_CONNECT_TIMEOUT, and waits for the schema the migrating process (the
// api-server, or `reliant db migrate up`) applies: a server role never races
// it (db.MigrationPolicy). It refuses a pool setting it cannot honour.
func NewDBRepo(d DBRepoDeps) (*DBRepo, error) {
	driver, url, directURL := DatabaseURLs(d.Database)
	if url == "" {
		return nil, errors.New("config: DATABASE_URL is required")
	}
	dbDriver, err := db.ParseDatabaseDriver(driver)
	if err != nil {
		return nil, fmt.Errorf("invalid DATABASE_DRIVER %q: %w", driver, err)
	}
	pool, err := DatabasePool(d.Database)
	if err != nil {
		return nil, err
	}
	repo, err := db.NewRepoFromConfig(db.DatabaseConfig{
		Driver:    dbDriver,
		DataDir:   d.DataDir,
		URL:       url,
		DirectURL: directURL,
		Pool:      pool,
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
	Repo         *DBRepo
}

// NewTokenAuthority is the ONE machine-credential authority (`rlat_`):
// control-plane's Introspect when hosted, reliant's own store when
// self-hosted (tokenauthority). Uncached here: a revoked credential is
// refused on its next check.
func NewTokenAuthority(d TokenAuthorityDeps) (tokenauthority.Authority, error) {
	authority, mode, err := tokenauthority.New(tokenauthority.DepsFor(ControlPlaneEndpoint(d.ControlPlane), d.Repo.Repo.ORM()))
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
