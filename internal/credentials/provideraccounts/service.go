// Copyright (c) 2025 Reliant Labs

// Package provideraccounts is the only path to the provider_accounts table for a
// person's and an org's provider accounts, their limit state, their poll schedule and
// the thread assignments that pin a thread to an account.
//
// Every secret column (the key or access token, the refresh token, the id token) is
// sealed here on write and opened here on read, under the owner's vault tenant: the
// person's, or the org's (accountseal). A caller gets plaintext only from the
// WithSecrets reads it already received it from. Each statement is scoped to an
// owner (core.AccountOwner), and an invalid owner (a person's id shaped like an owner
// key) reaches nothing, so a personal method never touches an org's account and an
// org's never a person's.
//
// The org-side administration (grants, seats, workspace bindings, key rotation, the
// rejected-key notices and the gone-org purge) is internal/credentials/orgaccounts.
//
// Built by hand, by internal/app's NewProviderAccounts provider (providers.go): never
// a node compose.go constructs from Deps.
//
//forge:provided
package provideraccounts

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/credentials/accountseal"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Deps holds dependencies for the provideraccounts package.
type Deps struct {
	// DB is the forge ORM handle. A call made with a ctx carrying a transaction joins it.
	DB orm.Context
	// Sealer seals and opens the secret columns (the credential vault).
	Sealer Sealer
}

//forge:contract
type service struct {
	deps Deps
	now  func() time.Time
}

// New creates the provider-account store. It fails without a vault: a secret is stored
// only sealed.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.DB == nil {
		return nil, errors.New("provideraccounts: Deps.DB is required")
	}
	if deps.Sealer == nil {
		return nil, accountseal.ErrVaultDisabled
	}
	return &service{deps: deps, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *service) openAll(ctx context.Context, a store.SealedProviderAccount) (core.AccountSecrets, error) {
	return accountseal.OpenAll(ctx, s.deps.Sealer, a.Owner(), a.ID, a.Secret, a.Refresh, a.IDToken) // owner from owner_kind / org_id, never the raw user_id
}

// inTx runs f in a READ COMMITTED transaction, or in the one ctx already carries. Writers of one
// (user, provider) serialize on an advisory lock rather than relying on serialization retries,
// and a transaction is never retried: f seals with fresh randomness and reads under its locks,
// so it is only correct as the one attempt.
func (s *service) inTx(ctx context.Context, f func(ctx context.Context) error) error {
	return s.deps.DB.RunTx(ctx, orm.TxOptions{Isolation: orm.ReadCommitted, MaxAttempts: 1}, f)
}

// ListProviderAccounts lists a person's own accounts. It never returns an org's account: the
// read is scoped to owner_kind 'user', and a user id shaped like an owner key is no person.
func (s *service) ListProviderAccounts(ctx context.Context, userID string) ([]core.ProviderAccount, error) {
	return s.ListOwnedProviderAccounts(ctx, core.UserAccountOwner(userID))
}

// ListOwnedProviderAccounts lists the owner's accounts, metadata only.
func (s *service) ListOwnedProviderAccounts(ctx context.Context, owner core.AccountOwner) ([]core.ProviderAccount, error) {
	return store.ListOwnerProviderAccounts(ctx, s.deps.DB, owner)
}

func (s *service) ListProviderAccountsWithSecrets(ctx context.Context, userID string) ([]core.AccountWithSecrets, error) {
	return s.ListOwnedProviderAccountsWithSecrets(ctx, core.UserAccountOwner(userID))
}

