package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SchemaSQL is the auth_sessions table [PostgresStore] reads and writes, with
// the indexes its queries use. The project owns the table as one of its
// migrations: forge writes this DDL into db/migrations when it scaffolds a
// login broker with server-side sessions, and a project adopting sessions
// later pastes it into a migration of its own. Keep a hand-written table to
// these columns.
//
// Nothing in a row is a usable credential: id_hash is derived from the
// session id (the cookie) and sealed holds the tokens encrypted under another
// key derived from it, so a copy of this table signs no one in.
// id_hash and sealed are still marked forge:secret, so a read-only role
// (forge db query) never sees them: no operator needs them, and keeping
// them hidden costs nothing.
const SchemaSQL = `CREATE TABLE auth_sessions (
    id_hash         bytea       PRIMARY KEY,
    subject         text        NOT NULL,
    sealed          bytea       NOT NULL,
    user_agent      text        NOT NULL DEFAULT '',
    issuer_session  text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    proved_at       timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    refreshed_at    timestamptz NOT NULL DEFAULT now(),
    idle_expires_at timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL
);

CREATE INDEX auth_sessions_subject_idx ON auth_sessions (subject, created_at DESC);
CREATE INDEX auth_sessions_idle_expires_idx ON auth_sessions (idle_expires_at);
CREATE INDEX auth_sessions_expires_idx ON auth_sessions (expires_at);

COMMENT ON COLUMN auth_sessions.id_hash IS 'forge:secret';
COMMENT ON COLUMN auth_sessions.sealed IS 'forge:secret';
`

// sessionsTable is the table a PostgresStore names.
const sessionsTable = "auth_sessions"

// PostgresStore is the [Store] for a deployed app: one table in the app's own
// database, shared by every replica. Update locks the row (SELECT … FOR
// UPDATE) for the length of its callback, so a refresh happens once per
// session however many replicas ask at the same moment.
type PostgresStore struct {
	db    *sql.DB
	table string
}

// PostgresOption configures a [PostgresStore].
type PostgresOption func(*PostgresStore)

// WithSchema names the schema auth_sessions lives in, so every statement
// says "<schema>".auth_sessions rather than resolving the bare name through
// the connection's search_path, which a transaction-pooling proxy can leave
// pointing anywhere. Empty leaves it unqualified.
func WithSchema(schema string) PostgresOption {
	return func(s *PostgresStore) {
		s.table = sessionsTable
		if schema != "" {
			s.table = `"` + strings.ReplaceAll(schema, `"`, `""`) + `".` + sessionsTable
		}
	}
}

// NewPostgresStore returns a PostgresStore over db. The table must exist
// ([SchemaSQL]).
func NewPostgresStore(db *sql.DB, opts ...PostgresOption) *PostgresStore {
	s := &PostgresStore{db: db, table: sessionsTable}
	for _, o := range opts {
		o(s)
	}
	return s
}

const recordColumns = `id_hash, subject, sealed, user_agent, issuer_session, created_at, proved_at, last_seen_at, refreshed_at, idle_expires_at, expires_at`

func scanRecord(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	err := row.Scan(&r.Key, &r.Subject, &r.Sealed, &r.UserAgent, &r.IssuerSession, &r.CreatedAt, &r.ProvedAt, &r.LastSeenAt,
		&r.RefreshedAt, &r.IdleExpiresAt, &r.ExpiresAt)
	return r, err
}

// Create implements [Store].
func (s *PostgresStore) Create(ctx context.Context, r Record) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+s.table+` (`+recordColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		r.Key, r.Subject, r.Sealed, r.UserAgent, r.IssuerSession, r.CreatedAt, r.ProvedAt, r.LastSeenAt, r.RefreshedAt, r.IdleExpiresAt, r.ExpiresAt)
	if err != nil {
		return fmt.Errorf("session store: insert: %w", err)
	}
	return nil
}

// Get implements [Store].
func (s *PostgresStore) Get(ctx context.Context, key []byte) (Record, error) {
	r, err := scanRecord(s.db.QueryRowContext(ctx, `SELECT `+recordColumns+` FROM `+s.table+` WHERE id_hash = $1`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNoSession
	}
	if err != nil {
		return Record{}, fmt.Errorf("session store: read: %w", err)
	}
	return r, nil
}

// Update implements [Store]. The row stays locked while fn runs, which may
// include a call to the issuer: that holds one connection and one row for
// the length of a refresh, and nothing else.
func (s *PostgresStore) Update(ctx context.Context, key []byte, fn func(*Record) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("session store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	r, err := scanRecord(tx.QueryRowContext(ctx, `SELECT `+recordColumns+` FROM `+s.table+` WHERE id_hash = $1 FOR UPDATE`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoSession
	}
	if err != nil {
		return fmt.Errorf("session store: lock: %w", err)
	}
	if err := fn(&r); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+s.table+` SET sealed = $2, user_agent = $3, issuer_session = $4,
		proved_at = $5, last_seen_at = $6, refreshed_at = $7, idle_expires_at = $8, expires_at = $9 WHERE id_hash = $1`,
		key, r.Sealed, r.UserAgent, r.IssuerSession, r.ProvedAt, r.LastSeenAt, r.RefreshedAt, r.IdleExpiresAt, r.ExpiresAt); err != nil {
		return fmt.Errorf("session store: update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("session store: commit: %w", err)
	}
	return nil
}

// Delete implements [Store].
func (s *PostgresStore) Delete(ctx context.Context, key []byte) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM `+s.table+` WHERE id_hash = $1`, key); err != nil {
		return fmt.Errorf("session store: delete: %w", err)
	}
	return nil
}

// Take implements [Store]. A DELETE waits for the row lock an Update holds
// and then removes, and returns, the row that Update committed.
func (s *PostgresStore) Take(ctx context.Context, key []byte) (Record, error) {
	r, err := scanRecord(s.db.QueryRowContext(ctx, `DELETE FROM `+s.table+` WHERE id_hash = $1 RETURNING `+recordColumns, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNoSession
	}
	if err != nil {
		return Record{}, fmt.Errorf("session store: take: %w", err)
	}
	return r, nil
}

// DeleteSubject implements [Store].
func (s *PostgresStore) DeleteSubject(ctx context.Context, subject string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM `+s.table+` WHERE subject = $1`, subject)
	if err != nil {
		return 0, fmt.Errorf("session store: delete subject: %w", err)
	}
	return res.RowsAffected()
}

// ListSubject implements [Store].
func (s *PostgresStore) ListSubject(ctx context.Context, subject string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM `+s.table+`
		WHERE subject = $1 ORDER BY created_at DESC, id_hash`, subject)
	if err != nil {
		return nil, fmt.Errorf("session store: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("session store: list: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteExpired implements [Store].
func (s *PostgresStore) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM `+s.table+` WHERE expires_at <= $1 OR idle_expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("session store: sweep: %w", err)
	}
	return res.RowsAffected()
}
