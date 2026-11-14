package dbanalyze

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// A DSN TARGET IS READ, CLONED, AND NEVER WRITTEN.
//
// An analysis applies migrations — DDL, data changes, nontransactional index
// builds — for real, and measures them. On the database a caller names that
// would change it: a transaction that is always rolled back does not hold
// (a migration's own COMMIT ends it, a sequence advances regardless, CREATE
// INDEX CONCURRENTLY cannot run in it at all). So the target is only read —
// its marker, version, statistics and schema_migrations, through a Source —
// and the analysis runs on a copy: CREATE DATABASE … TEMPLATE <target>, a
// file-level copy of every relation, catalog and row estimate, dropped when
// the analysis is done.

// MarkerSetting is the database setting a dsn target must carry to be
// analyzed: `ALTER DATABASE <db> SET forge.analyze_target = 'on'`. A database
// that has not been marked by whoever made it — production, say — is refused.
const MarkerSetting = "forge.analyze_target"

// Source is a read-only connection to a dsn target, for what is read from
// the target itself before it is cloned.
type Source struct {
	conn *pgx.Conn
}

// OpenSource connects to the target. Close it before CloneDatabase: postgres
// copies a database only while no session is connected to it.
func OpenSource(ctx context.Context, dsn string) (*Source, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect to the target: %w", parseCause(err))
	}
	if _, err := conn.Exec(ctx, "SET default_transaction_read_only = on"); err != nil {
		_ = conn.Close(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("connect to the target: %w", err)
	}
	return &Source{conn: conn}, nil
}

// Close disconnects. It is safe to call more than once.
func (s *Source) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	err := s.conn.Close(context.Background())
	s.conn = nil
	return err
}

// Marked reports whether the target database carries MarkerSetting = on as a
// DATABASE-level setting (pg_db_role_setting, any role). A role's setting or
// a session's SET does not count: the marker says what the DATABASE is, and
// only its owner (or a superuser) can set that.
func (s *Source) Marked(ctx context.Context) (bool, error) {
	var configs []string
	err := s.conn.QueryRow(ctx, `SELECT COALESCE(s.setconfig, '{}')
		FROM pg_db_role_setting s JOIN pg_database d ON d.oid = s.setdatabase
		WHERE d.datname = current_database() AND s.setrole = 0`).Scan(&configs)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the target's settings: %w", err)
	}
	for _, c := range configs {
		k, v, _ := strings.Cut(c, "=")
		if strings.EqualFold(k, MarkerSetting) {
			switch strings.ToLower(strings.Trim(v, `'"`)) {
			case "on", "true", "yes", "1":
				return true, nil
			}
		}
	}
	return false, nil
}

// Info reads the target's server version and whether its statistics can
// size tables.
func (s *Source) Info(ctx context.Context, kind TargetKind, identity string) (TargetInfo, error) {
	return readInfo(ctx, pgxQuerier{s.conn}, kind, identity)
}

// MigrationState reads the target's schema_migrations without creating it.
func (s *Source) MigrationState(ctx context.Context) (MigrationState, error) {
	return readMigrationState(ctx, pgxQuerier{s.conn})
}

// Clone is a disposable copy of a target database. The analysis runs on it;
// a caller with more to do on the migrated schema (a query pass) uses DSN
// until it is done, then Close drops it.
type Clone struct {
	info  CloneInfo
	dsn   string
	admin *pgx.ConnConfig
}

// CloneInfo is what the document reports of the copy.
type CloneInfo struct {
	// Database is the copy's name.
	Database string `json:"database"`
	// DurationMS is how long CREATE DATABASE … TEMPLATE took.
	DurationMS float64 `json:"duration_ms"`
	// SizeBytes is pg_database_size of the copy.
	SizeBytes int64 `json:"size_bytes"`
}

// SQLSTATEs CloneDatabase explains.
const (
	sqlStateObjectInUse          = "55006" // the template has other sessions
	sqlStateInsufficientPrivilge = "42501"
)

// CloneDatabase copies the database dsn names into a new one,
// forge_analyze_<random>, with CREATE DATABASE … TEMPLATE (STRATEGY FILE_COPY
// on postgres 15 and newer, the only strategy before it): one checkpoint and
// a file copy, rather than WAL-logging every block. The copy is made from a
// maintenance connection to the same server (the postgres database, else
// template1), because postgres will not copy a database anyone — this process
// included — is connected to.
//
// It needs a role that may create databases (CREATEDB) and may copy this one
// (its owner, a superuser, or a target marked IS_TEMPLATE), and a target with
// no other session connected. Either failing is an error naming the cause,
// and for the second, who is connected.
func CloneDatabase(ctx context.Context, dsn string) (*Clone, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse the target's connection string: %w", parseCause(err))
	}
	source := cfg.Database
	if source == "" {
		return nil, errors.New("the target's connection string names no database")
	}
	admin, conn, err := connectMaintenance(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	var versionNum int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&versionNum); err != nil {
		return nil, fmt.Errorf("read the server version: %w", err)
	}
	name, err := cloneName()
	if err != nil {
		return nil, err
	}
	stmt := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize() + " TEMPLATE " + pgx.Identifier{source}.Sanitize()
	if versionNum >= 150000 {
		stmt += " STRATEGY FILE_COPY"
	}
	start := time.Now()
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return nil, explainCloneError(ctx, conn, source, err)
	}
	c := &Clone{info: CloneInfo{Database: name}, admin: admin}
	c.info.DurationMS = float64(time.Since(start).Microseconds()) / 1000
	if err := conn.QueryRow(ctx, "SELECT pg_database_size($1)", name).Scan(&c.info.SizeBytes); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("size the copy: %w", err)
	}
	if c.dsn, err = withDatabase(dsn, name); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// DSN is the copy's connection string: the target's, naming the copy.
