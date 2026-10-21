// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
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

// accountStore is the Postgres implementation of core.ProviderAccountStore. Its statements are
// internal/store's (provider_account*_repo_ext.go, thread_provider_account_repo_ext.go,
// legacy_credential_copy_repo_ext.go); what stays here is everything that is not SQL: sealing
// and opening the secret columns, the advisory-lock-then-read-then-write transactions, the
// identity merge, and the compare-and-swap loop.
type accountStore struct {
	db     orm.Context
	sealer CredentialSealer
	now    func() time.Time
}

var _ core.ProviderAccountStore = (*accountStore)(nil)

// newAccountStore creates the store. Without SetSealer every secret read or write fails.
func newAccountStore(db orm.Context) *accountStore {
	return &accountStore{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// SetSealer enables the credential vault.
func (s *accountStore) SetSealer(sealer CredentialSealer) { s.sealer = sealer }

// SetClock replaces the clock used for timestamps the store stamps itself. For tests.
func (s *accountStore) SetClock(now func() time.Time) { s.now = now }

func (s *accountStore) seal(ctx context.Context, userID, accountID, column, plaintext string) ([]byte, error) {
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

func (s *accountStore) open(ctx context.Context, userID, accountID, column string, sealed []byte) (string, error) {
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

func (s *accountStore) sealAll(ctx context.Context, userID, accountID string, sec core.AccountSecrets) (secret, refresh, idToken []byte, err error) {
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

func (s *accountStore) openAll(ctx context.Context, a store.SealedProviderAccount) (core.AccountSecrets, error) {
	var out core.AccountSecrets
	var err error
	if out.Secret, err = s.open(ctx, a.UserID, a.ID, accountColSecret, a.Secret); err != nil {
		return core.AccountSecrets{}, err
	}
	if out.Refresh, err = s.open(ctx, a.UserID, a.ID, accountColRefresh, a.Refresh); err != nil {
		return core.AccountSecrets{}, err
	}
	if out.IDToken, err = s.open(ctx, a.UserID, a.ID, accountColIDToken, a.IDToken); err != nil {
		return core.AccountSecrets{}, err
	}
	return out, nil
}

// inTx runs f in a READ COMMITTED transaction, or in the one ctx already carries. Writers of one
// (user, provider) serialize on an advisory lock rather than relying on serialization retries,
// and a transaction is never retried: f seals with fresh randomness and reads under its locks,
// so it is only correct as the one attempt.
func (s *accountStore) inTx(ctx context.Context, f func(ctx context.Context) error) error {
	return s.db.RunTx(ctx, orm.TxOptions{Isolation: orm.ReadCommitted, MaxAttempts: 1}, f)
}

func (s *accountStore) ListProviderAccounts(ctx context.Context, userID string) ([]core.ProviderAccount, error) {
	return store.ListUserProviderAccounts(ctx, s.db, userID)
}

func (s *accountStore) ListProviderAccountsWithSecrets(ctx context.Context, userID string) ([]core.AccountWithSecrets, error) {
	rows, err := store.ListUserProviderAccountsSealed(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	out := make([]core.AccountWithSecrets, 0, len(rows))
	for _, r := range rows {
		sec, err := s.openAll(ctx, r)
		if err != nil {
			// A credential that will not open must not hide the user's other accounts.
			slog.WarnContext(ctx, "provider account secret did not open; skipping it", "account_id", r.ID, "provider", r.Provider, "error", err)
			continue
		}
		out = append(out, core.AccountWithSecrets{ProviderAccount: r.ProviderAccount, Secrets: sec})
	}
	return out, nil
}

func (s *accountStore) GetProviderAccountWithSecrets(ctx context.Context, userID, accountID string) (*core.AccountWithSecrets, error) {
	r, err := store.GetUserProviderAccountSealed(ctx, s.db, userID, accountID)
	if err != nil {
		return nil, err
	}
	sec, err := s.openAll(ctx, *r)
	if err != nil {
		return nil, err
	}
	return &core.AccountWithSecrets{ProviderAccount: r.ProviderAccount, Secrets: sec}, nil
}

// importMeta marks a write as the one-time copy of a legacy row: it stamps the legacy timestamps
// and source.
type importMeta struct {
	source    string
	createdAt time.Time
	updatedAt time.Time
}

func (s *accountStore) UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error) {
	if err := validateUpsert(u); err != nil {
		return core.ProviderAccount{}, false, err
	}
	var (
		acct    core.ProviderAccount
		created bool
	)
	err := s.inTx(ctx, func(ctx context.Context) error {
		var err error
		acct, created, err = s.upsertTx(ctx, u)
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

// upsertTx is the one write path for connecting an account: a fresh sign-in and the rewrite of a
// placeholder identity after a refresh.
func (s *accountStore) upsertTx(ctx context.Context, u core.AccountUpsert) (acct core.ProviderAccount, created bool, err error) {
	if err := store.LockUserProviderAccounts(ctx, s.db, u.UserID, u.Provider); err != nil {
		return core.ProviderAccount{}, false, err
	}

	// Only a placeholder is ever rewritten: a real identity is who the account IS.
	if u.ReplaceIdentityOf != "" && u.ReplaceIdentityOf != u.IdentityKey && core.IsPlaceholderIdentityKey(u.ReplaceIdentityOf) {
		old, err := store.LockProviderAccountByIdentity(ctx, s.db, u.UserID, u.Provider, u.ReplaceIdentityOf)
		if err != nil {
			return core.ProviderAccount{}, false, err
		}
		if old != nil {
			acct, err := s.renameTx(ctx, *old, u)
			return acct, false, err
		}
	}

	existing, err := store.LockProviderAccountByIdentity(ctx, s.db, u.UserID, u.Provider, u.IdentityKey)
	if err != nil {
		return core.ProviderAccount{}, false, err
	}
	if existing == nil {
		acct, err := s.insertTx(ctx, u, nil)
		return acct, true, err
	}
	acct, err = s.updateTx(ctx, *existing, u, u.IdentityKey, nil)
	return acct, false, err
}

// importTx copies one legacy row. It never merges and never deletes: an account that already
// holds the row's identity is left alone, and a row whose provider already has an account this
// copy made from the same table (an earlier run, or an earlier release's mirror, whose identity
// a refresh has since rewritten) is that account, not a new one. Only such a mirror is ever
// overwritten, and only by a strictly newer legacy write: an account the user connected through
// the new code has no legacy_source and is untouchable. A row that cannot be matched becomes a
// new account, because a duplicate is harmless (the next refresh merges it into the real
// identity) and a missing one is a user signed out.
func (s *accountStore) importTx(ctx context.Context, u core.AccountUpsert, imp importMeta) (created, applied bool, err error) {
	if err := store.LockUserProviderAccounts(ctx, s.db, u.UserID, u.Provider); err != nil {
		return false, false, err
	}
	existing, err := store.LockProviderAccountByIdentity(ctx, s.db, u.UserID, u.Provider, u.IdentityKey)
	if err != nil {
		return false, false, err
	}
	if existing == nil {
		if existing, err = store.LockLegacyMirror(ctx, s.db, u.UserID, u.Provider, imp.source); err != nil {
			return false, false, err
		}
	}
	if existing == nil {
		if _, err := s.insertTx(ctx, u, &imp); err != nil {
			return false, false, err
		}
		return true, true, nil
	}
	src, err := store.ProviderAccountLegacySource(ctx, s.db, existing.ID)
	if err != nil {
		return false, false, err
	}
	if src == nil || !existing.UpdatedAt.Before(imp.updatedAt) {
		return false, false, nil
	}
	identity := u.IdentityKey
	if core.IsPlaceholderIdentityKey(identity) {
		identity = existing.IdentityKey // a refresh already learned the real identity; the legacy row has not
	}
	if _, err := s.updateTx(ctx, *existing, u, identity, &imp); err != nil {
		return false, false, err
	}
	return false, true, nil
}

// credential is u's write of identity, with its secrets sealed for account accountID.
func (s *accountStore) credential(ctx context.Context, userID, accountID string, u core.AccountUpsert, identity string, imp *importMeta) (store.ProviderAccountCredential, error) {
	secret, refresh, idToken, err := s.sealAll(ctx, userID, accountID, u.Secrets)
	if err != nil {
		return store.ProviderAccountCredential{}, err
	}
	c := store.ProviderAccountCredential{
		AuthKind: u.AuthKind, IdentityKey: identity,
		Email: u.Email, ExternalAccountID: u.ExternalAccountID, OrganizationID: u.OrganizationID,
		OrganizationName: u.OrganizationName, Plan: u.Plan, PlanRaw: u.PlanRaw, KeyHint: u.KeyHint, Scope: u.Scope,
		Secret: secret, Refresh: refresh, IDToken: idToken, ExpiresAt: u.ExpiresAt, UpdatedAt: s.now(),
	}
	if imp != nil {
		c.UpdatedAt, c.LegacySource = imp.updatedAt, imp.source
	}
	return c, nil
}

func (s *accountStore) insertTx(ctx context.Context, u core.AccountUpsert, imp *importMeta) (core.ProviderAccount, error) {
	id := uuid.New().String()
	c, err := s.credential(ctx, u.UserID, id, u, u.IdentityKey, imp)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	priority, err := store.NextProviderAccountPriority(ctx, s.db, u.UserID, u.Provider)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	createdAt := c.UpdatedAt
	if imp != nil {
		createdAt = imp.createdAt
	}
	return store.InsertProviderAccount(ctx, s.db, id, u.UserID, u.Provider, priority, c, createdAt)
}

// updateTx rewrites an existing account in place with a fresh credential. Identity metadata that
// the new write does not carry is kept; the refresh and id tokens likewise (a re-consent that
// omits a refresh token must not erase the one we hold). Label, priority and pause are the
// user's and are never touched here.
func (s *accountStore) updateTx(ctx context.Context, cur core.ProviderAccount, u core.AccountUpsert, identity string, imp *importMeta) (core.ProviderAccount, error) {
	c, err := s.credential(ctx, cur.UserID, cur.ID, u, identity, imp)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	return store.RewriteProviderAccountCredential(ctx, s.db, cur.UserID, cur.ID, c)
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
func (s *accountStore) renameTx(ctx context.Context, old core.ProviderAccount, u core.AccountUpsert) (core.ProviderAccount, error) {
	target, err := store.LockProviderAccountByIdentity(ctx, s.db, u.UserID, u.Provider, u.IdentityKey)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	if target == nil {
		return s.updateTx(ctx, old, u, u.IdentityKey, nil)
	}
	if target.ID == old.ID {
		return s.updateTx(ctx, old, u, u.IdentityKey, nil)
	}
	if core.IsPlaceholderIdentityKey(target.IdentityKey) {
		return core.ProviderAccount{}, fmt.Errorf("refusing to merge into placeholder identity %q", target.IdentityKey)
	}

	if err := store.MoveThreadAssignments(ctx, s.db, old.ID, target.ID); err != nil {
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
	if err := store.RemoveProviderAccount(ctx, s.db, old.ID); err != nil {
		return core.ProviderAccount{}, err
	}
	if err := store.SetProviderAccountStanding(ctx, s.db, target.ID, priority, label, paused); err != nil {
		return core.ProviderAccount{}, err
	}
	target.Priority, target.Label, target.PausedAt = priority, label, paused
	acct, err := s.updateTx(ctx, *target, u, u.IdentityKey, nil)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	if err := store.DensifyProviderAccounts(ctx, s.db, u.UserID, u.Provider); err != nil {
		return core.ProviderAccount{}, err
	}
	return acct, nil
}

// maxAccountSwapAttempts bounds how often a compare-and-swap re-reads after another writer
// changed the row between its read and its update.
const maxAccountSwapAttempts = 3

// CompareAndSwapAccountTokens persists next only if the stored refresh token still equals
// expectedRefresh. Sealing is randomized, so the comparison happens here on the opened value, and
// the UPDATE is conditioned on the exact ciphertexts that were read (the witness). Every write
// re-seals the secret, so the witness changes whenever the row does: of two racers holding the
// same expectation exactly one update matches.
func (s *accountStore) CompareAndSwapAccountTokens(ctx context.Context, userID, accountID, expectedRefresh string, next core.AccountTokens) (bool, error) {
	if next.Secret == "" {
		return false, errors.New("account secret cannot be empty")
	}
	secret, refresh, idToken, err := s.sealAll(ctx, userID, accountID, core.AccountSecrets{Secret: next.Secret, Refresh: next.Refresh, IDToken: next.IDToken})
	if err != nil {
		return false, err
	}
	for range maxAccountSwapAttempts {
		wSecret, wRefresh, found, err := store.ProviderAccountWitness(ctx, s.db, userID, accountID)
		if err != nil || !found {
			return false, err
		}
		stored, err := s.open(ctx, userID, accountID, accountColRefresh, wRefresh)
		if err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare([]byte(stored), []byte(expectedRefresh)) != 1 {
			return false, nil
		}
		swapped, err := store.SwapProviderAccountTokens(ctx, s.db, userID, accountID, store.ProviderAccountCredential{
			Secret: secret, Refresh: refresh, IDToken: idToken, ExpiresAt: next.ExpiresAt,
			Email: next.Email, OrganizationName: next.OrganizationName, Plan: next.Plan, PlanRaw: next.PlanRaw,
			UpdatedAt: s.now(),
		}, wSecret, wRefresh)
		if err != nil || swapped {
			return swapped, err
		}
	}
	return false, nil
}

func (s *accountStore) PatchProviderAccount(ctx context.Context, userID, accountID string, p core.AccountPatch) (core.ProviderAccount, error) {
	var out core.ProviderAccount
	err := s.inTx(ctx, func(ctx context.Context) error {
		cur, err := store.LockUserProviderAccount(ctx, s.db, userID, accountID)
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
		out, err = store.PatchUserProviderAccount(ctx, s.db, userID, accountID, label, paused, reauthAt, reauthReason, now)
		return err
	})
	return out, err
}

func (s *accountStore) ReorderProviderAccounts(ctx context.Context, userID, provider string, orderedIDs []string) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := store.LockUserProviderAccounts(ctx, s.db, userID, provider); err != nil {
			return err
		}
		ids, err := store.LockProviderAccountIDs(ctx, s.db, userID, provider)
		if err != nil {
			return err
		}
		have := make(map[string]bool, len(ids))
		for _, id := range ids {
			have[id] = true
		}
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
			if err := store.SetProviderAccountPriority(ctx, s.db, id, i, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *accountStore) DeleteProviderAccount(ctx context.Context, userID, accountID string) error {
	provider, err := store.UserProviderAccountProvider(ctx, s.db, userID, accountID)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := store.LockUserProviderAccounts(ctx, s.db, userID, provider); err != nil {
			return err
		}
		deleted, err := store.DeleteUserProviderAccount(ctx, s.db, userID, accountID)
		if err != nil {
			return err
		}
		if !deleted {
			return core.ErrProviderAccountNotFound
		}
		return store.DensifyProviderAccounts(ctx, s.db, userID, provider)
	})
}

// ---- limit state ----

func (s *accountStore) ListAccountLimits(ctx context.Context, userID string) (map[string]core.LimitState, error) {
	return store.ListUserAccountLimits(ctx, s.db, userID)
}

// GetAccountLimits returns one account's limit state, or (nil, nil) when it has none. Unlike
// ListAccountLimits it is not scoped to a user: the usage poller leases bare account ids.
func (s *accountStore) GetAccountLimits(ctx context.Context, accountID string) (*core.LimitState, error) {
	return store.GetAccountLimitState(ctx, s.db, accountID)
}

// EnsureUsagePollScheduled schedules a usage poll for the account only when none is scheduled and
// its provider is one of providers. It never moves an existing schedule, so a poll that is backing
// off stays backed off while traffic keeps arriving, and it keeps the failure count (unlike
// ScheduleUsagePoll). An absent account, or one whose limits row does not exist yet, is a no-op.
func (s *accountStore) EnsureUsagePollScheduled(ctx context.Context, accountID string, next time.Time, providers []string) error {
	if next.IsZero() {
		return errors.New("a poll time is required")
	}
	if len(providers) == 0 {
		return nil
	}
	return store.EnsureUsagePollScheduled(ctx, s.db, accountID, next, providers, s.now())
}

// ClaimUsageFetch records that the caller is about to call the account's usage API and reports
// whether it may: false when another caller claimed it within the last floor. The claim is one
// atomic statement, so replicas share the floor. An absent account claims nothing.
func (s *accountStore) ClaimUsageFetch(ctx context.Context, accountID string, now time.Time, floor time.Duration) (bool, error) {
	return store.ClaimUsageFetch(ctx, s.db, accountID, now, floor)
}

func (s *accountStore) UpsertAccountLimitSnapshot(ctx context.Context, snap core.LimitState) error {
	if snap.AccountID == "" || snap.UserID == "" {
		return errors.New("account_id and user_id are required")
	}
	if snap.ObservedAt.IsZero() {
		return errors.New("observed_at is required")
	}
	return store.UpsertAccountLimitSnapshot(ctx, s.db, snap, s.now())
}

func (s *accountStore) BenchAccount(ctx context.Context, userID, accountID string, until time.Time, window string, probe bool) error {
	if until.IsZero() {
		return errors.New("bench needs a time; pass the probe time for an unknown reset")
	}
	found, err := store.BenchProviderAccount(ctx, s.db, userID, accountID, until, window, probe, s.now())
	if err != nil {
		return err
	}
	if !found {
		return core.ErrProviderAccountNotFound
	}
	return nil
}

func (s *accountStore) LeaseUsagePolls(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	return store.LeaseUsagePolls(ctx, s.db, now, limit, lease)
}

func (s *accountStore) ScheduleUsagePoll(ctx context.Context, accountID string, next time.Time, failures int, errText string) error {
	found, err := store.ScheduleUsagePoll(ctx, s.db, accountID, next, failures, errText, s.now())
	if err != nil {
		return err
	}
	if !found {
		return core.ErrProviderAccountNotFound
	}
	return nil
}

// ---- thread assignments ----

func (s *accountStore) GetThreadAssignment(ctx context.Context, threadID, provider string) (*core.Assignment, error) {
	return store.GetThreadAssignment(ctx, s.db, threadID, provider)
}

// LatestServingAccount returns the account that served the thread's newest assistant message
// that names one, or "" when none does. Unlike a sticky assignment it follows the provider the
// thread last ran on, so it is right after a cross-provider fallback returns.
func (s *accountStore) LatestServingAccount(ctx context.Context, threadID string) (string, error) {
	return store.LatestServingProviderAccount(ctx, s.db, threadID)
}

// ListThreadAssignments returns the thread's sticky account for every provider it has used,
// most recently assigned first.
func (s *accountStore) ListThreadAssignments(ctx context.Context, threadID string) ([]core.Assignment, error) {
	return store.ListThreadAssignments(ctx, s.db, threadID)
}

func (s *accountStore) SetThreadAssignment(ctx context.Context, a core.Assignment, expectedAccountID string) (bool, error) {
	if a.ThreadID == "" || a.Provider == "" || a.AccountID == "" {
		return false, errors.New("thread_id, provider and account_id are required")
	}
	if a.AssignedAt.IsZero() {
		a.AssignedAt = s.now()
	}
	if expectedAccountID == "" {
		return store.InsertThreadAssignment(ctx, s.db, a)
	}
	return store.ReassignThread(ctx, s.db, a, expectedAccountID)
}

func (s *accountStore) RecentAssignmentCounts(ctx context.Context, accountIDs []string, since time.Time) (map[string]int, error) {
	return store.RecentAssignmentCounts(ctx, s.db, accountIDs, since)
}

// ListUserIDsWithProviderAccount returns every user holding an account for provider, without
// opening (decrypting) any of them.
func (s *accountStore) ListUserIDsWithProviderAccount(ctx context.Context, provider string) ([]string, error) {
	return store.ListUserIDsWithProviderAccount(ctx, s.db, provider)
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

// LegacyCopyDone reports whether the one-time copy has completed. A cheap read, so a boot that
// has nothing to do opens no secret.
func (s *accountStore) LegacyCopyDone(ctx context.Context) (bool, error) {
	return store.LegacyCredentialCopyDone(ctx, s.db)
}

// ImportLegacy copies the legacy credentials into provider_accounts ONCE and records that it did.
// It runs in one transaction under a global advisory lock, and re-checks the completion marker
// under that lock, so boots racing each other (API server, worker, replicas) import exactly once:
// the losers find the marker and return ran=false. The marker commits with the accounts, so a
// crash leaves neither. A row that fails to write is rolled back to a savepoint, counted and
// logged: one bad credential never blocks the rest or the boot, and the count is kept in the
// marker so whoever drops the legacy tables can see whether anything was left behind.
func (s *accountStore) ImportLegacy(ctx context.Context, imports []LegacyImport, skipped int) (res ImportResult, ran bool, err error) {
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := store.LockLegacyCredentialCopy(ctx, s.db); err != nil {
			return err
		}
		done, err := store.LegacyCredentialCopyDone(ctx, s.db)
		if err != nil {
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
			var created, applied bool
			rowErr, err := store.InLegacyImportSavepoint(ctx, s.db, func() error {
				var err error
				created, applied, err = s.importTx(ctx, in.Upsert, importMeta{source: in.Source, createdAt: in.CreatedAt, updatedAt: in.UpdatedAt})
				return err
			})
			if err != nil {
				return err
			}
			if rowErr != nil {
				slog.WarnContext(ctx, "legacy credential skipped: write failed", "source", in.Source, "user_id", in.Upsert.UserID, "provider", in.Upsert.Provider, "error", rowErr)
				res.Failed++
				continue
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
		if err := store.RecordLegacyCredentialCopy(ctx, s.db, s.now(), res.Created+res.Updated, res.Unchanged, res.Failed); err != nil {
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
func (s *accountStore) OpenLegacy(ctx context.Context, userID string, aad, sealed []byte) (string, error) {
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
