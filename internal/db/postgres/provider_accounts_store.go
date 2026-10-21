// Copyright (c) 2025 Reliant Labs
package postgres

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Sealed account columns. Each ciphertext is bound to its account id and column, so a value
// copied to another account row, another user or another column does not open.
const (
	accountColSecret  = "secret"
	accountColRefresh = "refresh"
	accountColIDToken = "id_token"

	providerAccountsAADTable = "provider_accounts"
)

// ProviderAccountAAD is the vault AAD of one sealed column of one account.
func ProviderAccountAAD(accountID, column string) []byte {
	return []byte(providerAccountsAADTable + "\x00" + accountID + "\x00" + column)
}

// LegacyAPIKeyAAD and LegacyOAuthTokenAAD are the AADs the legacy api_keys and *_auth_tokens
// tables sealed under. They bind the table name, so the one-time copy must open and re-seal.
func LegacyAPIKeyAAD(userID, provider string) []byte { return apiKeyAAD(userID, provider) }

func LegacyOAuthTokenAAD(table, userID, column string) []byte {
	return oauthTokenAAD(table, userID, column)
}

// Legacy sources: the tables the one-time copy reads, recorded in provider_accounts.legacy_source
// on the accounts it creates so a re-run can tell an account it already made from a native one.
const (
	LegacySourceClaude      = "claude_auth_tokens"
	LegacySourceCodex       = "codex_auth_tokens"
	LegacySourceCopilot     = "copilot_auth_tokens"
	LegacySourceAntigravity = "antigravity_auth_tokens"
	LegacySourceAPIKeys     = "api_keys"
)

var errAccountVaultDisabled = errors.New("provider accounts require the credential vault; none is configured")

const accountMetaColumns = `id, user_id, provider, auth_kind, identity_key, label, email, external_account_id,
	organization_id, organization_name, plan, plan_raw, key_hint, priority, paused_at, needs_reauth_at,
	needs_reauth_reason, expires_at, scope, created_at, updated_at`

// AccountStore is the Postgres implementation of core.ProviderAccountStore. It owns its SQL and
// does not go through sqlc: the sealed columns, advisory locks and CAS loops are not query shapes
// sqlc expresses.
type AccountStore struct {
	db     *sql.DB
	sealer CredentialSealer
	now    func() time.Time
}

var _ core.ProviderAccountStore = (*AccountStore)(nil)

