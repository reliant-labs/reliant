// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
)

// The legacy credential tables (claude_auth_tokens, codex_auth_tokens, copilot_auth_tokens,
// antigravity_auth_tokens and the LLM rows of api_keys) are no longer read or written by anything.
// MigrateLegacyCredentials copies what they still hold into provider_accounts ONE LAST TIME, at
// boot, and records that it did; it never writes a legacy table. A later migration drops them.

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
}

var legacyTables = []legacyTable{
	{
		source: postgresstore.LegacySourceClaude, provider: "claude", authKind: core.AuthKindOAuth,
		query: `SELECT id, user_id, 'claude', expires_at, created_at, updated_at,
				account_uuid, account_email, organization_uuid, organization_name, scope,
				access_token_sealed, refresh_token_sealed
			FROM claude_auth_tokens`,
		sealed: []string{"access_token", "refresh_token"},
	},
	{
		source: postgresstore.LegacySourceCodex, provider: "codex", authKind: core.AuthKindOAuth,
		query: `SELECT id, user_id, 'codex', NULL::timestamptz, created_at, updated_at,
				account_id, NULL::text, NULL::text, NULL::text, NULL::text,
				access_token_sealed, refresh_token_sealed, id_token_sealed
			FROM codex_auth_tokens`,
		sealed: []string{"access_token", "refresh_token", "id_token"},
	},
	{
		source: postgresstore.LegacySourceCopilot, provider: "copilot", authKind: core.AuthKindDevice,
		query: `SELECT id, user_id, 'copilot', NULL::timestamptz, created_at, updated_at,
				tier, NULL::text, NULL::text, NULL::text, NULL::text,
				github_access_token_sealed, github_refresh_token_sealed
			FROM copilot_auth_tokens`,
		sealed: []string{"github_access_token", "github_refresh_token"},
	},
	{
		source: postgresstore.LegacySourceAntigravity, provider: "antigravity", authKind: core.AuthKindOAuth,
		query: `SELECT id, user_id, 'antigravity', expires_at, created_at, updated_at,
				scope, NULL::text, NULL::text, NULL::text, NULL::text,
				access_token_sealed, refresh_token_sealed, id_token_sealed
			FROM antigravity_auth_tokens`,
		sealed: []string{"access_token", "refresh_token", "id_token"},
	},
	{
		source: postgresstore.LegacySourceAPIKeys, provider: "", authKind: core.AuthKindAPIKey,
		query: `SELECT id, user_id, provider, NULL::timestamptz, created_at, updated_at,
				NULL::text, NULL::text, NULL::text, NULL::text, NULL::text,
				api_key_sealed
			FROM api_keys WHERE provider NOT LIKE '` + core.AutomationProviderPrefix + `%' AND api_key_sealed IS NOT NULL
				AND provider NOT IN ('claude', 'codex', 'copilot', 'antigravity')`,
		sealed: []string{"api_key"},
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

func (t legacyTable) read(ctx context.Context, db *sql.DB) ([]legacyRow, error) {
	rows, err := db.QueryContext(ctx, t.query)
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
	if t.source == postgresstore.LegacySourceAPIKeys && (cols[0] == "" || cols[0] == "dummy" || cols[0] == core.ProviderOAuthMarker) {
		// An empty or "dummy" key is a cleared one; the marker only records that an OAuth provider is connected.
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
	placeholder := core.PlaceholderIdentityKey(lr.id)
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

// MigrateLegacyCredentials copies every legacy credential into provider_accounts, once.
//
// Secrets are re-sealed under the provider_accounts AAD, so this cannot be SQL: it runs in Go,
// after the vault is available (BootVault) and after the schema is current. It is idempotent
// and safe from every process: the import is one transaction under a global advisory lock that
// also writes the legacy_credential_copy marker, so racing boots import exactly once and every
// later boot returns after one indexed read without opening a secret.
//
// A row that does not open (a rotated vault key, corrupt ciphertext) is logged at WARN, counted
// and skipped; that user sees the provider as not connected and signs in again. It never
// blocks boot. Because the marker is written regardless, the tables are kept until a later
// release drops them, and the marker's failed count says whether anything remained behind.
func (r *Repo) MigrateLegacyCredentials(ctx context.Context) (postgresstore.ImportResult, bool, error) {
	done, err := r.accounts.LegacyCopyDone(ctx)
	if err != nil || done {
		return postgresstore.ImportResult{}, false, err
	}
	sqlDB := r.DB.SQLDB()
	var (
		imports []postgresstore.LegacyImport
		skipped int
	)
	for _, t := range legacyTables {
		rows, err := t.read(ctx, sqlDB)
		if err != nil {
			return postgresstore.ImportResult{}, false, fmt.Errorf("reading %s: %w", t.source, err)
		}
		for _, lr := range rows {
			in, err := r.legacyToImport(ctx, lr)
			if err != nil {
				slog.Warn("legacy credential skipped: could not be read", "source", t.source, "user_id", lr.userID, "provider", lr.provider, "error", err)
				skipped++
				continue
			}
			if in != nil {
				imports = append(imports, *in)
			}
		}
	}
	res, ran, err := r.accounts.ImportLegacy(ctx, imports, skipped)
	if err != nil {
		return res, false, fmt.Errorf("copying legacy credentials: %w", err)
	}
	if ran {
		slog.Info("legacy credentials copied into provider accounts",
			"created", res.Created, "updated", res.Updated, "unchanged", res.Unchanged, "failed", res.Failed)
	}
	return res, ran, nil
}
