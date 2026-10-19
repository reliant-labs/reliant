// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
)

// The legacy credential tables are still what main reads and writes. The boot copy mirrors them
// into provider_accounts (expand); a later release flips the readers and then drops the legacy
// tables (contract). The copy never writes to a legacy table.

const providerReliant = "reliant"

// legacyTable describes how one legacy table maps onto an account.
type legacyTable struct {
	source   string
	provider string // "" = the row carries its own (api_keys)
	authKind core.AuthKind
	// query selects: id, user_id, provider, expires_at, created_at, updated_at, meta1..meta4, then
	// the sealed columns (named by sealed, in order).
	query  string
	sealed []string
	// exists is the correlated predicate (alias a = the account) that holds while the legacy row does.
	exists string
}

var legacyTables = []legacyTable{
	{
		source: postgresstore.LegacySourceClaude, provider: "claude", authKind: core.AuthKindOAuth,
		query: `SELECT id, user_id, 'claude', expires_at, created_at, updated_at,
				account_uuid, account_email, organization_uuid, organization_name, scope,
				access_token_sealed, refresh_token_sealed
			FROM claude_auth_tokens`,
		sealed: []string{"access_token", "refresh_token"},
		exists: `SELECT 1 FROM claude_auth_tokens l WHERE l.user_id = a.user_id`,
	},
	{
		source: postgresstore.LegacySourceCodex, provider: "codex", authKind: core.AuthKindOAuth,
		query: `SELECT id, user_id, 'codex', NULL::timestamptz, created_at, updated_at,
				account_id, NULL::text, NULL::text, NULL::text, NULL::text,
				access_token_sealed, refresh_token_sealed, id_token_sealed
			FROM codex_auth_tokens`,
		sealed: []string{"access_token", "refresh_token", "id_token"},
		exists: `SELECT 1 FROM codex_auth_tokens l WHERE l.user_id = a.user_id`,
	},
	{
		source: postgresstore.LegacySourceCopilot, provider: "copilot", authKind: core.AuthKindDevice,
		query: `SELECT id, user_id, 'copilot', NULL::timestamptz, created_at, updated_at,
				tier, NULL::text, NULL::text, NULL::text, NULL::text,
				github_access_token_sealed, github_refresh_token_sealed
			FROM copilot_auth_tokens`,
		sealed: []string{"github_access_token", "github_refresh_token"},
		exists: `SELECT 1 FROM copilot_auth_tokens l WHERE l.user_id = a.user_id`,
	},
	{
		source: postgresstore.LegacySourceAntigravity, provider: "antigravity", authKind: core.AuthKindOAuth,
		query: `SELECT id, user_id, 'antigravity', expires_at, created_at, updated_at,
				scope, NULL::text, NULL::text, NULL::text, NULL::text,
				access_token_sealed, refresh_token_sealed, id_token_sealed
			FROM antigravity_auth_tokens`,
		sealed: []string{"access_token", "refresh_token", "id_token"},
		exists: `SELECT 1 FROM antigravity_auth_tokens l WHERE l.user_id = a.user_id`,
	},
	{
		source: postgresstore.LegacySourceAPIKeys, provider: "", authKind: core.AuthKindAPIKey,
		query: `SELECT id, user_id, provider, NULL::timestamptz, created_at, updated_at,
				NULL::text, NULL::text, NULL::text, NULL::text, NULL::text,
				api_key_sealed
			FROM api_keys WHERE provider NOT LIKE '` + core.AutomationProviderPrefix + `%' AND api_key_sealed IS NOT NULL`,
		sealed: []string{"api_key"},
		exists: `SELECT 1 FROM api_keys l WHERE l.user_id = a.user_id AND l.provider = a.provider AND l.api_key_sealed IS NOT NULL`,
	},
}

func (t legacyTable) aad(userID, provider, column string) []byte {
	if t.source == postgresstore.LegacySourceAPIKeys {
		return postgresstore.LegacyAPIKeyAAD(userID, provider)
	}
	return postgresstore.LegacyOAuthTokenAAD(t.source, userID, column)
}

