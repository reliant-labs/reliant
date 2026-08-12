// Copyright (c) 2025 Reliant Labs

package connectorgrant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DBTX is the subset of database/sql this store needs, so it can run against
// either a *sql.DB or a transaction.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SQLStore is the Postgres-backed Store.
type SQLStore struct {
	db DBTX
}

// NewSQLStore returns a Store backed by db.
func NewSQLStore(db DBTX) *SQLStore {
	return &SQLStore{db: db}
}

const grantColumns = `
	id, user_id, daemon_id, name, token_hash, token_prefix,
	allowed_tools, path_root, exec_mode, exec_allowlist,
	expires_at, last_used_at, revoked_at, created_at, updated_at`

// CreateGrant inserts a grant.
//
// The DB's CHECK constraints are the backstop, but the same rules are enforced
// here so a caller gets a clear message rather than a constraint violation.
func (s *SQLStore) CreateGrant(ctx context.Context, g *Grant) error {
	if err := validateGrant(g); err != nil {
		return err
	}

	tools, err := json.Marshal(g.AllowedTools)
	if err != nil {
		return fmt.Errorf("encode allowed tools: %w", err)
	}
	allowlist, err := json.Marshal(orEmpty(g.ExecAllowlist))
	if err != nil {
		return fmt.Errorf("encode exec allowlist: %w", err)
	}

	now := time.Now().UTC()
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now
	}
	g.UpdatedAt = now

	const query = `
		INSERT INTO connector_grants (
			id, user_id, daemon_id, name, token_hash, token_prefix,
			allowed_tools, path_root, exec_mode, exec_allowlist,
			expires_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`

	if _, err := s.db.ExecContext(ctx, query,
		g.ID, g.UserID, g.DaemonID, g.Name, g.TokenHash, g.TokenPrefix,
		tools, g.PathRoot, string(g.ExecMode), allowlist,
		g.ExpiresAt, g.CreatedAt, g.UpdatedAt,
	); err != nil {
		return fmt.Errorf("create connector grant: %w", err)
	}
	return nil
}

// validateGrant rejects grants that would be stored as unusable.
func validateGrant(g *Grant) error {
	switch {
	case g == nil:
		return errors.New("grant is required")
	case g.ID == "":
		return errors.New("grant id is required")
	case g.UserID == "":
		return errors.New("grant user id is required")
	case g.DaemonID == "":
		// A grant must name exactly one daemon. There is no "all daemons"
		// form, by design.
		return errors.New("grant must be bound to a daemon")
	case g.TokenHash == "":
		return errors.New("grant credential hash is required")
	case len(g.AllowedTools) == 0:
		return errors.New("grant must allow at least one tool")
	case g.PathRoot == "":
		return errors.New("grant must specify a path root")
	}

	switch g.ExecMode {
	case ExecDeny, ExecUnrestricted:
	case ExecAllowlist:
		if len(g.ExecAllowlist) == 0 {
			return errors.New("exec allowlist mode requires at least one allowed command")
		}
	case "":
		// Default to the safest mode rather than rejecting, so a caller that
		// simply did not think about exec gets no exec.
		g.ExecMode = ExecDeny
	default:
		return fmt.Errorf("unknown exec mode %q", g.ExecMode)
	}

	return nil
}

// GetGrantByTokenHash resolves a credential to a live grant.
//
// Liveness is filtered in SQL rather than checked afterward, so a revoked or
// expired grant cannot be used by a caller that forgot to look.
func (s *SQLStore) GetGrantByTokenHash(ctx context.Context, tokenHash string) (*Grant, error) {
	if tokenHash == "" {
		return nil, ErrNotFound
	}

	query := `SELECT ` + grantColumns + `
		FROM connector_grants
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())`

	g, err := scanGrant(s.db.QueryRowContext(ctx, query, tokenHash))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Deliberately not distinguishing missing from revoked or expired:
			// all three are authentication failures, and telling them apart
			// only helps someone probing credentials.
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("look up connector grant: %w", err)
	}
	return g, nil
}