// NewProviderAccountStore creates the store. Without SetSealer every secret read or write fails.
func NewProviderAccountStore(db *sql.DB) *AccountStore {
	return &AccountStore{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// SetSealer enables the credential vault.
func (s *AccountStore) SetSealer(sealer CredentialSealer) { s.sealer = sealer }

// SetClock replaces the clock used for timestamps the store stamps itself. For tests.
func (s *AccountStore) SetClock(now func() time.Time) { s.now = now }

type accountRow interface{ Scan(dest ...any) error }

func scanAccount(r accountRow) (core.ProviderAccount, error) { return scanAccountWithTail(r) }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func (s *AccountStore) seal(ctx context.Context, userID, accountID, column, plaintext string) ([]byte, error) {
	if s.sealer == nil {
		return nil, errAccountVaultDisabled
	}
	if plaintext == "" {
		return nil, nil
	}
	sealed, err := s.sealer.Seal(ctx, vault.UserTenant(userID), []byte(plaintext), ProviderAccountAAD(accountID, column))
	if err != nil {
		return nil, fmt.Errorf("sealing provider account %s.%s: %w", accountID, column, err)
	}
	return sealed, nil
}

func (s *AccountStore) open(ctx context.Context, userID, accountID, column string, sealed []byte) (string, error) {
	if s.sealer == nil {
		return "", errAccountVaultDisabled
	}
	if len(sealed) == 0 {
		return "", nil
	}
	pt, err := s.sealer.Open(ctx, vault.UserTenant(userID), sealed, ProviderAccountAAD(accountID, column))
	if err != nil {
		return "", fmt.Errorf("opening provider account %s.%s: %w", accountID, column, err)
	}
	defer clear(pt)
	return string(pt), nil
}

func (s *AccountStore) sealAll(ctx context.Context, userID, accountID string, sec core.AccountSecrets) (secret, refresh, idToken []byte, err error) {
	if secret, err = s.seal(ctx, userID, accountID, accountColSecret, sec.Secret); err != nil {
		return nil, nil, nil, err
	}
	if refresh, err = s.seal(ctx, userID, accountID, accountColRefresh, sec.Refresh); err != nil {
		return nil, nil, nil, err
	}
	if idToken, err = s.seal(ctx, userID, accountID, accountColIDToken, sec.IDToken); err != nil {
		return nil, nil, nil, err
	}
	return secret, refresh, idToken, nil
}

func (s *AccountStore) openAll(ctx context.Context, a core.ProviderAccount, secret, refresh, idToken []byte) (core.AccountSecrets, error) {
	var out core.AccountSecrets
	var err error
	if out.Secret, err = s.open(ctx, a.UserID, a.ID, accountColSecret, secret); err != nil {
		return core.AccountSecrets{}, err
	}
	if out.Refresh, err = s.open(ctx, a.UserID, a.ID, accountColRefresh, refresh); err != nil {
		return core.AccountSecrets{}, err
	}
	if out.IDToken, err = s.open(ctx, a.UserID, a.ID, accountColIDToken, idToken); err != nil {
		return core.AccountSecrets{}, err
	}
	return out, nil
}

// lockProvider serializes writers of one user's accounts for one provider for the rest of tx.
func lockProvider(ctx context.Context, tx *sql.Tx, userID, provider string) error {
	name := fmt.Sprintf("provider-accounts:%d:%s:%s", len(userID), userID, provider)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, name); err != nil {
		return fmt.Errorf("lock provider accounts: %w", err)
	}
	return nil
}

func (s *AccountStore) begin(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// inTx runs f in a READ COMMITTED transaction. Writers of one (user, provider) serialize on an
// advisory lock rather than relying on serialization retries.
func (s *AccountStore) inTx(ctx context.Context, f func(tx *sql.Tx) error) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *AccountStore) ListProviderAccounts(ctx context.Context, userID string) ([]core.ProviderAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountMetaColumns+` FROM provider_accounts WHERE user_id = $1 ORDER BY provider, priority`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ProviderAccount
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *AccountStore) ListProviderAccountsWithSecrets(ctx context.Context, userID string) ([]core.AccountWithSecrets, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountMetaColumns+`, secret_sealed, refresh_sealed, id_token_sealed
		FROM provider_accounts WHERE user_id = $1 ORDER BY provider, priority`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type pending struct {
		acct                      core.ProviderAccount
		secret, refresh, idTokenB []byte
	}
	var work []pending
	for rows.Next() {
		var p pending
		a, err := scanAccountWithTail(rows, &p.secret, &p.refresh, &p.idTokenB)
		if err != nil {
			return nil, err
		}
		p.acct = a
		work = append(work, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	out := make([]core.AccountWithSecrets, 0, len(work))
	for _, p := range work {
		sec, err := s.openAll(ctx, p.acct, p.secret, p.refresh, p.idTokenB)
		if err != nil {
			// A credential that will not open must not hide the user's other accounts.
			slog.WarnContext(ctx, "provider account secret did not open; skipping it", "account_id", p.acct.ID, "provider", p.acct.Provider, "error", err)
			continue
		}
		out = append(out, core.AccountWithSecrets{ProviderAccount: p.acct, Secrets: sec})
	}
	return out, nil
}

// scanAccountWithTail scans the metadata columns followed by extra destinations.
func scanAccountWithTail(r accountRow, tail ...any) (core.ProviderAccount, error) {
	var (
		a                                                                core.ProviderAccount
		kind                                                             string
		email, extID, orgID, orgName, plan, planRaw, keyHint, reauthText sql.NullString
		scope                                                            sql.NullString
		paused, reauth, expires                                          sql.NullTime
	)
	dest := []any{&a.ID, &a.UserID, &a.Provider, &kind, &a.IdentityKey, &a.Label, &email, &extID,
		&orgID, &orgName, &plan, &planRaw, &keyHint, &a.Priority, &paused, &reauth,
		&reauthText, &expires, &scope, &a.CreatedAt, &a.UpdatedAt}
	if err := r.Scan(append(dest, tail...)...); err != nil {
		return core.ProviderAccount{}, err
	}
	a.AuthKind = core.AuthKind(kind)
	a.Email, a.ExternalAccountID = email.String, extID.String
	a.OrganizationID, a.OrganizationName = orgID.String, orgName.String
	a.Plan, a.PlanRaw, a.KeyHint = plan.String, planRaw.String, keyHint.String
	a.NeedsReauthReason, a.Scope = reauthText.String, scope.String
	if paused.Valid {
		t := paused.Time
		a.PausedAt = &t
	}
	if reauth.Valid {
		t := reauth.Time
		a.NeedsReauthAt = &t
	}
	if expires.Valid {
		a.ExpiresAt = expires.Time
	}
	return a, nil
}

func (s *AccountStore) GetProviderAccountWithSecrets(ctx context.Context, userID, accountID string) (*core.AccountWithSecrets, error) {
	var secret, refresh, idToken []byte
	row := s.db.QueryRowContext(ctx, `SELECT `+accountMetaColumns+`, secret_sealed, refresh_sealed, id_token_sealed
		FROM provider_accounts WHERE id = $1 AND user_id = $2`, accountID, userID)
	a, err := scanAccountWithTail(row, &secret, &refresh, &idToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrProviderAccountNotFound
		}
		return nil, err
	}
	sec, err := s.openAll(ctx, a, secret, refresh, idToken)
	if err != nil {
		return nil, err
	}
	return &core.AccountWithSecrets{ProviderAccount: a, Secrets: sec}, nil
}

// importMeta marks a write as the one-time copy of a legacy row: it stamps the legacy timestamps
// and source.
type importMeta struct {
	source    string
	createdAt time.Time
	updatedAt time.Time
}

func (s *AccountStore) UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error) {
	if err := validateUpsert(u); err != nil {
		return core.ProviderAccount{}, false, err
	}
	var (
		acct    core.ProviderAccount
		created bool
	)
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		acct, created, err = s.upsertTx(ctx, tx, u)
		return err
	})
	if err != nil {
		return core.ProviderAccount{}, false, err
	}
	return acct, created, nil
}

func validateUpsert(u core.AccountUpsert) error {
	switch {
	case u.UserID == "":
		return errors.New("user_id cannot be empty")
	case u.Provider == "":
		return errors.New("provider cannot be empty")
	case u.IdentityKey == "":
		return errors.New("identity_key cannot be empty")
	case !u.AuthKind.Valid():
		return fmt.Errorf("invalid auth_kind %q", u.AuthKind)
	case u.Secrets.Secret == "":
		return errors.New("account secret cannot be empty")
	case u.AuthKind == core.AuthKindManaged && u.IdentityKey != core.ManagedIdentityKey:
		return fmt.Errorf("a managed account's identity must be %q", core.ManagedIdentityKey)
	}
	return nil
}