// legacyRow is one raw row of a legacy table, secrets still sealed.
type legacyRow struct {
	table                             legacyTable
	id, userID, provider              string
	expiresAt                         sql.NullTime
	createdAt, updatedAt              time.Time
	meta1, meta2, meta3, meta4, meta5 sql.NullString
	sealed                            [][]byte
}

func (t legacyTable) read(ctx context.Context, db *sql.DB, userID, provider string) ([]legacyRow, error) {
	query := t.query
	var args []any
	if userID != "" {
		join := " WHERE "
		if strings.Contains(query, " WHERE ") {
			join = " AND "
		}
		query += join + "user_id = $1"
		args = append(args, userID)
		if provider != "" && t.provider == "" {
			query += " AND provider = $2"
			args = append(args, provider)
		}
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []legacyRow
	for rows.Next() {
		r := legacyRow{table: t, sealed: make([][]byte, len(t.sealed))}
		dest := []any{&r.id, &r.userID, &r.provider, &r.expiresAt, &r.createdAt, &r.updatedAt,
			&r.meta1, &r.meta2, &r.meta3, &r.meta4, &r.meta5}
		for i := range r.sealed {
			dest = append(dest, &r.sealed[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// toImport opens the row's secrets under the legacy AAD and derives the account's identity.
// (nil, nil) means the row is not an account (an OAuth marker, an unknown api_keys provider).
func (r *Repo) legacyToImport(ctx context.Context, lr legacyRow) (*postgresstore.LegacyImport, error) {
	t := lr.table
	cols := make([]string, len(t.sealed))
	for i, column := range t.sealed {
		v, err := r.accounts.OpenLegacy(ctx, lr.userID, t.aad(lr.userID, lr.provider, column), lr.sealed[i])
		if err != nil {
			return nil, fmt.Errorf("opening %s.%s: %w", t.source, column, err)
		}
		cols[i] = v
	}
	if t.source == postgresstore.LegacySourceAPIKeys && (cols[0] == "" || cols[0] == core.ProviderOAuthMarker) {
		// An empty key is a cleared one; the marker only records that an OAuth provider is connected.
		return nil, nil
	}
	if cols[0] == "" {
		return nil, fmt.Errorf("%s row %s has an empty credential", t.source, lr.id)
	}

	u := core.AccountUpsert{UserID: lr.userID, Provider: lr.provider, AuthKind: t.authKind}
	u.Secrets.Secret = cols[0]
	if len(cols) > 1 {
		u.Secrets.Refresh = cols[1]
	}
	if len(cols) > 2 {
		u.Secrets.IDToken = cols[2]
	}
	if lr.expiresAt.Valid {
		u.ExpiresAt = lr.expiresAt.Time
	}
	placeholder := core.LegacyIdentityKey(lr.id)
	u.IdentityKey = placeholder

	switch t.source {
	case postgresstore.LegacySourceClaude:
		u.ExternalAccountID, u.Email = lr.meta1.String, lr.meta2.String
		u.OrganizationID, u.OrganizationName, u.Scope = lr.meta3.String, lr.meta4.String, lr.meta5.String
		if u.ExternalAccountID != "" {
			u.IdentityKey = core.ClaudeIdentityKey(u.ExternalAccountID, u.OrganizationID)
		}
	case postgresstore.LegacySourceCodex:
		u.ExternalAccountID = lr.meta1.String
		if id, ok := core.CodexIdentityFromJWT(u.Secrets.IDToken, u.Secrets.Secret); ok {
			u.IdentityKey, u.Email, u.PlanRaw, u.Plan = id.IdentityKey, id.Email, id.PlanRaw, id.Plan
			u.ExternalAccountID = id.ExternalAccountID
		}
	case postgresstore.LegacySourceCopilot:
		u.PlanRaw = lr.meta1.String
	case postgresstore.LegacySourceAntigravity:
		u.Scope = lr.meta1.String
		if id, ok := core.GoogleIdentityFromIDToken(u.Secrets.IDToken); ok {
			u.IdentityKey, u.Email, u.ExternalAccountID = id.IdentityKey, id.Email, id.ExternalAccountID
		}
	case postgresstore.LegacySourceAPIKeys:
		if lr.provider == providerReliant {
			u.AuthKind, u.IdentityKey = core.AuthKindManaged, core.ManagedIdentityKey
		} else {
			u.IdentityKey = core.APIKeyIdentityKey(lr.userID, lr.provider, u.Secrets.Secret)
			u.KeyHint = core.KeyHint(u.Secrets.Secret)
		}
	}
	return &postgresstore.LegacyImport{Upsert: u, Source: t.source, CreatedAt: lr.createdAt, UpdatedAt: lr.updatedAt}, nil
}

// CopyResult summarizes one CopyLegacyCredentials run.
type CopyResult struct {
	Created, Updated, Unchanged, Failed, Pruned int
}

// CopyLegacyCredentials mirrors every legacy credential into provider_accounts (design §1.4).
// It is idempotent and safe to run on every boot from every process: a global advisory lock
// serializes concurrent runs, an account is written only when it is missing or still mirrors an
// older legacy row, and the legacy tables are never written. A row that does not open (a rotated
// vault key, corrupt ciphertext) is logged at WARN and skipped; the user is simply not offered
// that credential. Mirrors whose legacy row has since been removed by the previous release are
// pruned, so a disconnect there is not undone here.
func (r *Repo) CopyLegacyCredentials(ctx context.Context) (CopyResult, error) {
	var res CopyResult
	sqlDB := r.DB.SQLDB()
	for _, t := range legacyTables {
		rows, err := t.read(ctx, sqlDB, "", "")
		if err != nil {
			return res, fmt.Errorf("reading %s: %w", t.source, err)
		}
		imports := make([]postgresstore.LegacyImport, 0, len(rows))
		for _, lr := range rows {
			in, err := r.legacyToImport(ctx, lr)
			if err != nil {
				slog.Warn("legacy credential skipped: could not be read", "source", t.source, "user_id", lr.userID, "provider", lr.provider, "error", err)
				res.Failed++
				continue
			}
			if in != nil {
				imports = append(imports, *in)
			}
		}
		imported, err := r.accounts.ImportLegacy(ctx, imports)
		if err != nil {
			return res, fmt.Errorf("copying %s: %w", t.source, err)
		}
		res.Created += imported.Created
		res.Updated += imported.Updated
		res.Unchanged += imported.Unchanged
		res.Failed += imported.Failed

		pruned, err := r.accounts.PruneLegacyMirrors(ctx, t.source, t.exists)
		if err != nil {
			return res, fmt.Errorf("pruning mirrors of %s: %w", t.source, err)
		}
		res.Pruned += pruned
	}
	if res.Created+res.Updated+res.Pruned+res.Failed > 0 {
		slog.Info("provider accounts mirrored from legacy credentials",
			"created", res.Created, "updated", res.Updated, "unchanged", res.Unchanged, "failed", res.Failed, "pruned", res.Pruned)
	}
	return res, nil
}

// ReadLegacyCredential implements core.LegacyCredentialSource: the legacy row accountID mirrors,
// read fresh. The coordinator uses it after a failed refresh to adopt a newer lineage the
// previous release wrote. (nil, nil) when the account mirrors no legacy row or the row is gone.
func (r *Repo) ReadLegacyCredential(ctx context.Context, userID, accountID string) (*core.LegacyCredential, error) {
	provider, source, err := r.accounts.LegacyInfo(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	if source == "" {
		return nil, nil
	}
	for _, t := range legacyTables {
		if t.source != source {
			continue
		}
		rows, err := t.read(ctx, r.DB.SQLDB(), userID, provider)
		if err != nil {
			return nil, err
		}
		for _, lr := range rows {
			in, err := r.legacyToImport(ctx, lr)
			if err != nil {
				return nil, err
			}
			if in == nil {
				continue
			}
			return &core.LegacyCredential{
				Source:      in.Source,
				IdentityKey: in.Upsert.IdentityKey,
				Secrets:     in.Upsert.Secrets,
				ExpiresAt:   in.Upsert.ExpiresAt,
				UpdatedAt:   in.UpdatedAt,
			}, nil
		}
	}
	return nil, nil
}

var _ core.LegacyCredentialSource = (*Repo)(nil)