// ListOwnedProviderAccountsWithSecrets opens every account of the owner, each under the owner's
// tenant. An invalid owner lists nothing.
func (s *service) ListOwnedProviderAccountsWithSecrets(ctx context.Context, owner core.AccountOwner) ([]core.AccountWithSecrets, error) {
	rows, err := store.ListOwnerProviderAccountsSealed(ctx, s.deps.DB, owner)
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

func (s *service) GetProviderAccountWithSecrets(ctx context.Context, userID, accountID string) (*core.AccountWithSecrets, error) {
	return s.GetOwnedProviderAccountWithSecrets(ctx, core.UserAccountOwner(userID), accountID)
}

// GetOwnedProviderAccountWithSecrets opens one of the owner's accounts, under the owner's tenant.
func (s *service) GetOwnedProviderAccountWithSecrets(ctx context.Context, owner core.AccountOwner, accountID string) (*core.AccountWithSecrets, error) {
	r, err := store.GetOwnerProviderAccountSealed(ctx, s.deps.DB, owner, accountID)
	if err != nil {
		return nil, err
	}
	sec, err := s.openAll(ctx, *r)
	if err != nil {
		return nil, err
	}
	return &core.AccountWithSecrets{ProviderAccount: r.ProviderAccount, Secrets: sec}, nil
}

func (s *service) UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error) {
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
	owner := u.AccountOwner()
	switch {
	case owner.ID == "":
		return errors.New("user_id cannot be empty")
	case !owner.Valid():
		return fmt.Errorf("invalid account owner %q/%q", owner.Kind, owner.ID)
	case owner.IsOrg() && u.AuthKind != core.AuthKindAPIKey:
		return errors.New("an organization can own only an API key, never a sign-in")
	case owner.IsOrg() && !core.ValidOrgAccess(u.OrgAccess):
		return fmt.Errorf("invalid org access %q", u.OrgAccess)
	case owner.IsOrg() && u.CreatedByUserID == "":
		return errors.New("an organization's account records who added it")
	case !owner.IsOrg() && (u.OrgAccess != "" || u.CreatedByUserID != ""):
		return errors.New("org access and creator are an organization's account's fields")
	case u.Provider == "":
		return errors.New("provider cannot be empty")
	case u.IdentityKey == "":
		return errors.New("identity_key cannot be empty")
	case !u.AuthKind.Valid():
		return fmt.Errorf("invalid auth_kind %q", u.AuthKind)
	case u.Secrets.Secret == "":
		return errors.New("account secret cannot be empty")
	case u.AuthKind == core.AuthKindManaged && !validManagedUpsert(u):
		return fmt.Errorf("a managed account's identity must be %q, or %q with its org's managed attribution",
			core.ManagedIdentityKey, core.ManagedOrgIdentityKey("<org>"))
	case u.AuthKind != core.AuthKindManaged && (u.AttributedOrgID != "" || u.AttributionSource != ""):
		return errors.New("only a managed key is attributed when it is written; a seat is attributed by its workspace binding")
	}
	return nil
}

// validManagedUpsert reports whether a managed key's write is one of its two shapes: the
// org-less key (identity ManagedIdentityKey, no attribution), or the key minted in one org
// (identity ManagedOrgIdentityKey(org), attributed to that org with source managed).
func validManagedUpsert(u core.AccountUpsert) bool {
	if u.AttributedOrgID == "" {
		return u.IdentityKey == core.ManagedIdentityKey && u.AttributionSource == ""
	}
	return u.IdentityKey == core.ManagedOrgIdentityKey(u.AttributedOrgID) && u.AttributionSource == core.AttributionManaged
}

// upsertTx is the one write path for connecting an account: a fresh sign-in and the rewrite of a
// placeholder identity after a refresh.
func (s *service) upsertTx(ctx context.Context, u core.AccountUpsert) (acct core.ProviderAccount, created bool, err error) {
	key := u.AccountOwner().Key()
	if err := store.LockUserProviderAccounts(ctx, s.deps.DB, key, u.Provider); err != nil {
		return core.ProviderAccount{}, false, err
	}

	// Only a placeholder is ever rewritten: a real identity is who the account IS.
	if u.ReplaceIdentityOf != "" && u.ReplaceIdentityOf != u.IdentityKey && core.IsPlaceholderIdentityKey(u.ReplaceIdentityOf) {
		old, err := store.LockProviderAccountByIdentity(ctx, s.deps.DB, key, u.Provider, u.ReplaceIdentityOf)
		if err != nil {
			return core.ProviderAccount{}, false, err
		}
		if old != nil {
			acct, err := s.renameTx(ctx, *old, u)
			return acct, false, err
		}
	}

	existing, err := store.LockProviderAccountByIdentity(ctx, s.deps.DB, key, u.Provider, u.IdentityKey)
	if err != nil {
		return core.ProviderAccount{}, false, err
	}
	if existing == nil {
		acct, err := s.insertTx(ctx, u)
		return acct, true, err
	}
	acct, err = s.updateTx(ctx, *existing, u, u.IdentityKey)
	return acct, false, err
}