func (c *Clone) DSN() string { return c.dsn }

// Info is what the document reports of the copy.
func (c *Clone) Info() CloneInfo { return c.info }

// Close drops the copy, terminating any session still connected to it. It
// runs on its own deadline, not the analysis's context, so a cancelled run
// (an interrupt, a timeout) still cleans up. It is safe to call more than
// once.
func (c *Clone) Close() error {
	if c == nil || c.admin == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, c.admin)
	if err != nil {
		return fmt.Errorf("drop the analysis copy %s: %w", c.info.Database, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", c.info.Database); err != nil {
		return fmt.Errorf("drop the analysis copy %s: %w", c.info.Database, err)
	}
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{c.info.Database}.Sanitize()); err != nil {
		return fmt.Errorf("drop the analysis copy %s: %w", c.info.Database, err)
	}
	c.admin = nil
	return nil
}

// connectMaintenance connects to the target's server through a database
// other than the target: postgres, else template1.
func connectMaintenance(ctx context.Context, target *pgx.ConnConfig) (*pgx.ConnConfig, *pgx.Conn, error) {
	var errs []error
	for _, db := range []string{"postgres", "template1"} {
		if db == target.Database {
			continue
		}
		cfg := target.Copy()
		cfg.Database = db
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err == nil {
			return cfg, conn, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", db, err))
	}
	return nil, nil, fmt.Errorf("connect to the target's server through its postgres or template1 database, to copy the target from: %w", errors.Join(errs...))
}

// explainCloneError turns CREATE DATABASE's refusal into its cause.
func explainCloneError(ctx context.Context, conn *pgx.Conn, source string, err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("copy the target %s: %w", source, err)
	}
	switch pgErr.Code {
	case sqlStateObjectInUse:
		return fmt.Errorf("copy the target %s: other sessions are connected to it (%s), and postgres copies a database only while nobody is: disconnect them, or analyze a snapshot nothing else uses",
			source, connectedSessions(context.WithoutCancel(ctx), conn, source))
	case sqlStateInsufficientPrivilge:
		return fmt.Errorf("copy the target %s: %s. The role forge connects as needs CREATEDB (ALTER ROLE <role> CREATEDB), and must own %s (or be a superuser, or %s must be marked ALTER DATABASE %s IS_TEMPLATE true)",
			source, pgErr.Message, source, source, source)
	}
	return fmt.Errorf("copy the target %s: %w", source, err)
}

// connectedSessions names who is connected to a database.
func connectedSessions(ctx context.Context, conn *pgx.Conn, db string) string {
	rows, err := conn.Query(ctx, `SELECT pid, COALESCE(usename, ''), COALESCE(application_name, ''), COALESCE(host(client_addr), 'local')
		FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid() ORDER BY pid`, db)
	if err != nil {
		return "pg_stat_activity is unreadable: " + err.Error()
	}
	defer rows.Close()
	var who []string
	for rows.Next() {
		var pid int
		var user, app, addr string
		if err := rows.Scan(&pid, &user, &app, &addr); err != nil {
			return "pg_stat_activity is unreadable: " + err.Error()
		}
		s := fmt.Sprintf("pid %d user %s from %s", pid, user, addr)
		if app != "" {
			s += fmt.Sprintf(" (%s)", app)
		}
		who = append(who, s)
	}
	if len(who) == 0 {
		return "none is left now"
	}
	return strings.Join(who, "; ")
}

// cloneName is forge_analyze_ and 16 random hex digits.
func cloneName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("name the analysis copy: %w", err)
	}
	return "forge_analyze_" + hex.EncodeToString(b), nil
}

// withDatabase returns dsn naming database name instead, and checks that pgx
// reads it so.
func withDatabase(dsn, name string) (string, error) {
	var out string
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("parse the target's connection string: %w", parseCause(err))
		}
		u.Path, u.RawPath = "/"+name, ""
		q := u.Query()
		q.Del("dbname")
		q.Del("database")
		u.RawQuery = q.Encode()
		out = u.String()
	} else {
		// A keyword/value string: a later keyword overrides an earlier one.
		out = dsn + " dbname=" + name
	}
	cfg, err := pgconn.ParseConfig(out)
	if err != nil || cfg.Database != name {
		return "", fmt.Errorf("could not point the target's connection string at the analysis copy %s", name)
	}
	return out, nil
}

// parseCause is what to report of a connection string that would not parse:
// the cause alone. A *url.Error's text is the raw URL, password and all, and
// pgx's *pgconn.ParseConfigError masks only the positions it knows hold a
// password, echoing the rest (a fragment, say) as written.
func parseCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	var pe *pgconn.ParseConfigError
	if errors.As(err, &pe) {
		if inner := pe.Unwrap(); inner != nil {
			return inner
		}
		return errors.New("not a valid connection string")
	}
	return err
}

// pgxQuerier adapts a pgx connection to the reads readInfo and
// readMigrationState make.
type pgxQuerier struct{ conn *pgx.Conn }

func (q pgxQuerier) QueryRowContext(ctx context.Context, query string, args ...any) rowScanner {
	return q.conn.QueryRow(ctx, query, args...)
}
