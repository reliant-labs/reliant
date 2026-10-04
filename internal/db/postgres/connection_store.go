package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// connectionColumns is the metadata projection. It names every column: a
// SELECT * here would be one migration away from returning ciphertext, and the
// schema keeps ciphertext in connection_secrets precisely so that cannot happen.
const connectionColumns = `id, owner_kind, user_id, org_id, integration_id, auth_kind, name,
	account_label, external_account_id, scopes, oauth_client, auth_header, status, status_reason,
	is_default, access_expires_at, last_used_at, created_at, updated_at, deleted_at`

type connectionStore struct{ db *sql.DB }

// NewConnectionStore creates the Postgres connection store.
func NewConnectionStore(db *sql.DB) core.ConnectionStore { return &connectionStore{db: db} }

type rowScanner interface{ Scan(dest ...any) error }

func scanConnection(r rowScanner) (*core.Connection, error) {
	var (
		c                                           core.Connection
		orgID, label, extID, client, header, reason sql.NullString
		accessExp, lastUsed, deleted                sql.NullTime
		scopes                                      pq.StringArray
	)
	err := r.Scan(&c.ID, &c.OwnerKind, &c.UserID, &orgID, &c.IntegrationID, &c.AuthKind, &c.Name,
		&label, &extID, &scopes, &client, &header, &c.Status, &reason,
		&c.IsDefault, &accessExp, &lastUsed, &c.CreatedAt, &c.UpdatedAt, &deleted)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrConnectionNotFound
		}
		return nil, err
	}
	c.OrgID, c.AccountLabel, c.ExternalAccountID = nullStr(orgID), nullStr(label), nullStr(extID)
	c.OAuthClient, c.AuthHeader, c.StatusReason = nullStr(client), nullStr(header), nullStr(reason)
	c.Scopes = []string(scopes)
	if c.Scopes == nil {
		c.Scopes = []string{}
	}
	c.AccessExpiresAt, c.LastUsedAt, c.DeletedAt = nullTime(accessExp), nullTime(lastUsed), nullTime(deleted)
	return &c, nil
}

func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func strPtrArg(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func timePtrArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func emptyToNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func isUniqueViolation(err error, constraint string) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return constraint == "" || pqErr.Constraint == constraint
	}
	// pgx (database/sql stdlib) surfaces *pgconn.PgError, which exposes the same
	// fields through this interface.
	var pg interface {
		SQLState() string
	}
	if errors.As(err, &pg) && pg.SQLState() == "23505" {
		return true
	}
	return false
}

func insertSecret(ctx context.Context, x execer, s core.ConnectionSecret) error {
	_, err := x.ExecContext(ctx, `
		INSERT INTO connection_secrets (connection_id, field, vault_key_id, ciphertext, generation, updated_at)
		VALUES ($1, $2, $3, $4, 1, now())
		ON CONFLICT (connection_id, field) DO UPDATE
		   SET vault_key_id = EXCLUDED.vault_key_id,
		       ciphertext   = EXCLUDED.ciphertext,
		       generation   = connection_secrets.generation + 1,
		       updated_at   = now()`,
		s.ConnectionID, s.Field, s.VaultKeyID, s.Ciphertext)
	return err
}

func insertEvent(ctx context.Context, x execer, ev core.ConnectionEvent) error {
	_, err := x.ExecContext(ctx, `
		INSERT INTO connection_events (connection_id, user_id, kind, run_id, node_id, tool_call_id, actor)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		ev.ConnectionID, ev.UserID, ev.Kind, emptyToNil(ev.RunID), emptyToNil(ev.NodeID), emptyToNil(ev.ToolCallID), ev.Actor)
	return err
}

func (s *connectionStore) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *connectionStore) CreateConnection(ctx context.Context, c *core.Connection, secrets []core.ConnectionSecret, ev core.ConnectionEvent) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		// The first connection for an integration is its default. Serialize
		// concurrent first-creates for the same (user, integration) so the
		// partial unique index is never what decides.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("connections:%d:%s:%s", len(c.UserID), c.UserID, c.IntegrationID)); err != nil {
			return err
		}
		var existing int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM connections WHERE user_id = $1 AND integration_id = $2 AND deleted_at IS NULL AND owner_kind = 'user' AND is_default`,
			c.UserID, c.IntegrationID).Scan(&existing); err != nil {
			return err
		}
		c.IsDefault = existing == 0
		scopes := pq.StringArray(c.Scopes)
		if scopes == nil {
			scopes = pq.StringArray{}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO connections (id, owner_kind, user_id, org_id, integration_id, auth_kind, name,
				account_label, external_account_id, scopes, oauth_client, auth_header, status, status_reason,
				is_default, access_expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			c.ID, c.OwnerKind, c.UserID, strPtrArg(c.OrgID), c.IntegrationID, c.AuthKind, c.Name,
			strPtrArg(c.AccountLabel), strPtrArg(c.ExternalAccountID), scopes, strPtrArg(c.OAuthClient), strPtrArg(c.AuthHeader),
			c.Status, strPtrArg(c.StatusReason), c.IsDefault, timePtrArg(c.AccessExpiresAt))
		if err != nil {
			if isUniqueViolation(err, "connections_name") {
				return core.ErrConnectionNameTaken
			}
			return err
		}
		for _, sec := range secrets {
			sec.ConnectionID = c.ID
			if err := insertSecret(ctx, tx, sec); err != nil {
				return err
			}
		}
		ev.ConnectionID = c.ID
		return insertEvent(ctx, tx, ev)
	})
}

