// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
	"github.com/reliant-labs/reliant/internal/store"
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
	// read returns the table's rows (internal/store's readers document each one's Meta); sealed
	// names their sealed columns, in order, for the legacy AAD.
	read   func(ctx context.Context, db orm.Context) ([]store.LegacyCredential, error)
	sealed []string
}

var legacyTables = []legacyTable{
	{
		source: postgresstore.LegacySourceClaude, provider: "claude", authKind: core.AuthKindOAuth,
		read:   store.ListLegacyClaudeCredentials,
		sealed: []string{"access_token", "refresh_token"},
	},
	{
		source: postgresstore.LegacySourceCodex, provider: "codex", authKind: core.AuthKindOAuth,
		read:   store.ListLegacyCodexCredentials,
		sealed: []string{"access_token", "refresh_token", "id_token"},
	},
	{
		source: postgresstore.LegacySourceCopilot, provider: "copilot", authKind: core.AuthKindDevice,
		read:   store.ListLegacyCopilotCredentials,
		sealed: []string{"github_access_token", "github_refresh_token"},
	},
	{
		source: postgresstore.LegacySourceAntigravity, provider: "antigravity", authKind: core.AuthKindOAuth,
		read:   store.ListLegacyAntigravityCredentials,
		sealed: []string{"access_token", "refresh_token", "id_token"},
	},
	{
		source: postgresstore.LegacySourceAPIKeys, provider: "", authKind: core.AuthKindAPIKey,
		read:   store.ListLegacyAPIKeyCredentials,
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
	table legacyTable
	store.LegacyCredential
}

// toImport opens the row's secrets under the legacy AAD and derives the account's identity.
// (nil, nil) means the row is not an account (an OAuth marker, an unknown api_keys provider).
func (r *Repo) legacyToImport(ctx context.Context, lr legacyRow) (*postgresstore.LegacyImport, error) {
	t := lr.table
	cols := make([]string, len(t.sealed))
	for i, column := range t.sealed {
		v, err := r.accounts.OpenLegacy(ctx, lr.UserID, t.aad(lr.UserID, lr.Provider, column), lr.Sealed[i])
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
		return nil, fmt.Errorf("%s row %s has an empty credential", t.source, lr.ID)
	}

	u := core.AccountUpsert{UserID: lr.UserID, Provider: lr.Provider, AuthKind: t.authKind}
	u.Secrets.Secret = cols[0]
	if len(cols) > 1 {
		u.Secrets.Refresh = cols[1]
	}
	if len(cols) > 2 {
		u.Secrets.IDToken = cols[2]
	}
	u.ExpiresAt = lr.ExpiresAt
	placeholder := core.PlaceholderIdentityKey(lr.ID)
	u.IdentityKey = placeholder

	switch t.source {
	case postgresstore.LegacySourceClaude:
		u.ExternalAccountID, u.Email = lr.Meta[0], lr.Meta[1]
		u.OrganizationID, u.OrganizationName, u.Scope = lr.Meta[2], lr.Meta[3], lr.Meta[4]
		if u.ExternalAccountID != "" {
			u.IdentityKey = core.ClaudeIdentityKey(u.ExternalAccountID, u.OrganizationID)
		}
	case postgresstore.LegacySourceCodex:
		u.ExternalAccountID = lr.Meta[0]
		if id, ok := core.CodexIdentityFromJWT(u.Secrets.IDToken, u.Secrets.Secret); ok {
			u.IdentityKey, u.Email, u.PlanRaw, u.Plan = id.IdentityKey, id.Email, id.PlanRaw, id.Plan
			u.ExternalAccountID = id.ExternalAccountID
		}
	case postgresstore.LegacySourceCopilot:
		u.PlanRaw = lr.Meta[0]
	case postgresstore.LegacySourceAntigravity:
		u.Scope = lr.Meta[0]
		if id, ok := core.GoogleIdentityFromIDToken(u.Secrets.IDToken); ok {
			u.IdentityKey, u.Email, u.ExternalAccountID = id.IdentityKey, id.Email, id.ExternalAccountID
		}
	case postgresstore.LegacySourceAPIKeys:
		if lr.Provider == providerReliant {
			u.AuthKind, u.IdentityKey = core.AuthKindManaged, core.ManagedIdentityKey
		} else {
			u.IdentityKey = core.APIKeyIdentityKey(lr.UserID, lr.Provider, u.Secrets.Secret)
			u.KeyHint = core.KeyHint(u.Secrets.Secret)
		}
	}
	return &postgresstore.LegacyImport{Upsert: u, Source: t.source, CreatedAt: lr.CreatedAt, UpdatedAt: lr.UpdatedAt}, nil
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
	var (
		imports []postgresstore.LegacyImport
		skipped int
	)
	for _, t := range legacyTables {
		rows, err := t.read(ctx, r.ORM())
		if err != nil {
			return postgresstore.ImportResult{}, false, fmt.Errorf("reading %s: %w", t.source, err)
		}
		for _, row := range rows {
			lr := legacyRow{table: t, LegacyCredential: row}
			in, err := r.legacyToImport(ctx, lr)
			if err != nil {
				slog.WarnContext(ctx, "legacy credential skipped: could not be read", "source", t.source, "user_id", lr.UserID, "provider", lr.Provider, "error", err)
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
		slog.InfoContext(ctx, "legacy credentials copied into provider accounts",
			"created", res.Created, "updated", res.Updated, "unchanged", res.Unchanged, "failed", res.Failed)
	}
	return res, ran, nil
}
