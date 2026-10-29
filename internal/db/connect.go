// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/XSAM/otelsql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel/attribute"

	"github.com/reliant-labs/forge/pkg/observe/dbtrace"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/logging"
)

type DatabaseDriver string

const (
	DriverPostgres DatabaseDriver = "postgres"
)

// MigrationPolicy decides what a process does about the schema when it opens
// the database. Exactly one process per database may hold MigrateApply.
//
// Every server used to migrate on open, which is safe for a single process and
// wrong for the deployment we actually run: api-server, temporal-worker and
// gateway all start at once against one Postgres, so goose in each of them
// raced goose in the others. The losers failed on their own success — two
// concurrent CREATE TABLEs collide in the catalog (duplicate key ...
// pg_type_typname_nsp_index) rather than reporting "already exists", and the
// next migration reports `column "seq" of relation "messages" already exists`.
// Both are startup crashes caused purely by the second migrator existing.
//
// Making the DDL idempotent would not have fixed it: CREATE TABLE IF NOT
// EXISTS still races on the same catalog index because the existence check is
// not taken under a lock, and a migration like 20260802000000_add_message_seq
// is a backfill plus an ADD CONSTRAINT — there is no "if not exists" spelling
// of it that stays correct when two processes interleave.
//
// forge's migrator, which replaced goose, takes a run lock, so two migrators
// now take turns instead of colliding (Migrate). One
// owner is still the model: the waiters need no DDL rights, and there is one
// place to look when a migration fails.
type MigrationPolicy int

const (
	// MigrateApply owns the schema: it applies every pending migration on
	// open (Migrate). This is the zero value so that any single-process
	// caller (the CLI, tests) keeps migrating itself by default — a lone
	// process that waited for a migrator would wait forever.
	MigrateApply MigrationPolicy = iota

	// MigrateWait does not migrate. It blocks on open until every embedded
	// migration is recorded applied, then proceeds. Waiting rather than
	// skipping is the point: a process that read a stale schema would fail
	// later, deep in a query, instead of here with a clear message.
	MigrateWait
)

type DatabaseConfig struct {
	Driver  DatabaseDriver
	DataDir string
	URL     string

	// DirectURL is an optional second connection string for the SAME
	// database, used only for work that needs a Postgres session to itself —
	// today, the session-level advisory lock behind Repo.TryAdvisoryLock.
	//
	// Set it when URL points at a transaction-pooling PgBouncer. Behind one,
	// consecutive statements on a single client connection can run on
	// different server backends, so a session lock taken by one statement is
	// held by a backend the caller no longer owns: the unlock lands elsewhere
	// and fails, and any other client that is later handed that backend
	// re-enters the "held" lock and believes it won. DirectURL must reach a
	// backend that stays the caller's for as long as the connection is open —
	// Postgres itself, or a session-mode pool.
	//
	// Empty means URL already has session semantics, which is true of every
	// deployment that does not run a transaction pooler.
	//
	// Migrations run here too when it is set (MigrationURL): forge's migrator
	// holds session-level advisory locks for the length of a run.
	DirectURL string

	// Migrate defaults to MigrateApply; see MigrationPolicy.
	Migrate MigrationPolicy

	// Pool is how the process sizes its pool and waits on the database.
	Pool Pool
}

// Pool is how a process sizes its connection pool and waits on its database:
// the tuning half of its config's DatabaseConfig. A zero field takes its
// default.
type Pool struct {
	// MaxOpenConns and MaxIdleConns size the pool; 0 is 25 / 10. Dev caps
	// them low: the dev Postgres is SHARED by N parallel stacks (one per git
	// worktree), and reliant's api + worker pools count against the same
	// instance's max_connections alongside control-plane's services.
	MaxOpenConns int
	MaxIdleConns int

	// ConnectTimeout is the total time the startup ping keeps retrying a
	// database that is not up yet (a pooler or primary starting beside this
	// process). Zero means serverkit.DefaultDBConnectTimeout. It must stay
	// inside the deployment's startup probe window.
	ConnectTimeout time.Duration

	// SchemaWait bounds how long a MigrateWait process waits for the schema;
	// 0 is DefaultSchemaWait.
	SchemaWait time.Duration

	// KeepIdleWarm keeps MaxIdleConns connections open and idle (see
	// keepIdleWarm). Servers set it; a one-shot CLI process does not, since
	// it would only pay for connections it never uses.
	KeepIdleWarm bool
}

// Pool defaults, for a zero Pool field.
const (
	defaultMaxOpenConns = 25
	defaultMaxIdleConns = 10
)

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func ParseDatabaseDriver(raw string) (DatabaseDriver, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(DriverPostgres):
		return DriverPostgres, nil
	default:
		return "", fmt.Errorf("invalid DATABASE_DRIVER %q (expected postgres)", raw)
	}
}

func ConnectWithConfig(cfg DatabaseConfig) (*sql.DB, error) {
	switch cfg.Driver {
	case DriverPostgres:
		return connectPostgres(cfg)
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", cfg.Driver)
	}
}