func (s *connectionStore) ReauthorizeConnection(ctx context.Context, userID, id string, upd core.ConnectionUpdate, secrets []core.ConnectionSecret, ev core.ConnectionEvent) (*core.Connection, error) {
	var out *core.Connection
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		scopes := pq.StringArray(upd.Scopes)
		if scopes == nil {
			scopes = pq.StringArray{}
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE connections
			   SET account_label = $3, external_account_id = $4, scopes = $5, oauth_client = $6,
			       access_expires_at = $7, status = 'active', status_reason = NULL, updated_at = now()
			 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
			id, userID, emptyToNil(upd.AccountLabel), emptyToNil(upd.ExternalAccountID), scopes,
			emptyToNil(upd.OAuthClient), timePtrArg(upd.AccessExpiresAt))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return core.ErrConnectionNotFound
		}
		for _, sec := range secrets {
			sec.ConnectionID = id
			if err := insertSecret(ctx, tx, sec); err != nil {
				return err
			}
		}
		ev.ConnectionID = id
		if err := insertEvent(ctx, tx, ev); err != nil {
			return err
		}
		out, err = scanConnection(tx.QueryRowContext(ctx,
			`SELECT `+connectionColumns+` FROM connections WHERE id = $1 AND user_id = $2`, id, userID))
		return err
	})
	return out, err
}

func (s *connectionStore) GetConnection(ctx context.Context, userID, id string) (*core.Connection, error) {
	return scanConnection(s.db.QueryRowContext(ctx,
		`SELECT `+connectionColumns+` FROM connections WHERE id = $1 AND user_id = $2 AND owner_kind = 'user' AND deleted_at IS NULL`,
		id, userID))
}

func (s *connectionStore) ListConnections(ctx context.Context, userID, integrationID string) ([]*core.Connection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+connectionColumns+` FROM connections
		  WHERE user_id = $1 AND owner_kind = 'user' AND deleted_at IS NULL AND ($2 = '' OR integration_id = $2)
		  ORDER BY integration_id, created_at, id`, userID, integrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*core.Connection{}
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *connectionStore) DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error) {
	return scanConnection(s.db.QueryRowContext(ctx,
		`SELECT `+connectionColumns+` FROM connections
		  WHERE user_id = $1 AND integration_id = $2 AND owner_kind = 'user' AND is_default AND deleted_at IS NULL`,
		userID, integrationID))
}

func (s *connectionStore) FindByExternalAccount(ctx context.Context, userID, integrationID, externalAccountID string) (*core.Connection, error) {
	return scanConnection(s.db.QueryRowContext(ctx,
		`SELECT `+connectionColumns+` FROM connections
		  WHERE user_id = $1 AND integration_id = $2 AND external_account_id = $3 AND owner_kind = 'user' AND deleted_at IS NULL
		  ORDER BY created_at LIMIT 1`,
		userID, integrationID, externalAccountID))
}

func (s *connectionStore) RenameConnection(ctx context.Context, userID, id, name string, ev core.ConnectionEvent) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE connections SET name = $3, updated_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
			id, userID, name)
		if err != nil {
			if isUniqueViolation(err, "connections_name") {
				return core.ErrConnectionNameTaken
			}
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return core.ErrConnectionNotFound
		}
		ev.ConnectionID = id
		return insertEvent(ctx, tx, ev)
	})
}