func selectAccountByIdentity(ctx context.Context, tx *sql.Tx, userID, provider, identity string) (*core.ProviderAccount, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+accountMetaColumns+` FROM provider_accounts
		WHERE user_id = $1 AND provider = $2 AND identity_key = $3 FOR UPDATE`, userID, provider, identity)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func selectAccountByID(ctx context.Context, tx *sql.Tx, userID, accountID string) (*core.ProviderAccount, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+accountMetaColumns+` FROM provider_accounts
		WHERE id = $1 AND user_id = $2 FOR UPDATE`, accountID, userID)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrProviderAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// upsertTx is the one write path for connecting an account: a fresh sign-in and the rewrite of a
// placeholder identity after a refresh.
func (s *AccountStore) upsertTx(ctx context.Context, tx *sql.Tx, u core.AccountUpsert) (acct core.ProviderAccount, created bool, err error) {
	if err := lockProvider(ctx, tx, u.UserID, u.Provider); err != nil {
		return core.ProviderAccount{}, false, err
	}

	// Only a placeholder is ever rewritten: a real identity is who the account IS.
	if u.ReplaceIdentityOf != "" && u.ReplaceIdentityOf != u.IdentityKey && core.IsPlaceholderIdentityKey(u.ReplaceIdentityOf) {
		old, err := selectAccountByIdentity(ctx, tx, u.UserID, u.Provider, u.ReplaceIdentityOf)
		if err != nil {
			return core.ProviderAccount{}, false, err
		}
		if old != nil {
			acct, err := s.renameTx(ctx, tx, *old, u)
			return acct, false, err
		}
	}

	existing, err := selectAccountByIdentity(ctx, tx, u.UserID, u.Provider, u.IdentityKey)
	if err != nil {
		return core.ProviderAccount{}, false, err
	}
	if existing == nil {
		acct, err := s.insertTx(ctx, tx, u, nil)
		return acct, true, err
	}
	acct, err = s.updateTx(ctx, tx, *existing, u, u.IdentityKey, nil)
	return acct, false, err
}

func selectMirror(ctx context.Context, tx *sql.Tx, userID, provider, source string) (*core.ProviderAccount, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+accountMetaColumns+` FROM provider_accounts
		WHERE user_id = $1 AND provider = $2 AND legacy_source = $3 ORDER BY priority LIMIT 1 FOR UPDATE`, userID, provider, source)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// importTx copies one legacy row. It never merges and never deletes: an account that already
// holds the row's identity is left alone, and a row whose provider already has an account this
// copy made from the same table (an earlier run, or an earlier release's mirror, whose identity
// a refresh has since rewritten) is that account, not a new one. Only such a mirror is ever
// overwritten, and only by a strictly newer legacy write: an account the user connected through
// the new code has no legacy_source and is untouchable. A row that cannot be matched becomes a
// new account, because a duplicate is harmless (the next refresh merges it into the real
// identity) and a missing one is a user signed out.
func (s *AccountStore) importTx(ctx context.Context, tx *sql.Tx, u core.AccountUpsert, imp importMeta) (created, applied bool, err error) {
	if err := lockProvider(ctx, tx, u.UserID, u.Provider); err != nil {
		return false, false, err
	}
	existing, err := selectAccountByIdentity(ctx, tx, u.UserID, u.Provider, u.IdentityKey)
	if err != nil {
		return false, false, err
	}
	if existing == nil {
		if existing, err = selectMirror(ctx, tx, u.UserID, u.Provider, imp.source); err != nil {
			return false, false, err
		}
	}
	if existing == nil {
		if _, err := s.insertTx(ctx, tx, u, &imp); err != nil {
			return false, false, err
		}
		return true, true, nil
	}
	var src sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT legacy_source FROM provider_accounts WHERE id = $1`, existing.ID).Scan(&src); err != nil {
		return false, false, err
	}
	if !src.Valid || !existing.UpdatedAt.Before(imp.updatedAt) {
		return false, false, nil
	}
	identity := u.IdentityKey
	if core.IsPlaceholderIdentityKey(identity) {
		identity = existing.IdentityKey // a refresh already learned the real identity; the legacy row has not
	}
	if _, err := s.updateTx(ctx, tx, *existing, u, identity, &imp); err != nil {
		return false, false, err
	}
	return false, true, nil
}

func (s *AccountStore) insertTx(ctx context.Context, tx *sql.Tx, u core.AccountUpsert, imp *importMeta) (core.ProviderAccount, error) {
	id := uuid.New().String()
	secret, refresh, idToken, err := s.sealAll(ctx, u.UserID, id, u.Secrets)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	var priority int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(priority), -1) + 1 FROM provider_accounts WHERE user_id = $1 AND provider = $2`,
		u.UserID, u.Provider).Scan(&priority); err != nil {
		return core.ProviderAccount{}, err
	}
	now := s.now()
	created, updated := now, now
	var legacySource any
	if imp != nil {
		created, updated, legacySource = imp.createdAt, imp.updatedAt, imp.source
	}
	row := tx.QueryRowContext(ctx, `INSERT INTO provider_accounts (
			id, user_id, provider, auth_kind, identity_key, email, external_account_id, organization_id,
			organization_name, plan, plan_raw, key_hint, priority, secret_sealed, refresh_sealed,
			id_token_sealed, expires_at, scope, legacy_source, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		RETURNING `+accountMetaColumns,
		id, u.UserID, u.Provider, string(u.AuthKind), u.IdentityKey, nullable(u.Email), nullable(u.ExternalAccountID),
		nullable(u.OrganizationID), nullable(u.OrganizationName), nullable(u.Plan), nullable(u.PlanRaw),
		nullable(u.KeyHint), priority, secret, refresh, idToken, nullableTime(u.ExpiresAt), nullable(u.Scope),
		legacySource, created.UTC(), updated.UTC())
	return scanAccount(row)
}