func connectPostgres(cfg DatabaseConfig) (*sql.DB, error) {
	logging.Info("Opening Postgres database")

	// serverkit.OpenDB opens the pgx pool so that it rides through a pooler's
	// rollout — cfg.URL is PgBouncer in a deployment: a statement a draining
	// PgBouncer refused ("FATAL: server shutting down", 08P01) runs again on
	// another connection instead of failing the caller, and closing a
	// connection the pooler already dropped is not reported as an error. It
	// retries the first ping with capped backoff: a database that comes up
	// seconds after its clients is a wait, not a crash loop.
	//
	// Pool settings: cfg.Pool (see Pool). Dev sets
	// RELIANT_DB_MAX_OPEN_CONNS=8 / RELIANT_DB_MAX_IDLE_CONNS=2 (control-plane
	// deploy/kcl/dev/main.k _reliant_host_env); prod sets neither and keeps
	// 25/10.
	db, err := serverkit.OpenDB(context.Background(), "pgx", cfg.URL,
		serverkit.DBPoolTuning{
			MaxOpenConns:    orDefault(cfg.Pool.MaxOpenConns, defaultMaxOpenConns),
			MaxIdleConns:    orDefault(cfg.Pool.MaxIdleConns, defaultMaxIdleConns),
			ConnMaxLifetime: 30 * time.Minute,
		},
		serverkit.ConnectPolicy{Timeout: cfg.Pool.ConnectTimeout, Logger: slog.Default()})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres database: %w", err)
	}

	if cfg.Pool.KeepIdleWarm {
		go keepIdleWarm(db, min(orDefault(cfg.Pool.MaxIdleConns, defaultMaxIdleConns), orDefault(cfg.Pool.MaxOpenConns, defaultMaxOpenConns)), idleWarmInterval)
	}

	// Register OTel for DB stats metrics
	if _, regErr := otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(attribute.String("db.system", "postgresql"))); regErr != nil {
		logging.Warn("Failed to register DB stats metrics", "error", regErr)
	}

	switch cfg.Migrate {
	case MigrateWait:
		if err := WaitForSchema(db, cfg.Pool.SchemaWait); err != nil {
			if closeErr := db.Close(); closeErr != nil {
				logging.Error("Failed to close postgres database after schema wait failure", "error", closeErr)
			}
			return nil, err
		}
	default:
		logging.Debug("Applying database migrations...")
		res, err := Migrate(context.Background(), cfg.MigrationURL())
		if err != nil {
			logging.Error("Failed to apply migrations", "error", err)
			if closeErr := db.Close(); closeErr != nil {
				logging.Error("Failed to close postgres database after migration failure", "error", closeErr)
			}
			return nil, fmt.Errorf("failed to apply migrations: %w", err)
		}
		logMigrateResult(slog.Default(), res)
	}

	logging.Info("Database ready", "driver", DriverPostgres)
	return db, nil
}

// sessionPoolMaxOpen bounds the DirectURL pool. Each session-scoped caller
// holds one connection for as long as its session state must live (the worktree
// sweep holds its lock for one pass), and there is one such caller per process
// today; the second slot keeps a slow release from blocking the next attempt.
const sessionPoolMaxOpen = 2

// openSessionPool opens the pool DatabaseConfig.DirectURL names. It keeps no
// idle connections: a caller takes one, holds it exactly as long as its session
// state must live, and closes it, so between uses the pool costs the database
// nothing. It pings once so a wrong DirectURL fails startup, not the first
// sweep an hour later.
func openSessionPool(url string) (*sql.DB, error) {
	pool, err := dbtrace.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("failed to open the direct (session) database pool: %w", err)
	}
	pool.SetMaxOpenConns(sessionPoolMaxOpen)
	pool.SetMaxIdleConns(0)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("failed to connect to the direct (session) database URL: %w", err)
	}
	return pool, nil
}

// idleWarmInterval is how often keepIdleWarm tops the pool up.
const idleWarmInterval = 10 * time.Second

// keepIdleWarm holds the pool at n idle connections until db is closed.
// TODO(forge pin bump): forge's serverkit now owns this as DBPoolTuning.MinIdleConns (serverkit.KeepIdleWarm); set it in connectPostgres and delete keepIdleWarm/warmIdle.
//
// database/sql has no minimum-idle setting: it opens a connection when a
// caller finds none idle, and that caller waits for the dial (TLS and auth,
// through PgBouncer) — measured in prod as 85-336ms db.connect spans on chat
// opens, whose snapshot fans out to ~10 concurrent reads at once. Connections
// leave the idle set all the time without anyone asking for fewer:
// ConnMaxLifetime retires them, a burst past MaxIdleConns closes the excess,
// and a fresh process starts with none. So this tops the idle set back up,
// off the request path, by checking out connections — the pool hands back its
// idle ones first and dials the rest — and returning them, which leaves them
// idle (n is at most MaxIdleConns). It never takes a connection a request
// could need: it asks only for what fits beside the ones in use.
func keepIdleWarm(db *sql.DB, n int, every time.Duration) {
	if n <= 0 {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if !warmIdle(db, n) {
			return // closed
		}
		<-ticker.C
	}
}

// warmIdle tops db up to n idle connections, best-effort. It reports false
// once db is closed.
func warmIdle(db *sql.DB, n int) bool {
	st := db.Stats()
	if st.Idle >= n {
		return true
	}
	want := n
	if st.MaxOpenConnections > 0 && st.InUse+want > st.MaxOpenConnections {
		want = st.MaxOpenConnections - st.InUse
	}
	if want <= st.Idle {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conns := make([]*sql.Conn, 0, want)
	defer func() {
		for _, c := range conns {
			_ = c.Close() // back to the pool, idle
		}
	}()
	for range want {
		c, err := db.Conn(ctx)
		if errors.Is(err, sql.ErrConnDone) || (err != nil && strings.Contains(err.Error(), "database is closed")) {
			return false
		}
		if err != nil {
			logging.Debug("could not warm a database connection", "error", err)
			return true
		}
		conns = append(conns, c)
	}
	return true
}