func (s *connectionStore) SetDefaultConnection(ctx context.Context, userID, id string, ev core.ConnectionEvent) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var integrationID string
		if err := tx.QueryRowContext(ctx,
			`SELECT integration_id FROM connections WHERE id = $1 AND user_id = $2 AND owner_kind = 'user' AND deleted_at IS NULL FOR UPDATE`,
			id, userID).Scan(&integrationID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return core.ErrConnectionNotFound
			}
			return err
		}
		// Clear first: the partial unique index allows only one default.
		if _, err := tx.ExecContext(ctx,
			`UPDATE connections SET is_default = false, updated_at = now()
			  WHERE user_id = $1 AND integration_id = $2 AND is_default AND deleted_at IS NULL AND id <> $3`,
			userID, integrationID, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE connections SET is_default = true, updated_at = now() WHERE id = $1 AND user_id = $2`, id, userID); err != nil {
			return err
		}
		ev.ConnectionID = id
		return insertEvent(ctx, tx, ev)
	})
}

func (s *connectionStore) DeleteConnection(ctx context.Context, userID, id string, ev core.ConnectionEvent) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE connections SET deleted_at = now(), is_default = false, status = 'revoked',
			       status_reason = 'deleted', updated_at = now()
			 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return core.ErrConnectionNotFound
		}
		// Soft delete keeps the row for the audit trail; the secrets are not
		// kept.
		if _, err := tx.ExecContext(ctx, `DELETE FROM connection_secrets WHERE connection_id = $1`, id); err != nil {
			return err
		}
		ev.ConnectionID = id
		return insertEvent(ctx, tx, ev)
	})
}

func (s *connectionStore) RecordTestResult(ctx context.Context, userID, id, accountLabel string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE connections SET account_label = COALESCE($3, account_label), updated_at = now()
		  WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID, emptyToNil(accountLabel))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrConnectionNotFound
	}
	return nil
}

func (s *connectionStore) TouchConnectionUsed(ctx context.Context, userID, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE connections SET last_used_at = now() WHERE id = $1 AND user_id = $2`, id, userID)
	return err
}

func readSecrets(ctx context.Context, x execer, id string, forUpdate bool) (map[string]core.ConnectionSecret, error) {
	q := `SELECT connection_id, field, vault_key_id, ciphertext, generation, updated_at
	        FROM connection_secrets WHERE connection_id = $1 ORDER BY field`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	rows, err := x.QueryContext(ctx, q, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]core.ConnectionSecret{}
	for rows.Next() {
		var sec core.ConnectionSecret
		if err := rows.Scan(&sec.ConnectionID, &sec.Field, &sec.VaultKeyID, &sec.Ciphertext, &sec.Generation, &sec.UpdatedAt); err != nil {
			return nil, err
		}
		out[sec.Field] = sec
	}
	return out, rows.Err()
}

