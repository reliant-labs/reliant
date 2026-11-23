// Copyright (c) 2025 Reliant Labs

// Package orgaccounts is the only path to an organization's provider credentials: the org's
// own API-key accounts (rotating the key reseals it under the org's vault tenant), who in the
// org may use each (grants and access), the provider workspaces whose seats the org pays for,
// the rejected-key notices, and the purge of a gone org's credentials.
//
// Who may call these is the handler's to decide (an org's owners and admins). Every method
// scopes to the org it is given in SQL, so an id of another org's account, grant or binding is
// not found. A person's own accounts are internal/credentials/provideraccounts.
//
// Built by hand, by internal/app's NewOrgAccounts provider (providers.go): never a node
// compose.go constructs from Deps.
//
//forge:provided
package orgaccounts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/credentials/accountseal"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Deps holds dependencies for the orgaccounts package.
type Deps struct {
	// DB is the forge ORM handle. A call made with a ctx carrying a transaction joins it.
	DB orm.Context
	// Sealer seals a rotated key under the org's tenant (the credential vault).
	Sealer Sealer
}

//forge:contract
type service struct {
	deps Deps
	now  func() time.Time
}

// New creates the org-credential store. It fails without a vault: a key is stored only sealed.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.DB == nil {
		return nil, errors.New("orgaccounts: Deps.DB is required")
	}
	if deps.Sealer == nil {
		return nil, accountseal.ErrVaultDisabled
	}
	return &service{deps: deps, now: func() time.Time { return time.Now().UTC() }}, nil
}

// inTx runs f in a READ COMMITTED transaction, or in the one ctx already carries. The writers
// serialize on the account's or the binding's lock rather than on serialization retries, and a
// transaction is never retried: each seals with fresh randomness and decides under its locks.
func (s *service) inTx(ctx context.Context, f func(ctx context.Context) error) error {
	return s.deps.DB.RunTx(ctx, orm.TxOptions{Isolation: orm.ReadCommitted, MaxAttempts: 1}, f)
}

// SetOrgAccountAccess sets who in orgID may use one of its accounts.
func (s *service) SetOrgAccountAccess(ctx context.Context, orgID, accountID, access string) (core.ProviderAccount, error) {
	if !core.ValidOrgAccess(access) {
		return core.ProviderAccount{}, fmt.Errorf("invalid org access %q", access)
	}
	return store.SetOrgProviderAccountAccess(ctx, s.deps.DB, orgID, accountID, access, s.now())
}

// ListOrgAccountGrants returns every grant of orgID's accounts.
func (s *service) ListOrgAccountGrants(ctx context.Context, orgID string) ([]core.OrgAccountGrant, error) {
	return store.ListOrgProviderAccountGrants(ctx, s.deps.DB, orgID)
}

// ListOrgProviderWorkspaces returns the provider workspaces bound to orgID.
func (s *service) ListOrgProviderWorkspaces(ctx context.Context, orgID string) ([]core.OrgProviderWorkspace, error) {
	return store.ListOrgProviderWorkspacesOf(ctx, s.deps.DB, orgID)
}

// ListOrgSeats returns the members' accounts orgID pays for as seats (attribution source
// workspace or approved). The managed key minted in the org is not a seat.
func (s *service) ListOrgSeats(ctx context.Context, orgID string) ([]core.ProviderAccount, error) {
	all, err := store.ListAttributedProviderAccounts(ctx, s.deps.DB, orgID)
	if err != nil {
		return nil, err
	}
	seats := all[:0]
	for _, a := range all {
		if seatSource(a.AttributionSource) {
			seats = append(seats, a)
		}
	}
	return seats, nil
}

// ProviderWorkspaceBinding returns the binding of provider's workspace externalOrgID, or nil.
func (s *service) ProviderWorkspaceBinding(ctx context.Context, provider, externalOrgID string) (*core.OrgProviderWorkspace, error) {
	return store.GetProviderWorkspaceBinding(ctx, s.deps.DB, provider, externalOrgID)
}

// ListWorkspaceSignIns returns every person's sign-in in provider's workspace externalOrgID.
func (s *service) ListWorkspaceSignIns(ctx context.Context, provider, externalOrgID string) ([]core.ProviderAccount, error) {
	return store.ListWorkspaceSignIns(ctx, s.deps.DB, provider, externalOrgID)
}

// HasRejectedOrgKeys reports whether any org-owned API key needs attention (store.HasRejectedOrgKeys).
func (s *service) HasRejectedOrgKeys(ctx context.Context) (bool, error) {
	return store.HasRejectedOrgKeys(ctx, s.deps.DB)
}

// ListRejectedOrgKeysIn returns the rejected org-owned API keys of orgIDs; the inbox lists them to
// those orgs' owners and admins.
func (s *service) ListRejectedOrgKeysIn(ctx context.Context, orgIDs []string) ([]core.ProviderAccount, error) {
	return store.ListRejectedOrgKeysIn(ctx, s.deps.DB, orgIDs)
}