// updateTx rewrites an existing account in place with a fresh credential. Identity metadata that
// the new write does not carry is kept; the refresh and id tokens likewise (a re-consent that
// omits a refresh token must not erase the one we hold). Label, priority and pause are the
// user's and are never touched here.
func (s *AccountStore) updateTx(ctx context.Context, tx *sql.Tx, cur core.ProviderAccount, u core.AccountUpsert, identity string, imp *importMeta) (core.ProviderAccount, error) {
	secret, refresh, idToken, err := s.sealAll(ctx, cur.UserID, cur.ID, u.Secrets)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	updated := s.now()
	var legacySource any
	if imp != nil {
		updated, legacySource = imp.updatedAt, imp.source
	}
	row := tx.QueryRowContext(ctx, `UPDATE provider_accounts SET
			identity_key = $3,
			auth_kind = $4,
			email = COALESCE($5, email),
			external_account_id = COALESCE($6, external_account_id),
			organization_id = COALESCE($7, organization_id),
			organization_name = COALESCE($8, organization_name),
			plan = COALESCE($9, plan),
			plan_raw = COALESCE($10, plan_raw),
			key_hint = COALESCE($11, key_hint),
			secret_sealed = $12,
			refresh_sealed = COALESCE($13, refresh_sealed),
			id_token_sealed = COALESCE($14, id_token_sealed),
			expires_at = $15,
			scope = COALESCE($16, scope),
			needs_reauth_at = NULL,
			needs_reauth_reason = NULL,
			legacy_source = COALESCE($17, legacy_source),
			updated_at = $18
		WHERE id = $1 AND user_id = $2
		RETURNING `+accountMetaColumns,
		cur.ID, cur.UserID, identity, string(u.AuthKind), nullable(u.Email), nullable(u.ExternalAccountID),
		nullable(u.OrganizationID), nullable(u.OrganizationName), nullable(u.Plan), nullable(u.PlanRaw),
		nullable(u.KeyHint), secret, refresh, idToken, nullableTime(u.ExpiresAt), nullable(u.Scope),
		legacySource, updated.UTC())
	return scanAccount(row)
}

