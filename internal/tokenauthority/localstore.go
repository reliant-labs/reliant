package tokenauthority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
)

// SelfHostedOrg is the one org a self-hosted reliant's org automation tokens
// belong to. Self-hosted reliant has no organizations; personal tokens use the
// acting user's id as their org, which keeps one user's tokens from ever being
// listed or revoked under another's.
const SelfHostedOrg = "self-hosted"

// maxLiveUserTokens mirrors control-plane's per-user cap on rotated keys.
const maxLiveUserTokens = 50

// LocalStore is the SELF-HOSTED Authority: the same token system as
// control-plane's — every rule comes from forge/pkg/accesstoken — over
// reliant's own access_tokens table (migration 20260925000000). A token minted
// here behaves identically to one minted by control-plane.
type LocalStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewLocalStore constructs the self-hosted store over reliant's database.
func NewLocalStore(db *sql.DB) *LocalStore { return &LocalStore{db: db, now: time.Now} }

// Introspect resolves a presented token. Revocation and expiry are in the
// WHERE clause and evaluated on the database clock; the hash is re-compared in
// constant time.
func (s *LocalStore) Introspect(ctx context.Context, token string) (*fat.Principal, error) {
	if !fat.HasFormat(token) {
		return nil, accesstokenclient.ErrInactive
	}
	hash := fat.Hash(token)
	var (
		p                              fat.Principal
		stored                         string
		scopes                         []string
		acting, resKind, resID, orgRaw sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, org_id, token_hash, scopes, acting_user_id, resource_kind, resource_id, ephemeral, expires_at
		FROM access_tokens
		WHERE token_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, hash).
		Scan(&p.TokenID, &orgRaw, &stored, pq.Array(&scopes), &acting, &resKind, &resID, &p.Ephemeral, &p.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, accesstokenclient.ErrInactive
	}
	if err != nil {
		return nil, fmt.Errorf("tokenauthority: resolving token: %w", err)
	}
	if !fat.EqualHash(hash, stored) {
		return nil, accesstokenclient.ErrInactive
	}
	if p.Scopes, err = fat.NewSet(scopes); err != nil {
		return nil, fmt.Errorf("tokenauthority: token %s: %w", p.TokenID, err)
	}
	p.OrgID, p.ActingUserID = orgRaw.String, acting.String
	if resKind.Valid {
		p.Resource = &fat.Resource{Kind: fat.ResourceKind(resKind.String), ID: resID.String}
	}
	// Best-effort, throttled to once a minute per token.
	_, _ = s.db.ExecContext(ctx, `UPDATE access_tokens SET last_used_at = now()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, p.TokenID)
	return &p, nil
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// MintForUser mints a personal token acting as req.UserID. With Rotate, it
// atomically revokes the user's live tokens of the same name and scope set.
func (s *LocalStore) MintForUser(ctx context.Context, req MintRequest) (Minted, error) {
	scopes := fat.SetOf(req.Scopes...)
	grant := fat.Grant{
		OrgID:        req.UserID,
		Name:         strings.TrimSpace(req.Name),
		Scopes:       scopes,
		ActingUserID: req.UserID,
		Resource:     req.Resource,
		Ephemeral:    req.Ephemeral,
		ExpiresAt:    req.ExpiresAt,
	}
	if strings.TrimSpace(req.UserID) == "" {
		return Minted{}, fmt.Errorf("%w: user id is required", fat.ErrInvalidGrant)
	}
	if err := grant.Validate(s.now()); err != nil {
		return Minted{}, err
	}
	if !req.Rotate {
		return s.insert(ctx, s.db, grant)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Minted{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('accesstoken:user:' || $1))`, req.UserID); err != nil {
		return Minted{}, err
	}
	scopeArr := pq.Array(scopes.Strings())
	res, err := tx.ExecContext(ctx, `UPDATE access_tokens SET revoked_at = now()
		WHERE acting_user_id = $1 AND name = $2 AND scopes = $3 AND revoked_at IS NULL`,
		req.UserID, grant.Name, scopeArr)
	if err != nil {
		return Minted{}, err
	}
	replaced, _ := res.RowsAffected()
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM access_tokens
		WHERE acting_user_id = $1 AND scopes = $2 AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())`, req.UserID, scopeArr).Scan(&live); err != nil {
		return Minted{}, err
	}
	if live >= maxLiveUserTokens {
		return Minted{}, fmt.Errorf("tokenauthority: too many live tokens (%d); revoke unused ones first", maxLiveUserTokens)
	}
	m, err := s.insert(ctx, tx, grant)
	if err != nil {
		return Minted{}, err
	}
	if err := tx.Commit(); err != nil {
		return Minted{}, err
	}
	m.Rotated = replaced > 0
	return m, nil
}

func (s *LocalStore) insert(ctx context.Context, q queryRower, g fat.Grant) (Minted, error) {
	minted, err := fat.Mint()
	if err != nil {
		return Minted{}, err
	}
	id := uuid.NewString()
	var resKind, resID *string
	if g.Resource != nil {
		k, i := string(g.Resource.Kind), g.Resource.ID
		resKind, resID = &k, &i
	}
	var acting *string
	if g.ActingUserID != "" {
		a := g.ActingUserID
		acting = &a
	}
	var created time.Time
	if err := q.QueryRowContext(ctx, `
		INSERT INTO access_tokens (id, org_id, name, token_hash, token_prefix, scopes,
			created_by_user_id, acting_user_id, resource_kind, resource_id, ephemeral, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $9, $10, $11)
		RETURNING created_at`,
		id, g.OrgID, g.Name, minted.Hash, minted.DisplayPrefix, pq.Array(g.Scopes.Strings()),
		acting, resKind, resID, g.Ephemeral, g.ExpiresAt).Scan(&created); err != nil {
		return Minted{}, fmt.Errorf("tokenauthority: storing token: %w", err)
	}
	return Minted{TokenID: id, Plaintext: minted.Plaintext, DisplayPrefix: minted.DisplayPrefix, ExpiresAt: g.ExpiresAt}, nil
}

// ListForUser lists the live tokens acting as userID, optionally one scope.
func (s *LocalStore) ListForUser(ctx context.Context, userID string, scope fat.Scope) ([]TokenInfo, error) {
	q := `SELECT id, name, token_prefix, scopes, resource_kind, resource_id, ephemeral, created_at, expires_at, last_used_at
		FROM access_tokens WHERE acting_user_id = $1 AND revoked_at IS NULL`
	args := []any{userID}
	if scope != "" {
		q += ` AND $2 = ANY(scopes)`
		args = append(args, string(scope))
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("tokenauthority: listing tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TokenInfo
	for rows.Next() {
		var (
			t              TokenInfo
			resKind, resID sql.NullString
		)
		if err := rows.Scan(&t.ID, &t.Name, &t.DisplayPrefix, pq.Array(&t.Scopes), &resKind, &resID,
			&t.Ephemeral, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		if resKind.Valid {
			t.Resource = &fat.Resource{Kind: fat.ResourceKind(resKind.String), ID: resID.String}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ErrNotFound reports a token that is not the user's (or does not exist).
var ErrNotFound = errors.New("tokenauthority: token not found")

// RevokeForUser revokes one of userID's tokens; the user is in the WHERE
// clause. Idempotent on an already-revoked token.
func (s *LocalStore) RevokeForUser(ctx context.Context, userID, tokenID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE access_tokens SET revoked_at = now()
		WHERE id = $1 AND acting_user_id = $2 AND revoked_at IS NULL`, tokenID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM access_tokens WHERE id = $1 AND acting_user_id = $2)`,
		tokenID, userID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// RevokeResource revokes every live token bound to resource.
func (s *LocalStore) RevokeResource(ctx context.Context, resource fat.Resource) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE access_tokens SET revoked_at = now()
		WHERE resource_kind = $1 AND resource_id = $2 AND revoked_at IS NULL`, string(resource.Kind), resource.ID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RevokeEphemeral revokes userID's ephemeral tokens.
func (s *LocalStore) RevokeEphemeral(ctx context.Context, userID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE access_tokens SET revoked_at = now()
		WHERE acting_user_id = $1 AND ephemeral AND revoked_at IS NULL`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