// ClaimDueOrgKeyNotices claims up to limit rejected org keys whose email to the org's owners
// and admins was never confirmed started and is due, moving each one's next attempt lease on
// (store.ClaimDueOrgKeyNotices).
func (s *service) ClaimDueOrgKeyNotices(ctx context.Context, limit int, lease time.Duration) ([]core.ProviderAccount, error) {
	return store.ClaimDueOrgKeyNotices(ctx, s.deps.DB, limit, lease)
}

// ConfirmOrgKeyNotice records that the notice of a rejected org key's mark was started
// (store.ConfirmOrgKeyNotice).
func (s *service) ConfirmOrgKeyNotice(ctx context.Context, orgID, accountID string, markedAt time.Time) error {
	return store.ConfirmOrgKeyNotice(ctx, s.deps.DB, orgID, accountID, markedAt)
}

// ListProviderAccountGrantMembers returns one page of the distinct (org,
// member) pairs holding grants, after the pair after.
func (s *service) ListProviderAccountGrantMembers(ctx context.Context, after core.GrantMember, limit int) ([]core.GrantMember, error) {
	return store.ListProviderAccountGrantMembers(ctx, s.deps.DB, after, limit)
}

// DeleteProviderAccountGrantsBefore deletes userID's grants in orgID made
// before before.
func (s *service) DeleteProviderAccountGrantsBefore(ctx context.Context, orgID, userID string, before time.Time) (int64, error) {
	return store.DeleteProviderAccountGrantsBefore(ctx, s.deps.DB, orgID, userID, before)
}

// ListOrgCredentialHolders returns one page of the ids of the orgs reliant
// holds credentials for, after the id after.
func (s *service) ListOrgCredentialHolders(ctx context.Context, after string, limit int) ([]string, error) {
	return store.ListOrgCredentialHolders(ctx, s.deps.DB, after, limit)
}

// PurgeOrgCredentials removes every credential reliant holds for orgID, which
// the caller has established is gone, in one transaction: all or nothing,
// and a repeat finds nothing (store.PurgeOrgCredentials).
func (s *service) PurgeOrgCredentials(ctx context.Context, orgID string) (core.OrgCredentialsPurged, error) {
	var out core.OrgCredentialsPurged
	err := s.deps.DB.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		out, err = store.PurgeOrgCredentials(ctx, s.deps.DB, orgID)
		return err
	})
	return out, err
}

// seatSources are the attribution sources of a seat: a person's sign-in the org pays for. The
// managed source is the Reliant key minted in the org, which an admin does not release.
var seatSources = []string{core.AttributionWorkspace, core.AttributionApproved}

func seatSource(s string) bool {
	return s == core.AttributionWorkspace || s == core.AttributionApproved
}

// RotateOrgAPIKey reseals one of orgID's API-key accounts with a new key, in place: the row keeps
// its id, so its label, priority, pause, access, grants, limit state and thread assignments all
// stay; its identity, key hint and secret change, and a needs-attention mark is cleared
// (org-provider-accounts.md §3.5). A key the org already holds as ANOTHER account is
// core.ErrOrgAPIKeyInUse: the (owner, provider, identity) key is unique.
func (s *service) RotateOrgAPIKey(ctx context.Context, orgID, accountID string, k core.OrgAPIKey) (core.ProviderAccount, error) {
	owner := core.OrgAccountOwner(orgID)
	if !owner.Valid() {
		return core.ProviderAccount{}, core.ErrProviderAccountNotFound
	}
	if k.Secret == "" || k.IdentityKey == "" {
		return core.ProviderAccount{}, errors.New("a rotated key needs its secret and identity")
	}
	key := owner.Key()
	provider, err := store.UserProviderAccountProvider(ctx, s.deps.DB, key, accountID)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	var out core.ProviderAccount
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := store.LockUserProviderAccounts(ctx, s.deps.DB, key, provider); err != nil {
			return err
		}
		cur, err := store.LockOwnerProviderAccount(ctx, s.deps.DB, owner, accountID)
		if err != nil {
			return err
		}
		if cur.AuthKind != core.AuthKindAPIKey || cur.Provider != provider {
			return core.ErrProviderAccountNotFound
		}
		other, err := store.LockProviderAccountByIdentity(ctx, s.deps.DB, key, provider, k.IdentityKey)
		if err != nil {
			return err
		}
		if other != nil && other.ID != cur.ID {
			return core.ErrOrgAPIKeyInUse
		}
		c, err := accountseal.Credential(ctx, s.deps.Sealer, owner, cur.ID, core.AccountUpsert{
			AuthKind: core.AuthKindAPIKey, KeyHint: k.KeyHint, Secrets: core.AccountSecrets{Secret: k.Secret},
		}, k.IdentityKey, s.now())
		if err != nil {
			return err
		}
		out, err = store.RewriteProviderAccountCredential(ctx, s.deps.DB, key, cur.ID, c)
		return err
	})
	return out, err
}