// credential is u's write of identity, with its secrets sealed for account accountID.
func (s *service) credential(ctx context.Context, owner core.AccountOwner, accountID string, u core.AccountUpsert, identity string) (store.ProviderAccountCredential, error) {
	return accountseal.Credential(ctx, s.deps.Sealer, owner, accountID, u, identity, s.now())
}

func (s *service) insertTx(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, error) {
	id := uuid.New().String()
	owner := u.AccountOwner()
	c, err := s.credential(ctx, owner, id, u, u.IdentityKey)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	priority, err := store.NextProviderAccountPriority(ctx, s.deps.DB, owner.Key(), u.Provider)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	own := store.ProviderAccountOwnership{
		Owner: owner, OrgAccess: u.OrgAccess, CreatedByUserID: u.CreatedByUserID,
		AttributedOrgID: u.AttributedOrgID, AttributionSource: u.AttributionSource,
	}
	return store.InsertProviderAccount(ctx, s.deps.DB, id, own, u.Provider, priority, c, c.UpdatedAt)
}

// updateTx rewrites an existing account in place with a fresh credential. Identity metadata that
// the new write does not carry is kept; the refresh and id tokens likewise (a re-consent that
// omits a refresh token must not erase the one we hold). Label, priority and pause are the
// user's and are never touched here.
func (s *service) updateTx(ctx context.Context, cur core.ProviderAccount, u core.AccountUpsert, identity string) (core.ProviderAccount, error) {
	c, err := s.credential(ctx, cur.Owner(), cur.ID, u, identity)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	return store.RewriteProviderAccountCredential(ctx, s.deps.DB, cur.UserID, cur.ID, c)
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
func (s *service) renameTx(ctx context.Context, old core.ProviderAccount, u core.AccountUpsert) (core.ProviderAccount, error) {
	target, err := store.LockProviderAccountByIdentity(ctx, s.deps.DB, old.UserID, u.Provider, u.IdentityKey)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	if target == nil {
		return s.updateTx(ctx, old, u, u.IdentityKey)
	}
	if target.ID == old.ID {
		return s.updateTx(ctx, old, u, u.IdentityKey)
	}
	if core.IsPlaceholderIdentityKey(target.IdentityKey) {
		return core.ProviderAccount{}, fmt.Errorf("refusing to merge into placeholder identity %q", target.IdentityKey)
	}

	if err := store.MoveThreadAssignments(ctx, s.deps.DB, old.ID, target.ID); err != nil {
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
	if err := store.RemoveProviderAccount(ctx, s.deps.DB, old.ID); err != nil {
		return core.ProviderAccount{}, err
	}
	if err := store.SetProviderAccountStanding(ctx, s.deps.DB, target.ID, priority, label, paused); err != nil {
		return core.ProviderAccount{}, err
	}
	target.Priority, target.Label, target.PausedAt = priority, label, paused
	acct, err := s.updateTx(ctx, *target, u, u.IdentityKey)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	if err := store.DensifyProviderAccounts(ctx, s.deps.DB, old.UserID, u.Provider); err != nil {
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
func (s *service) CompareAndSwapAccountTokens(ctx context.Context, userID, accountID, expectedRefresh string, next core.AccountTokens) (bool, error) {
	if next.Secret == "" {
		return false, errors.New("account secret cannot be empty")
	}
	// A refreshed sign-in is always a person's: an org owns only API keys, which never refresh.
	owner := core.UserAccountOwner(userID)
	if !owner.Valid() {
		return false, nil
	}
	secret, refresh, idToken, err := accountseal.SealAll(ctx, s.deps.Sealer, owner, accountID, core.AccountSecrets{Secret: next.Secret, Refresh: next.Refresh, IDToken: next.IDToken})
	if err != nil {
		return false, err
	}
	for range maxAccountSwapAttempts {
		wSecret, wRefresh, found, err := store.ProviderAccountWitness(ctx, s.deps.DB, userID, accountID)
		if err != nil || !found {
			return false, err
		}
		stored, err := accountseal.Open(ctx, s.deps.Sealer, owner, accountID, accountseal.ColRefresh, wRefresh)
		if err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare([]byte(stored), []byte(expectedRefresh)) != 1 {
			return false, nil
		}
		swapped, err := store.SwapProviderAccountTokens(ctx, s.deps.DB, userID, accountID, store.ProviderAccountCredential{
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

// PatchProviderAccount patches one of a person's own accounts.
func (s *service) PatchProviderAccount(ctx context.Context, userID, accountID string, p core.AccountPatch) (core.ProviderAccount, error) {
	return s.PatchOwnedProviderAccount(ctx, core.UserAccountOwner(userID), accountID, p)
}

// PatchOwnedProviderAccount patches one of the owner's accounts. Marking an org's key
// needs-sign-in takes it out of every member's candidates at once (org-provider-accounts.md §4.7).
func (s *service) PatchOwnedProviderAccount(ctx context.Context, owner core.AccountOwner, accountID string, p core.AccountPatch) (core.ProviderAccount, error) {
	var out core.ProviderAccount
	err := s.inTx(ctx, func(ctx context.Context) error {
		cur, err := store.LockOwnerProviderAccount(ctx, s.deps.DB, owner, accountID)
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
		out, err = store.PatchUserProviderAccount(ctx, s.deps.DB, cur.UserID, accountID, label, paused, reauthAt, reauthReason, now)
		return err
	})
	return out, err
}

func (s *service) ReorderProviderAccounts(ctx context.Context, userID, provider string, orderedIDs []string) error {
	return s.ReorderOwnedProviderAccounts(ctx, core.UserAccountOwner(userID), provider, orderedIDs)
}

// ReorderOwnedProviderAccounts sets the priorities of the owner's accounts of provider to
// 0..n-1 in orderedIDs order. An invalid owner (a person's id shaped like an owner key) has no
// order: core.ErrInvalidAccountOrder.
func (s *service) ReorderOwnedProviderAccounts(ctx context.Context, owner core.AccountOwner, provider string, orderedIDs []string) error {
	if !owner.Valid() {
		return core.ErrInvalidAccountOrder // no owner: an owner key never reaches an org's order as a person's
	}
	key := owner.Key()
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := store.LockUserProviderAccounts(ctx, s.deps.DB, key, provider); err != nil {
			return err
		}
		ids, err := store.LockProviderAccountIDs(ctx, s.deps.DB, key, provider)
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
			if err := store.SetProviderAccountPriority(ctx, s.deps.DB, id, i, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *service) DeleteProviderAccount(ctx context.Context, userID, accountID string) error {
	return s.DeleteOwnedProviderAccount(ctx, core.UserAccountOwner(userID), accountID)
}

// DeleteOwnedProviderAccount deletes one of the owner's accounts and re-densifies its provider's
// priorities. Its ciphertext goes with the row; limit state, thread assignments and an org
// account's grants cascade.
func (s *service) DeleteOwnedProviderAccount(ctx context.Context, owner core.AccountOwner, accountID string) error {
	if !owner.Valid() {
		return core.ErrProviderAccountNotFound // a person's delete never reaches an org's account
	}
	key := owner.Key()
	provider, err := store.UserProviderAccountProvider(ctx, s.deps.DB, key, accountID)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := store.LockUserProviderAccounts(ctx, s.deps.DB, key, provider); err != nil {
			return err
		}
		deleted, err := store.DeleteUserProviderAccount(ctx, s.deps.DB, key, accountID)
		if err != nil {
			return err
		}
		if !deleted {
			return core.ErrProviderAccountNotFound
		}
		return store.DensifyProviderAccounts(ctx, s.deps.DB, key, provider)
	})
}

// ---- limit state ----

func (s *service) ListAccountLimits(ctx context.Context, userID string) (map[string]core.LimitState, error) {
	if !core.UserAccountOwner(userID).Valid() {
		return map[string]core.LimitState{}, nil
	}
	return store.ListUserAccountLimits(ctx, s.deps.DB, userID)
}

// GetAccountLimits returns one account's limit state, or (nil, nil) when it has none. Unlike
// ListAccountLimits it is not scoped to a user: the usage poller leases bare account ids.
func (s *service) GetAccountLimits(ctx context.Context, accountID string) (*core.LimitState, error) {
	return store.GetAccountLimitState(ctx, s.deps.DB, accountID)
}

// EnsureUsagePollScheduled schedules a usage poll for the account only when none is scheduled and
// its provider is one of providers. It never moves an existing schedule, so a poll that is backing
// off stays backed off while traffic keeps arriving, and it keeps the failure count (unlike
// ScheduleUsagePoll). An absent account, or one whose limits row does not exist yet, is a no-op.
func (s *service) EnsureUsagePollScheduled(ctx context.Context, accountID string, next time.Time, providers []string) error {
	if next.IsZero() {
		return errors.New("a poll time is required")
	}
	if len(providers) == 0 {
		return nil
	}
	return store.EnsureUsagePollScheduled(ctx, s.deps.DB, accountID, next, providers, s.now())
}

// ClaimUsageFetch records that the caller is about to call the account's usage API and reports
// whether it may: false when another caller claimed it within the last floor. The claim is one
// atomic statement, so replicas share the floor. An absent account claims nothing.
func (s *service) ClaimUsageFetch(ctx context.Context, accountID string, now time.Time, floor time.Duration) (bool, error) {
	return store.ClaimUsageFetch(ctx, s.deps.DB, accountID, now, floor)
}

// UpsertAccountLimitSnapshot writes snap for the account whose owner key is snap.UserID: an org's
// account's limits row is keyed by the org's owner key, shared by every member who uses it.
func (s *service) UpsertAccountLimitSnapshot(ctx context.Context, snap core.LimitState) error {
	if snap.AccountID == "" || snap.UserID == "" {
		return errors.New("account_id and user_id are required")
	}
	if snap.ObservedAt.IsZero() {
		return errors.New("observed_at is required")
	}
	return store.UpsertAccountLimitSnapshot(ctx, s.deps.DB, snap, s.now())
}

// BenchAccount benches one of the owner's accounts. For an org's account the bench is the org's,
// so it takes the key out of rotation for every member until it resets.
func (s *service) BenchAccount(ctx context.Context, owner core.AccountOwner, accountID string, until time.Time, window string, probe bool) error {
	if until.IsZero() {
		return errors.New("bench needs a time; pass the probe time for an unknown reset")
	}
	if !owner.Valid() {
		return core.ErrProviderAccountNotFound
	}
	found, err := store.BenchProviderAccount(ctx, s.deps.DB, owner.Key(), accountID, until, window, probe, s.now())
	if err != nil {
		return err
	}
	if !found {
		return core.ErrProviderAccountNotFound
	}
	return nil
}

func (s *service) LeaseUsagePolls(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	return store.LeaseUsagePolls(ctx, s.deps.DB, now, limit, lease)
}

func (s *service) ScheduleUsagePoll(ctx context.Context, accountID string, next time.Time, failures int, errText string) error {
	found, err := store.ScheduleUsagePoll(ctx, s.deps.DB, accountID, next, failures, errText, s.now())
	if err != nil {
		return err
	}
	if !found {
		return core.ErrProviderAccountNotFound
	}
	return nil
}

// ---- thread assignments ----

func (s *service) GetThreadAssignment(ctx context.Context, threadID, provider string) (*core.Assignment, error) {
	return store.GetThreadAssignment(ctx, s.deps.DB, threadID, provider)
}

// LatestServingAccount returns the account that served the thread's newest assistant message
// that names one, or "" when none does. Unlike a sticky assignment it follows the provider the
// thread last ran on, so it is right after a cross-provider fallback returns.
func (s *service) LatestServingAccount(ctx context.Context, threadID string) (string, error) {
	return store.LatestServingProviderAccount(ctx, s.deps.DB, threadID)
}

// ListThreadAssignments returns the thread's sticky account for every provider it has used,
// most recently assigned first.
func (s *service) ListThreadAssignments(ctx context.Context, threadID string) ([]core.Assignment, error) {
	return store.ListThreadAssignments(ctx, s.deps.DB, threadID)
}

func (s *service) SetThreadAssignment(ctx context.Context, a core.Assignment, expectedAccountID string) (bool, error) {
	if a.ThreadID == "" || a.Provider == "" || a.AccountID == "" {
		return false, errors.New("thread_id, provider and account_id are required")
	}
	if a.AssignedAt.IsZero() {
		a.AssignedAt = s.now()
	}
	if expectedAccountID == "" {
		return store.InsertThreadAssignment(ctx, s.deps.DB, a)
	}
	return store.ReassignThread(ctx, s.deps.DB, a, expectedAccountID)
}

func (s *service) RecentAssignmentCounts(ctx context.Context, accountIDs []string, since time.Time) (map[string]int, error) {
	return store.RecentAssignmentCounts(ctx, s.deps.DB, accountIDs, since)
}

// ListUserIDsWithProviderAccount returns every user holding an account for provider, without
// opening (decrypting) any of them.
func (s *service) ListUserIDsWithProviderAccount(ctx context.Context, provider string) ([]string, error) {
	return store.ListUserIDsWithProviderAccount(ctx, s.deps.DB, provider)
}

// ListOwnedAccountLimits returns limit state by account id for the owner's accounts that have
// any. An org's limits are keyed by its owner key, shared by every member who uses the account.
func (s *service) ListOwnedAccountLimits(ctx context.Context, owner core.AccountOwner) (map[string]core.LimitState, error) {
	if !owner.Valid() {
		return map[string]core.LimitState{}, nil
	}
	return store.ListUserAccountLimits(ctx, s.deps.DB, owner.Key())
}

// ListMemberAccountGrants returns when each of orgID's accounts was granted to userID, by
// account id. The caller decides whether a grant still counts (control-plane
// docs/design/org-provider-accounts.md §3.3: only one made in the current membership).
func (s *service) ListMemberAccountGrants(ctx context.Context, orgID, userID string) (map[string]time.Time, error) {
	return store.ListMemberAccountGrants(ctx, s.deps.DB, orgID, userID)
}

// ClearOtherThreadAssignments clears incompatible provider pins before the next assignment
// after a chat-wide provider switch, scoped to the thread's owner. Repeated calls are harmless.
func (s *service) ClearOtherThreadAssignments(ctx context.Context, userID, threadID, provider string) error {
	return store.ClearOtherThreadAssignments(ctx, s.deps.DB, userID, threadID, provider)
}

var errEmptyUserID = errors.New("user id cannot be empty")

// PurgeUser deletes the person's own accounts and the grants made to them, and returns how many
// rows went, for the account purge. An organization's accounts, and the grants its admins made, are
// the org's and stay. It joins the transaction ctx carries, so the purge stays all-or-nothing.
func (s *service) PurgeUser(ctx context.Context, userID string) (int64, error) {
	if userID == "" {
		return 0, errEmptyUserID
	}
	grants, err := store.DeleteUserProviderAccountGrants(ctx, s.deps.DB, userID)
	if err != nil {
		return 0, fmt.Errorf("delete grants made to the user: %w", err)
	}
	accounts, err := store.DeleteUserProviderAccounts(ctx, s.deps.DB, userID)
	if err != nil {
		return 0, fmt.Errorf("delete the user's accounts: %w", err)
	}
	return grants + accounts, nil
}

// UserHasCredentials reports whether the person owns any account.
func (s *service) UserHasCredentials(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, errEmptyUserID
	}
	return store.UserHasProviderAccount(ctx, s.deps.DB, userID)
}

// ProviderAccountNames returns the provider, label and kind of each account in ids that still
// exists, keyed by id, for a usage view. It reads by id with no owner and returns no secret: the
// ids a caller passes come from usage rows it was already allowed to read.
func (s *service) ProviderAccountNames(ctx context.Context, ids []string) (map[string]core.ProviderAccountName, error) {
	return store.ProviderAccountNames(ctx, s.deps.DB, ids)
}