// GetGrant fetches one of the user's grants, including revoked ones so the UI
// can show history.
func (s *SQLStore) GetGrant(ctx context.Context, userID, id string) (*Grant, error) {
	query := `SELECT ` + grantColumns + ` FROM connector_grants WHERE user_id = $1 AND id = $2`
	g, err := scanGrant(s.db.QueryRowContext(ctx, query, userID, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get connector grant: %w", err)
	}
	return g, nil
}

// GetGrantByID returns a live grant by id, with the same liveness filtering as
// credential lookup so a revoked or expired grant cannot be re-resolved.
func (s *SQLStore) GetGrantByID(ctx context.Context, id string) (*Grant, error) {
	query := `SELECT ` + grantColumns + `
		FROM connector_grants
		WHERE id = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())`

	g, err := scanGrant(s.db.QueryRowContext(ctx, query, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get connector grant by id: %w", err)
	}
	return g, nil
}

// ListGrantsByUser returns a user's grants, newest first.
func (s *SQLStore) ListGrantsByUser(ctx context.Context, userID string) ([]*Grant, error) {
	query := `SELECT ` + grantColumns + ` FROM connector_grants WHERE user_id = $1 ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("list connector grants: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var grants []*Grant
	for rows.Next() {
		g, err := scanGrantRows(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// RevokeGrant marks a grant revoked. It reports whether a live grant was
// changed, so a repeated revoke is distinguishable from one that took effect.
func (s *SQLStore) RevokeGrant(ctx context.Context, userID, id string) (bool, error) {
	// Scoped by user_id so a caller can never revoke someone else's grant by
	// guessing an id.
	const query = `
		UPDATE connector_grants
		SET revoked_at = now(), updated_at = now()
		WHERE user_id = $1 AND id = $2 AND revoked_at IS NULL`

	res, err := s.db.ExecContext(ctx, query, userID, id)
	if err != nil {
		return false, fmt.Errorf("revoke connector grant: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, nil
	}
	return affected > 0, nil
}

// TouchGrant stamps last use. Failures are the caller's to ignore: this is
// telemetry, and it must never fail a tool call.
func (s *SQLStore) TouchGrant(ctx context.Context, id string) error {
	const query = `UPDATE connector_grants SET last_used_at = now() WHERE id = $1`
	if _, err := s.db.ExecContext(ctx, query, id); err != nil {
		return fmt.Errorf("touch connector grant: %w", err)
	}
	return nil
}

// RecordAudit appends an audit row.
func (s *SQLStore) RecordAudit(ctx context.Context, rec *AuditRecord) error {
	if rec == nil || rec.ID == "" {
		return errors.New("audit record id is required")
	}

	// The column is NOT NULL, so an absent payload is stored as an empty
	// object rather than as SQL NULL.
	args := rec.Arguments
	if len(args) == 0 {
		args = []byte(`{}`)
	}

	createdAt := rec.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	status := rec.Status
	if status == "" {
		status = AuditCompleted
	}

	const query = `
		INSERT INTO connector_audit_log (
			id, grant_id, user_id, daemon_id, tool_name, command_type,
			arguments, denied, error_message, duration_ms, status, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`

	if _, err := s.db.ExecContext(ctx, query,
		rec.ID, rec.GrantID, rec.UserID, rec.DaemonID, rec.ToolName, rec.CommandType,
		args, rec.Denied, nullString(rec.ErrorMsg), rec.DurationMS, status, createdAt,
	); err != nil {
		return fmt.Errorf("record connector audit: %w", err)
	}
	return nil
}

// CompleteAudit resolves an intent row.
//
// Scoped to rows still in AuditStarted so a late or duplicated completion
// cannot overwrite an outcome already recorded.
func (s *SQLStore) CompleteAudit(ctx context.Context, id, status, errMsg string, durationMS int) error {
	if id == "" {
		return errors.New("audit record id is required")
	}

	const query = `
		UPDATE connector_audit_log
		SET status = $2, error_message = $3, duration_ms = $4, denied = ($2 = 'denied')
		WHERE id = $1 AND status = 'started'`

	if _, err := s.db.ExecContext(ctx, query, id, status, nullString(errMsg), durationMS); err != nil {
		return fmt.Errorf("complete connector audit: %w", err)
	}
	return nil
}

// ListAuditByUser returns a user's recent connector activity.
func (s *SQLStore) ListAuditByUser(ctx context.Context, userID string, limit int) ([]*AuditRecord, error) {
	const query = `
		SELECT id, grant_id, user_id, daemon_id, tool_name, command_type,
		       arguments, denied, error_message, duration_ms, status, created_at
		FROM connector_audit_log
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2`
	return s.queryAudit(ctx, query, userID, clampLimit(limit))
}

// ListAuditByGrant returns activity for one connector. It is scoped by user so
// a grant id alone does not expose another user's log.
func (s *SQLStore) ListAuditByGrant(ctx context.Context, userID, grantID string, limit int) ([]*AuditRecord, error) {
	const query = `
		SELECT id, grant_id, user_id, daemon_id, tool_name, command_type,
		       arguments, denied, error_message, duration_ms, status, created_at
		FROM connector_audit_log
		WHERE user_id = $1 AND grant_id = $2
		ORDER BY created_at DESC
		LIMIT $3`
	return s.queryAudit(ctx, query, userID, grantID, clampLimit(limit))
}

func (s *SQLStore) queryAudit(ctx context.Context, query string, args ...any) ([]*AuditRecord, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list connector audit: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*AuditRecord
	for rows.Next() {
		var (
			rec      AuditRecord
			argBytes []byte
			errMsg   sql.NullString
			duration sql.NullInt64
		)
		if err := rows.Scan(
			&rec.ID, &rec.GrantID, &rec.UserID, &rec.DaemonID, &rec.ToolName, &rec.CommandType,
			&argBytes, &rec.Denied, &errMsg, &duration, &rec.Status, &rec.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan connector audit: %w", err)
		}
		rec.Arguments = argBytes
		rec.ErrorMsg = errMsg.String
		rec.DurationMS = int(duration.Int64)
		out = append(out, &rec)
	}
	return out, rows.Err()
}

// rowScanner unifies *sql.Row and *sql.Rows for the shared scan path.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanGrant(row rowScanner) (*Grant, error) { return scanGrantFrom(row) }

func scanGrantRows(rows *sql.Rows) (*Grant, error) { return scanGrantFrom(rows) }

func scanGrantFrom(row rowScanner) (*Grant, error) {
	var (
		g         Grant
		tools     []byte
		allowlist []byte
		execMode  string
		expires   sql.NullTime
		lastUsed  sql.NullTime
		revoked   sql.NullTime
	)

	if err := row.Scan(
		&g.ID, &g.UserID, &g.DaemonID, &g.Name, &g.TokenHash, &g.TokenPrefix,
		&tools, &g.PathRoot, &execMode, &allowlist,
		&expires, &lastUsed, &revoked, &g.CreatedAt, &g.UpdatedAt,
	); err != nil {
		return nil, err
	}

	if err := json.Unmarshal(tools, &g.AllowedTools); err != nil {
		return nil, fmt.Errorf("decode allowed tools for grant %s: %w", g.ID, err)
	}
	if len(allowlist) > 0 {
		if err := json.Unmarshal(allowlist, &g.ExecAllowlist); err != nil {
			return nil, fmt.Errorf("decode exec allowlist for grant %s: %w", g.ID, err)
		}
	}

	g.ExecMode = ExecMode(execMode)
	g.ExpiresAt = timePtr(expires)
	g.LastUsedAt = timePtr(lastUsed)
	g.RevokedAt = timePtr(revoked)

	return &g, nil
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orEmpty(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// clampLimit keeps an unbounded or hostile limit from reading the whole table.
func clampLimit(limit int) int {
	const (
		defaultLimit = 100
		maxLimit     = 1000
	)
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}