// SetOrgAccountGrants replaces the grants of one of orgID's accounts, under the account's row
// lock so a concurrent delete cannot leave grants behind.
func (s *service) SetOrgAccountGrants(ctx context.Context, orgID, accountID string, userIDs []string, grantedBy string) error {
	owner := core.OrgAccountOwner(orgID)
	if !owner.Valid() {
		return core.ErrProviderAccountNotFound
	}
	if grantedBy == "" {
		return errors.New("a grant records who made it")
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		if _, err := store.LockOwnerProviderAccount(ctx, s.deps.DB, owner, accountID); err != nil {
			return err
		}
		return store.ReplaceProviderAccountGrants(ctx, s.deps.DB, orgID, accountID, userIDs, grantedBy, s.now())
	})
}

// BindOrgProviderWorkspace records the binding; a workspace another org holds is
// core.ErrProviderWorkspaceBoundElsewhere, never naming that org.
func (s *service) BindOrgProviderWorkspace(ctx context.Context, w core.OrgProviderWorkspace) (core.OrgProviderWorkspace, bool, error) {
	got, created, err := store.InsertOrgProviderWorkspace(ctx, s.deps.DB, w, s.now())
	if err != nil && orm.ConstraintName(err) == store.OrgProviderWorkspaceConstraintOneOrg {
		return core.OrgProviderWorkspace{}, false, core.ErrProviderWorkspaceBoundElsewhere
	}
	return got, created, err
}

// UnbindOrgProviderWorkspace deletes the binding and, in the same transaction, clears every
// attribution it made (source 'workspace' in that workspace), so no seat outlives its binding.
func (s *service) UnbindOrgProviderWorkspace(ctx context.Context, orgID, provider, externalOrgID string) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		deleted, err := store.DeleteOrgProviderWorkspace(ctx, s.deps.DB, orgID, provider, externalOrgID)
		if err != nil {
			return err
		}
		if !deleted {
			return core.ErrProviderWorkspaceNotFound
		}
		if _, err := store.ClearWorkspaceAttributions(ctx, s.deps.DB, orgID, provider, externalOrgID, s.now()); err != nil {
			return err
		}
		// The org's releases in it go too, so binding it again starts afresh. Accounts before
		// releases, the order ReleaseOrgSeat locks them in.
		_, err = store.DeleteWorkspaceSeatReleases(ctx, s.deps.DB, orgID, provider, externalOrgID)
		return err
	})
}

// AttributeWorkspaceSeat attributes accountID to w's org under a share lock on the binding, so an
// unbind either sees the new seat (and clears it) or has already removed the binding (and this
// attributes nothing).
func (s *service) AttributeWorkspaceSeat(ctx context.Context, w core.OrgProviderWorkspace, accountID string) (bool, error) {
	var done bool
	err := s.inTx(ctx, func(ctx context.Context) error {
		bound, err := store.LockOrgProviderWorkspaceShared(ctx, s.deps.DB, w.OrgID, w.Provider, w.ExternalOrgID)
		if err != nil || !bound {
			return err
		}
		done, err = store.AttributeWorkspaceSeat(ctx, s.deps.DB, w, accountID, s.now())
		return err
	})
	return done, err
}

// ClearWorkspaceSeat decides and clears in one transaction, so a decision made from an older
// read can never undo a newer attribution. It locks the account row first, then reads the
// binding with a fresh statement (READ COMMITTED: it sees every bind committed before it), and
// clears only a seat of orgID, source 'workspace', whose current workspace no binding of orgID
// covers. A rebind's rematch either committed before the lock (the read finds its binding: no
// clear) or waits on the lock and then attributes the row this clears. Lock order is the account,
// then the binding unlocked, so it cannot deadlock with AttributeWorkspaceSeat (binding, then
// account) or an unbind (binding, then accounts).
func (s *service) ClearWorkspaceSeat(ctx context.Context, orgID, accountID string) (bool, error) {
	var cleared bool
	err := s.inTx(ctx, func(ctx context.Context) error {
		a, err := store.LockProviderAccountByID(ctx, s.deps.DB, accountID)
		if err != nil || a == nil || a.AttributedOrgID != orgID || a.AttributionSource != core.AttributionWorkspace {
			return err
		}
		if ws := core.ProviderWorkspaceID(*a); ws != "" {
			b, err := store.GetProviderWorkspaceBinding(ctx, s.deps.DB, a.Provider, ws)
			if err != nil {
				return err
			}
			if b != nil && b.OrgID == orgID {
				return nil // still the org's seat
			}
		}
		cleared, err = store.ClearProviderAccountAttribution(ctx, s.deps.DB, orgID, accountID,
			[]string{core.AttributionWorkspace}, s.now())
		return err
	})
	return cleared, err
}

// ReleaseOrgSeat clears one of orgID's seats and records the release in one transaction.
func (s *service) ReleaseOrgSeat(ctx context.Context, orgID, accountID, releasedBy string) error {
	if releasedBy == "" {
		return errors.New("a release records who made it")
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		released, err := store.ClearProviderAccountAttribution(ctx, s.deps.DB, orgID, accountID, seatSources, s.now())
		if err != nil {
			return err
		}
		if !released {
			return core.ErrOrgSeatNotFound
		}
		return store.InsertOrgSeatRelease(ctx, s.deps.DB, orgID, accountID, releasedBy, s.now())
	})
}