// renameTx teaches a placeholder account (old) its real identity, u.IdentityKey.
//
// If no other account owns that identity the placeholder row is rewritten in place. If one does,
// the two are the same upstream account and they merge, and the merge ALWAYS keeps the account
// that already held the real identity (target) and deletes the placeholder. That account is the
// one with a distinct, known identity, so it is the one the user's label, pause, limit state,
// poll schedule and thread assignments may already belong to; deleting it (as a newest-wins rule
// once did) lost all of that. What carries over from the placeholder is only what the target
// lacks: the smaller priority, a label, a pause, its thread assignments. The tokens written are
// u's, the refresh response that taught us the identity, which are newer than either row.
func (s *AccountStore) renameTx(ctx context.Context, tx *sql.Tx, old core.ProviderAccount, u core.AccountUpsert) (core.ProviderAccount, error) {
	target, err := selectAccountByIdentity(ctx, tx, u.UserID, u.Provider, u.IdentityKey)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	if target == nil {
		return s.updateTx(ctx, tx, old, u, u.IdentityKey, nil)
	}
	if target.ID == old.ID {
		return s.updateTx(ctx, tx, old, u, u.IdentityKey, nil)
	}
	if core.IsPlaceholderIdentityKey(target.IdentityKey) {
		return core.ProviderAccount{}, fmt.Errorf("refusing to merge into placeholder identity %q", target.IdentityKey)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE thread_provider_accounts t SET account_id = $1
		WHERE t.account_id = $2 AND NOT EXISTS (
			SELECT 1 FROM thread_provider_accounts o
			WHERE o.thread_id = t.thread_id AND o.provider = t.provider AND o.account_id = $1)`, target.ID, old.ID); err != nil {
		return core.ProviderAccount{}, err
	}
	priority := min(target.Priority, old.Priority)
	label := target.Label
	if label == "" {
		label = old.Label
	}
	paused := target.PausedAt
	if paused == nil {
		paused = old.PausedAt
	}
	// Free the identity bookkeeping before the target takes the smaller priority.
	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_accounts WHERE id = $1`, old.ID); err != nil {
		return core.ProviderAccount{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_accounts SET priority = $2, label = $3, paused_at = $4 WHERE id = $1`,
		target.ID, priority, label, ptrTime(paused)); err != nil {
		return core.ProviderAccount{}, err
	}
	target.Priority, target.Label, target.PausedAt = priority, label, paused
	acct, err := s.updateTx(ctx, tx, *target, u, u.IdentityKey, nil)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	if err := densifyTx(ctx, tx, u.UserID, u.Provider); err != nil {
		return core.ProviderAccount{}, err
	}
	return acct, nil
}

// densifyTx renumbers a provider's priorities to 0..n-1, keeping their order. The unique
// constraint on priority is deferred, so the intermediate states are allowed.
func densifyTx(ctx context.Context, tx *sql.Tx, userID, provider string) error {
	_, err := tx.ExecContext(ctx, `UPDATE provider_accounts p SET priority = r.rn
		FROM (SELECT id, (row_number() OVER (ORDER BY priority, created_at, id) - 1)::int AS rn
		      FROM provider_accounts WHERE user_id = $1 AND provider = $2) r
		WHERE p.id = r.id AND p.priority <> r.rn`, userID, provider)
	return err
}

// maxSealedSwapAttempts bounds how often a compare-and-swap re-reads after another writer
// changed the row between its read and its update.
const maxAccountSwapAttempts = 3

// CompareAndSwapAccountTokens persists next only if the stored refresh token still equals
// expectedRefresh. Sealing is randomized, so the comparison happens here on the opened value, and
// the UPDATE is conditioned on the exact ciphertexts that were read (the witness). Every write
// re-seals the secret, so the witness changes whenever the row does: of two racers holding the
// same expectation exactly one update matches.
func (s *AccountStore) CompareAndSwapAccountTokens(ctx context.Context, userID, accountID, expectedRefresh string, next core.AccountTokens) (bool, error) {
	if next.Secret == "" {
		return false, errors.New("account secret cannot be empty")
	}
	secret, refresh, idToken, err := s.sealAll(ctx, userID, accountID, core.AccountSecrets{Secret: next.Secret, Refresh: next.Refresh, IDToken: next.IDToken})
	if err != nil {
		return false, err
	}
	for range maxAccountSwapAttempts {
		var wSecret, wRefresh []byte
		err := s.db.QueryRowContext(ctx, `SELECT secret_sealed, refresh_sealed FROM provider_accounts WHERE id = $1 AND user_id = $2`,
			accountID, userID).Scan(&wSecret, &wRefresh)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		stored, err := s.open(ctx, userID, accountID, accountColRefresh, wRefresh)
		if err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare([]byte(stored), []byte(expectedRefresh)) != 1 {
			return false, nil
		}
		res, err := s.db.ExecContext(ctx, `UPDATE provider_accounts SET
				secret_sealed = $3,
				refresh_sealed = COALESCE($4, refresh_sealed),
				id_token_sealed = COALESCE($5, id_token_sealed),
				expires_at = $6,
				email = COALESCE($7, email),
				organization_name = COALESCE($8, organization_name),
				plan = COALESCE($9, plan),
				plan_raw = COALESCE($10, plan_raw),
				updated_at = $11
			WHERE id = $1 AND user_id = $2 AND secret_sealed = $12 AND refresh_sealed IS NOT DISTINCT FROM $13`,
			accountID, userID, secret, refresh, idToken, nullableTime(next.ExpiresAt),
			nullable(next.Email), nullable(next.OrganizationName), nullable(next.Plan), nullable(next.PlanRaw),
			s.now(), wSecret, wRefresh)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (s *AccountStore) PatchProviderAccount(ctx context.Context, userID, accountID string, p core.AccountPatch) (core.ProviderAccount, error) {
	var out core.ProviderAccount
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := selectAccountByID(ctx, tx, userID, accountID)
		if err != nil {
			return err
		}
		now := s.now()
		label := cur.Label
		if p.Label != nil {
			label = *p.Label
		}
		paused := cur.PausedAt
		if p.Paused != nil {
			switch {
			case *p.Paused && paused == nil:
				paused = &now
			case !*p.Paused:
				paused = nil
			}
		}
		reauthAt, reauthReason := cur.NeedsReauthAt, cur.NeedsReauthReason
		if p.NeedsReauth != nil {
			if *p.NeedsReauth == "" {
				reauthAt, reauthReason = nil, ""
			} else {
				if reauthAt == nil {
					reauthAt = &now
				}
				reauthReason = *p.NeedsReauth
			}
		}
		row := tx.QueryRowContext(ctx, `UPDATE provider_accounts SET label = $3, paused_at = $4, needs_reauth_at = $5,
				needs_reauth_reason = $6, updated_at = $7
			WHERE id = $1 AND user_id = $2 RETURNING `+accountMetaColumns,
			accountID, userID, label, ptrTime(paused), ptrTime(reauthAt), nullable(reauthReason), now)
		out, err = scanAccount(row)
		return err
	})
	return out, err
}

func ptrTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func (s *AccountStore) ReorderProviderAccounts(ctx context.Context, userID, provider string, orderedIDs []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := lockProvider(ctx, tx, userID, provider); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM provider_accounts WHERE user_id = $1 AND provider = $2 FOR UPDATE`, userID, provider)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			have[id] = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		if len(orderedIDs) != len(have) {
			return core.ErrInvalidAccountOrder
		}
		seen := map[string]bool{}
		for _, id := range orderedIDs {
			if !have[id] || seen[id] {
				return core.ErrInvalidAccountOrder
			}
			seen[id] = true
		}
		now := s.now()
		for i, id := range orderedIDs {
			if _, err := tx.ExecContext(ctx, `UPDATE provider_accounts SET priority = $2, updated_at = $3 WHERE id = $1`, id, i, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *AccountStore) DeleteProviderAccount(ctx context.Context, userID, accountID string) error {
	var provider string
	err := s.db.QueryRowContext(ctx, `SELECT provider FROM provider_accounts WHERE id = $1 AND user_id = $2`, accountID, userID).Scan(&provider)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrProviderAccountNotFound
	}
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := lockProvider(ctx, tx, userID, provider); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM provider_accounts WHERE id = $1 AND user_id = $2`, accountID, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return core.ErrProviderAccountNotFound
		}
		return densifyTx(ctx, tx, userID, provider)
	})
}

// ---- limit state ----

// epoch stands in for "never observed" in the NOT NULL observed_at column.
var epoch = time.Unix(0, 0).UTC()

func encodeWindows(ws []core.LimitWindow) ([]byte, error) {
	if ws == nil {
		ws = []core.LimitWindow{}
	}
	return json.Marshal(ws)
}

func (s *AccountStore) ListAccountLimits(ctx context.Context, userID string) (map[string]core.LimitState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT account_id, user_id, status, windows, representative_window, source, observed_at,
			limited_until, limited_window, limited_probes, usage_api_next_at, usage_api_failures, usage_api_error, last_traffic_at
		FROM provider_account_limits WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]core.LimitState{}
	for rows.Next() {
		l, err := scanLimitState(rows)
		if err != nil {
			return nil, err
		}
		out[l.AccountID] = l
	}
	return out, rows.Err()
}

// scanLimitState reads one provider_account_limits row in limitColumns order.
func scanLimitState(r accountRow) (core.LimitState, error) {
	var (
		l                                core.LimitState
		windows                          []byte
		repr, limitedWindow, usageAPIErr sql.NullString
		limitedUntil, nextAt, traffic    sql.NullTime
	)
	if err := r.Scan(&l.AccountID, &l.UserID, &l.Status, &windows, &repr, &l.Source, &l.ObservedAt,
		&limitedUntil, &limitedWindow, &l.LimitedProbes, &nextAt, &l.UsageAPIFailures, &usageAPIErr, &traffic); err != nil {
		return core.LimitState{}, err
	}
	if err := json.Unmarshal(windows, &l.Windows); err != nil {
		return core.LimitState{}, fmt.Errorf("decoding limit windows of account %s: %w", l.AccountID, err)
	}
	l.RepresentativeWindow, l.LimitedWindow, l.UsageAPIError = repr.String, limitedWindow.String, usageAPIErr.String
	if limitedUntil.Valid {
		t := limitedUntil.Time
		l.LimitedUntil = &t
	}
	if nextAt.Valid {
		t := nextAt.Time
		l.UsageAPINextAt = &t
	}
	if traffic.Valid {
		l.LastTrafficAt = traffic.Time
	}
	if !l.ObservedAt.After(epoch) {
		l.ObservedAt = time.Time{}
	}
	return l, nil
}

// GetAccountLimits returns one account's limit state, or (nil, nil) when it has none. Unlike
// ListAccountLimits it is not scoped to a user: the usage poller leases bare account ids.
func (s *AccountStore) GetAccountLimits(ctx context.Context, accountID string) (*core.LimitState, error) {
	l, err := scanLimitState(s.db.QueryRowContext(ctx, `SELECT account_id, user_id, status, windows, representative_window, source, observed_at,
			limited_until, limited_window, limited_probes, usage_api_next_at, usage_api_failures, usage_api_error, last_traffic_at
		FROM provider_account_limits WHERE account_id = $1`, accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// EnsureUsagePollScheduled schedules a usage poll for the account only when none is scheduled and
// its provider is one of providers. It never moves an existing schedule, so a poll that is backing
// off stays backed off while traffic keeps arriving, and it keeps the failure count (unlike
// ScheduleUsagePoll). An absent account, or one whose limits row does not exist yet, is a no-op.
func (s *AccountStore) EnsureUsagePollScheduled(ctx context.Context, accountID string, next time.Time, providers []string) error {
	if next.IsZero() {
		return errors.New("a poll time is required")
	}
	if len(providers) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE provider_account_limits l SET usage_api_next_at = $2, updated_at = $4
		FROM provider_accounts a
		WHERE l.account_id = $1 AND a.id = l.account_id AND a.provider = ANY($3::text[]) AND l.usage_api_next_at IS NULL`,
		accountID, next.UTC(), pq.Array(providers), s.now())
	return err
}

// ClaimUsageFetch records that the caller is about to call the account's usage API and reports
// whether it may: false when another caller claimed it within the last floor. The claim is one
// atomic statement, so replicas share the floor. An absent account claims nothing.
func (s *AccountStore) ClaimUsageFetch(ctx context.Context, accountID string, now time.Time, floor time.Duration) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO provider_account_limits
			(account_id, user_id, status, windows, source, observed_at, usage_polled_at, updated_at)
		SELECT id, user_id, 'unknown', '[]'::jsonb, 'usage_api', $2, $3, $3 FROM provider_accounts WHERE id = $1
		ON CONFLICT (account_id) DO UPDATE SET usage_polled_at = EXCLUDED.usage_polled_at, updated_at = EXCLUDED.updated_at
		WHERE provider_account_limits.usage_polled_at IS NULL OR provider_account_limits.usage_polled_at <= $4`,
		accountID, epoch, now.UTC(), now.Add(-floor).UTC())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *AccountStore) UpsertAccountLimitSnapshot(ctx context.Context, snap core.LimitState) error {
	if snap.AccountID == "" || snap.UserID == "" {
		return errors.New("account_id and user_id are required")
	}
	if snap.ObservedAt.IsZero() {
		return errors.New("observed_at is required")
	}
	windows, err := encodeWindows(snap.Windows)
	if err != nil {
		return err
	}
	status := snap.Status
	if status == "" {
		status = "unknown"
	}
	source := snap.Source
	if source == "" {
		source = "headers"
	}
	// Monotonic in observed_at, so several processes never regress it. An "allowed" snapshot newer
	// than the stored state means the provider is serving again: it lifts the bench early. It
	// never touches the poll schedule. A snapshot for a deleted account inserts nothing.
	_, err = s.db.ExecContext(ctx, `INSERT INTO provider_account_limits
			(account_id, user_id, status, windows, representative_window, source, observed_at, last_traffic_at, updated_at)
		SELECT id, user_id, $3, $4::jsonb, $5, $6::text, $7, CASE WHEN $6::text = 'headers' THEN $7::timestamptz END, $8
		FROM provider_accounts WHERE id = $1 AND user_id = $2
		ON CONFLICT (account_id) DO UPDATE SET
			last_traffic_at = CASE WHEN EXCLUDED.source = 'headers' THEN EXCLUDED.observed_at ELSE provider_account_limits.last_traffic_at END,
			status = EXCLUDED.status,
			windows = EXCLUDED.windows,
			representative_window = EXCLUDED.representative_window,
			source = EXCLUDED.source,
			observed_at = EXCLUDED.observed_at,
			limited_until = CASE WHEN EXCLUDED.status = 'allowed' THEN NULL ELSE provider_account_limits.limited_until END,
			limited_window = CASE WHEN EXCLUDED.status = 'allowed' THEN NULL ELSE provider_account_limits.limited_window END,
			limited_probes = CASE WHEN EXCLUDED.status = 'allowed' THEN 0 ELSE provider_account_limits.limited_probes END,
			updated_at = EXCLUDED.updated_at
		WHERE provider_account_limits.observed_at < EXCLUDED.observed_at`,
		snap.AccountID, snap.UserID, status, string(windows), nullable(snap.RepresentativeWindow), source, snap.ObservedAt.UTC(), s.now())
	return err
}

func (s *AccountStore) BenchAccount(ctx context.Context, userID, accountID string, until time.Time, window string, probe bool) error {
	if until.IsZero() {
		return errors.New("bench needs a time; pass the probe time for an unknown reset")
	}
	probes := 0
	if probe {
		probes = 1
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO provider_account_limits
			(account_id, user_id, status, windows, source, observed_at, limited_until, limited_window, limited_probes, updated_at)
		SELECT id, user_id, 'rejected', '[]'::jsonb, 'usage_limit_error', $3, $4, $5, $6, $3
		FROM provider_accounts WHERE id = $1 AND user_id = $2
		ON CONFLICT (account_id) DO UPDATE SET
			status = 'rejected',
			source = 'usage_limit_error',
			observed_at = GREATEST(provider_account_limits.observed_at, EXCLUDED.observed_at),
			limited_window = CASE WHEN EXCLUDED.limited_until >= COALESCE(provider_account_limits.limited_until, '-infinity'::timestamptz)
				THEN EXCLUDED.limited_window ELSE provider_account_limits.limited_window END,
			limited_until = GREATEST(provider_account_limits.limited_until, EXCLUDED.limited_until),
			limited_probes = CASE WHEN $7 THEN provider_account_limits.limited_probes + 1 ELSE 0 END,
			updated_at = EXCLUDED.updated_at`,
		accountID, userID, now, until.UTC(), nullable(window), probes, probe)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrProviderAccountNotFound
	}
	return nil
}