func (s *connectionStore) GetSecrets(ctx context.Context, userID, id string) (map[string]core.ConnectionSecret, error) {
	// Ownership is checked in the same statement that reads ciphertext, so a
	// caller that forgot to Get first still cannot read another user's.
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.connection_id, s.field, s.vault_key_id, s.ciphertext, s.generation, s.updated_at
		  FROM connection_secrets s JOIN connections c ON c.id = s.connection_id
		 WHERE s.connection_id = $1 AND c.user_id = $2 AND c.owner_kind = 'user' AND c.deleted_at IS NULL
		 ORDER BY s.field`, id, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]core.ConnectionSecret{}
	for rows.Next() {
		var sec core.ConnectionSecret
		if err := rows.Scan(&sec.ConnectionID, &sec.Field, &sec.VaultKeyID, &sec.Ciphertext, &sec.Generation, &sec.UpdatedAt); err != nil {
			return nil, err
		}
		out[sec.Field] = sec
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, core.ErrConnectionNotFound
	}
	return out, nil
}

type secretsTx struct {
	tx      *sql.Tx
	conn    *core.Connection
	secrets map[string]core.ConnectionSecret
}

func (t *secretsTx) Connection() *core.Connection              { return t.conn }
func (t *secretsTx) Secrets() map[string]core.ConnectionSecret { return t.secrets }

func (t *secretsTx) Replace(ctx context.Context, expectedGeneration int64, secrets []core.ConnectionSecret, accessExpiresAt *time.Time) error {
	cur, ok := t.secrets[core.SecretFieldAccessToken]
	if !ok || cur.Generation != expectedGeneration {
		return core.ErrGenerationConflict
	}
	for _, sec := range secrets {
		sec.ConnectionID = t.conn.ID
		if err := insertSecret(context.WithoutCancel(ctx), t.tx, sec); err != nil {
			return err
		}
	}
	_, err := t.tx.ExecContext(ctx,
		`UPDATE connections SET access_expires_at = $2, updated_at = now() WHERE id = $1`, t.conn.ID, timePtrArg(accessExpiresAt))
	return err
}

func (t *secretsTx) MarkStatus(ctx context.Context, status, reason string) error {
	_, err := t.tx.ExecContext(ctx,
		`UPDATE connections SET status = $2, status_reason = $3, updated_at = now() WHERE id = $1`,
		t.conn.ID, status, emptyToNil(reason))
	return err
}

func (t *secretsTx) AppendEvent(ctx context.Context, ev core.ConnectionEvent) error {
	ev.ConnectionID = t.conn.ID
	return insertEvent(ctx, t.tx, ev)
}

func (s *connectionStore) WithSecretsLock(ctx context.Context, userID, id string, fn func(core.SecretsTx) error) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		// Lock the connection row (not just the secrets): a connection with no
		// secret rows still needs a lock target, and this one is always there.
		conn, err := scanConnection(tx.QueryRowContext(ctx,
			`SELECT `+connectionColumns+` FROM connections
			  WHERE id = $1 AND user_id = $2 AND owner_kind = 'user' AND deleted_at IS NULL FOR UPDATE`, id, userID))
		if err != nil {
			return err
		}
		secrets, err := readSecrets(ctx, tx, id, true)
		if err != nil {
			return err
		}
		return fn(&secretsTx{tx: tx, conn: conn, secrets: secrets})
	})
}

func (s *connectionStore) CreateOAuthFlow(ctx context.Context, f *core.OAuthFlow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_flows (state_hash, user_id, session_id_hash, integration_id, pkce_verifier_sealed,
			redirect_after, reconnect_connection_id, connection_name, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		f.StateHash, f.UserID, f.SessionIDHash, f.IntegrationID, f.PKCEVerifierSealed,
		emptyToNil(f.RedirectAfter), emptyToNil(f.ReconnectConnectionID), emptyToNil(f.ConnectionName), f.ExpiresAt)
	return err
}

func (s *connectionStore) ConsumeOAuthFlow(ctx context.Context, stateHash []byte, now time.Time) (*core.OAuthFlow, error) {
	var (
		f                          core.OAuthFlow
		redirect, reconnect, cname sql.NullString
	)
	// One statement: the compare-and-swap on consumed_at is what makes the
	// state single-use even under concurrent replays.
	err := s.db.QueryRowContext(ctx, `
		UPDATE oauth_flows SET consumed_at = $2
		 WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > $2
		RETURNING state_hash, user_id, session_id_hash, integration_id, pkce_verifier_sealed,
		          redirect_after, reconnect_connection_id, connection_name, expires_at`,
		stateHash, now).Scan(&f.StateHash, &f.UserID, &f.SessionIDHash, &f.IntegrationID, &f.PKCEVerifierSealed,
		&redirect, &reconnect, &cname, &f.ExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrOAuthFlowInvalid
		}
		return nil, err
	}
	f.RedirectAfter, f.ReconnectConnectionID, f.ConnectionName = redirect.String, reconnect.String, cname.String
	return &f, nil
}

func (s *connectionStore) AppendConnectionEvent(ctx context.Context, ev core.ConnectionEvent) error {
	return insertEvent(ctx, s.db, ev)
}

func (s *connectionStore) ListConnectionEvents(ctx context.Context, userID, id string, limit int, beforeID int64) ([]core.ConnectionEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.connection_id, e.user_id, e.kind, COALESCE(e.run_id,''), COALESCE(e.node_id,''),
		       COALESCE(e.tool_call_id,''), e.actor, e.at
		  FROM connection_events e
		 WHERE e.connection_id = $1 AND e.user_id = $2 AND ($3::bigint = 0 OR e.id < $3)
		 ORDER BY e.id DESC LIMIT $4`, id, userID, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing connection events: %w", err)
	}
	defer rows.Close()
	out := []core.ConnectionEvent{}
	for rows.Next() {
		var ev core.ConnectionEvent
		if err := rows.Scan(&ev.ID, &ev.ConnectionID, &ev.UserID, &ev.Kind, &ev.RunID, &ev.NodeID, &ev.ToolCallID, &ev.Actor, &ev.At); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