func (s *AccountStore) LeaseUsagePolls(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `UPDATE provider_account_limits SET usage_api_next_at = $2
		WHERE account_id IN (
			SELECT account_id FROM provider_account_limits
			WHERE usage_api_next_at IS NOT NULL AND usage_api_next_at <= $1
			ORDER BY usage_api_next_at LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING account_id`, now.UTC(), now.Add(lease).UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *AccountStore) ScheduleUsagePoll(ctx context.Context, accountID string, next time.Time, failures int, errText string) error {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO provider_account_limits
			(account_id, user_id, status, windows, source, observed_at, usage_api_next_at, usage_api_failures, usage_api_error, updated_at)
		SELECT id, user_id, 'unknown', '[]'::jsonb, 'usage_api', $2, $3, $4, $5, $6 FROM provider_accounts WHERE id = $1
		ON CONFLICT (account_id) DO UPDATE SET
			usage_api_next_at = EXCLUDED.usage_api_next_at,
			usage_api_failures = EXCLUDED.usage_api_failures,
			usage_api_error = EXCLUDED.usage_api_error,
			updated_at = EXCLUDED.updated_at`,
		accountID, epoch, nullableTime(next), failures, nullable(errText), now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrProviderAccountNotFound
	}
	return nil
}

// ---- thread assignments ----

func (s *AccountStore) GetThreadAssignment(ctx context.Context, threadID, provider string) (*core.Assignment, error) {
	var a core.Assignment
	err := s.db.QueryRowContext(ctx, `SELECT thread_id, provider, account_id, assigned_at, reason
		FROM thread_provider_accounts WHERE thread_id = $1 AND provider = $2`, threadID, provider).
		Scan(&a.ThreadID, &a.Provider, &a.AccountID, &a.AssignedAt, &a.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// LatestServingAccount returns the account that served the thread's newest assistant message
// that names one, or "" when none does. Unlike a sticky assignment it follows the provider the
// thread last ran on, so it is right after a cross-provider fallback returns.
func (s *AccountStore) LatestServingAccount(ctx context.Context, threadID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT provider_account_id FROM messages
		WHERE thread_id = $1 AND provider_account_id IS NOT NULL
		ORDER BY seq DESC LIMIT 1`, threadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ListThreadAssignments returns the thread's sticky account for every provider it has used,
// most recently assigned first.
func (s *AccountStore) ListThreadAssignments(ctx context.Context, threadID string) ([]core.Assignment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id, provider, account_id, assigned_at, reason
		FROM thread_provider_accounts WHERE thread_id = $1 ORDER BY assigned_at DESC, provider`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Assignment
	for rows.Next() {
		var a core.Assignment
		if err := rows.Scan(&a.ThreadID, &a.Provider, &a.AccountID, &a.AssignedAt, &a.Reason); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *AccountStore) SetThreadAssignment(ctx context.Context, a core.Assignment, expectedAccountID string) (bool, error) {
	if a.ThreadID == "" || a.Provider == "" || a.AccountID == "" {
		return false, errors.New("thread_id, provider and account_id are required")
	}
	if a.AssignedAt.IsZero() {
		a.AssignedAt = s.now()
	}
	var (
		res sql.Result
		err error
	)
	if expectedAccountID == "" {
		res, err = s.db.ExecContext(ctx, `INSERT INTO thread_provider_accounts (thread_id, provider, account_id, assigned_at, reason)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (thread_id, provider) DO NOTHING`,
			a.ThreadID, a.Provider, a.AccountID, a.AssignedAt.UTC(), a.Reason)
	} else {
		res, err = s.db.ExecContext(ctx, `UPDATE thread_provider_accounts SET account_id = $3, assigned_at = $4, reason = $5
			WHERE thread_id = $1 AND provider = $2 AND account_id = $6`,
			a.ThreadID, a.Provider, a.AccountID, a.AssignedAt.UTC(), a.Reason, expectedAccountID)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *AccountStore) RecentAssignmentCounts(ctx context.Context, accountIDs []string, since time.Time) (map[string]int, error) {
	out := map[string]int{}
	if len(accountIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT account_id, count(*) FROM thread_provider_accounts
		WHERE account_id = ANY($1::text[]) AND assigned_at >= $2 GROUP BY account_id`, pq.Array(accountIDs), since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// LegacyImport is one credential read from a legacy table, ready to copy.
type LegacyImport struct {
	Upsert    core.AccountUpsert
	Source    string // a LegacySource* constant
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ImportResult counts what the one-time legacy copy did.
type ImportResult struct{ Created, Updated, Unchanged, Failed int }

const legacyCopyLockName = "provider_accounts_legacy_copy"

// LegacyCopyDone reports whether the one-time copy has completed. A cheap read, so a boot that
// has nothing to do opens no secret.
func (s *AccountStore) LegacyCopyDone(ctx context.Context) (bool, error) {
	var done bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM legacy_credential_copy)`).Scan(&done)
	return done, err
}

// ImportLegacy copies the legacy credentials into provider_accounts ONCE and records that it did.
// It runs in one transaction under a global advisory lock, and re-checks the completion marker
// under that lock, so boots racing each other (API server, worker, replicas) import exactly once:
// the losers find the marker and return ran=false. The marker commits with the accounts, so a
// crash leaves neither. A row that fails to write is rolled back to a savepoint, counted and
// logged: one bad credential never blocks the rest or the boot, and the count is kept in the
// marker so whoever drops the legacy tables can see whether anything was left behind.
func (s *AccountStore) ImportLegacy(ctx context.Context, imports []LegacyImport, skipped int) (res ImportResult, ran bool, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, legacyCopyLockName); err != nil {
			return fmt.Errorf("lock legacy credential copy: %w", err)
		}
		var done bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM legacy_credential_copy)`).Scan(&done); err != nil {
			return err
		}
		if done {
			return nil
		}
		res = ImportResult{Failed: skipped}
		for _, in := range imports {
			if err := validateUpsert(in.Upsert); err != nil {
				slog.WarnContext(ctx, "legacy credential skipped: invalid", "source", in.Source, "user_id", in.Upsert.UserID, "provider", in.Upsert.Provider, "error", err)
				res.Failed++
				continue
			}
			if _, err := tx.ExecContext(ctx, `SAVEPOINT legacy_import`); err != nil {
				return err
			}
			created, applied, err := s.importTx(ctx, tx, in.Upsert, importMeta{source: in.Source, createdAt: in.CreatedAt, updatedAt: in.UpdatedAt})
			if err != nil {
				if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT legacy_import`); rbErr != nil {
					return rbErr
				}
				slog.WarnContext(ctx, "legacy credential skipped: write failed", "source", in.Source, "user_id", in.Upsert.UserID, "provider", in.Upsert.Provider, "error", err)
				res.Failed++
				continue
			}
			if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT legacy_import`); err != nil {
				return err
			}
			switch {
			case created:
				res.Created++
			case applied:
				res.Updated++
			default:
				res.Unchanged++
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_credential_copy (id, completed_at, created, skipped, failed)
			VALUES (1, $1, $2, $3, $4)`, s.now(), res.Created+res.Updated, res.Unchanged, res.Failed); err != nil {
			return err
		}
		ran = true
		return nil
	})
	if err != nil {
		return ImportResult{}, false, err
	}
	return res, ran, nil
}

// OpenLegacy opens a value sealed by a legacy table under its old AAD.
func (s *AccountStore) OpenLegacy(ctx context.Context, userID string, aad, sealed []byte) (string, error) {
	if s.sealer == nil {
		return "", errAccountVaultDisabled
	}
	if len(sealed) == 0 {
		return "", errors.New("legacy value has no sealed ciphertext")
	}
	pt, err := s.sealer.Open(ctx, vault.UserTenant(userID), sealed, aad)
	if err != nil {
		return "", err
	}
	defer clear(pt)
	return string(pt), nil
}
