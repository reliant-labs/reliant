// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The administration of an org's own provider accounts, their grants, and the provider
// workspaces whose seats the org pays for (control-plane docs/design/org-provider-accounts.md
// §3.3–§3.5, §6). Who may call these is the handler's to decide (an org's owners and admins);
// every method here scopes to the org it is given in SQL, so an id of another org's account,
// grant or binding is not found.

// ListOwnedAccountLimits returns limit state by account id for the owner's accounts that have
// any. An org's limits are keyed by its owner key, shared by every member who uses the account.
func (r *Repo) ListOwnedAccountLimits(ctx context.Context, owner core.AccountOwner) (map[string]core.LimitState, error) {
	if !owner.Valid() {
		return map[string]core.LimitState{}, nil
	}
	return store.ListUserAccountLimits(ctx, r.ORM(), owner.Key())
}

// ReorderOwnedProviderAccounts sets the order of the owner's accounts of provider. orderedIDs
// must be exactly those accounts (core.ErrInvalidAccountOrder otherwise).
func (r *Repo) ReorderOwnedProviderAccounts(ctx context.Context, owner core.AccountOwner, provider string, orderedIDs []string) error {
	return r.accounts.ReorderOwnedProviderAccounts(ctx, owner, provider, orderedIDs)
}

// DeleteOwnedProviderAccount removes one of the owner's accounts and destroys its ciphertext.
func (r *Repo) DeleteOwnedProviderAccount(ctx context.Context, owner core.AccountOwner, accountID string) error {
	return r.accounts.DeleteOwnedProviderAccount(ctx, owner, accountID)
}

// RotateOrgAPIKey replaces the key of one of orgID's API-key accounts in place.
func (r *Repo) RotateOrgAPIKey(ctx context.Context, orgID, accountID string, k core.OrgAPIKey) (core.ProviderAccount, error) {
	return r.accounts.RotateOrgAPIKey(ctx, orgID, accountID, k)
}

// SetOrgAccountAccess sets who in orgID may use one of its accounts.
func (r *Repo) SetOrgAccountAccess(ctx context.Context, orgID, accountID, access string) (core.ProviderAccount, error) {
	if !core.ValidOrgAccess(access) {
		return core.ProviderAccount{}, fmt.Errorf("invalid org access %q", access)
	}
	return store.SetOrgProviderAccountAccess(ctx, r.ORM(), orgID, accountID, access, r.accounts.now())
}

// ListOrgAccountGrants returns every grant of orgID's accounts.
func (r *Repo) ListOrgAccountGrants(ctx context.Context, orgID string) ([]core.OrgAccountGrant, error) {
	return store.ListOrgProviderAccountGrants(ctx, r.ORM(), orgID)
}

// SetOrgAccountGrants makes userIDs exactly the members granted one of orgID's accounts, each
// granted (or re-granted) now: a re-grant renews a grant left from an earlier membership.
func (r *Repo) SetOrgAccountGrants(ctx context.Context, orgID, accountID string, userIDs []string, grantedBy string) error {
	return r.accounts.SetOrgAccountGrants(ctx, orgID, accountID, userIDs, grantedBy)
}

// ListOrgProviderWorkspaces returns the provider workspaces bound to orgID.
func (r *Repo) ListOrgProviderWorkspaces(ctx context.Context, orgID string) ([]core.OrgProviderWorkspace, error) {
	return store.ListOrgProviderWorkspacesOf(ctx, r.ORM(), orgID)
}

// BindOrgProviderWorkspace binds w to w.OrgID. created is false when the org already holds the
// binding; core.ErrProviderWorkspaceBoundElsewhere when another org does.
func (r *Repo) BindOrgProviderWorkspace(ctx context.Context, w core.OrgProviderWorkspace) (core.OrgProviderWorkspace, bool, error) {
	return r.accounts.BindOrgProviderWorkspace(ctx, w)
}

// UnbindOrgProviderWorkspace removes one of orgID's bindings and clears the attributions it made.
func (r *Repo) UnbindOrgProviderWorkspace(ctx context.Context, orgID, provider, externalOrgID string) error {
	return r.accounts.UnbindOrgProviderWorkspace(ctx, orgID, provider, externalOrgID)
}

// ListOrgSeats returns the members' accounts orgID pays for as seats (attribution source
// workspace or approved). The managed key minted in the org is not a seat.
func (r *Repo) ListOrgSeats(ctx context.Context, orgID string) ([]core.ProviderAccount, error) {
	all, err := store.ListAttributedProviderAccounts(ctx, r.ORM(), orgID)
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

// ReleaseOrgSeat makes one seat orgID pays for the member's personal account again, and records
// that releasedBy released it, so the binding matcher never makes it the org's seat again (§6).
// core.ErrOrgSeatNotFound when the account is not one of the org's seats.
func (r *Repo) ReleaseOrgSeat(ctx context.Context, orgID, accountID, releasedBy string) error {
	return r.accounts.ReleaseOrgSeat(ctx, orgID, accountID, releasedBy)
}

// ProviderWorkspaceBinding returns the binding of provider's workspace externalOrgID, or nil.
func (r *Repo) ProviderWorkspaceBinding(ctx context.Context, provider, externalOrgID string) (*core.OrgProviderWorkspace, error) {
	return store.GetProviderWorkspaceBinding(ctx, r.ORM(), provider, externalOrgID)
}

// ListWorkspaceSignIns returns every person's sign-in in provider's workspace externalOrgID.
func (r *Repo) ListWorkspaceSignIns(ctx context.Context, provider, externalOrgID string) ([]core.ProviderAccount, error) {
	return store.ListWorkspaceSignIns(ctx, r.ORM(), provider, externalOrgID)
}

// AttributeWorkspaceSeat makes accountID, a person's sign-in in w, w's org's seat while the
// binding exists; reports whether it did (seatmatch).
func (r *Repo) AttributeWorkspaceSeat(ctx context.Context, w core.OrgProviderWorkspace, accountID string) (bool, error) {
	return r.accounts.AttributeWorkspaceSeat(ctx, w, accountID)
}

// ClearWorkspaceSeat makes accountID, a seat orgID's binding matched, the person's own again
// when no binding of orgID covers its workspace any more; reports whether it did (seatmatch).
func (r *Repo) ClearWorkspaceSeat(ctx context.Context, orgID, accountID string) (bool, error) {
	return r.accounts.ClearWorkspaceSeat(ctx, orgID, accountID)
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
func (s *accountStore) RotateOrgAPIKey(ctx context.Context, orgID, accountID string, k core.OrgAPIKey) (core.ProviderAccount, error) {
	owner := core.OrgAccountOwner(orgID)
	if !owner.Valid() {
		return core.ProviderAccount{}, core.ErrProviderAccountNotFound
	}
	if k.Secret == "" || k.IdentityKey == "" {
		return core.ProviderAccount{}, errors.New("a rotated key needs its secret and identity")
	}
	key := owner.Key()
	provider, err := store.UserProviderAccountProvider(ctx, s.db, key, accountID)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	var out core.ProviderAccount
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := store.LockUserProviderAccounts(ctx, s.db, key, provider); err != nil {
			return err
		}
		cur, err := store.LockOwnerProviderAccount(ctx, s.db, owner, accountID)
		if err != nil {
			return err
		}
		if cur.AuthKind != core.AuthKindAPIKey || cur.Provider != provider {
			return core.ErrProviderAccountNotFound
		}
		other, err := store.LockProviderAccountByIdentity(ctx, s.db, key, provider, k.IdentityKey)
		if err != nil {
			return err
		}
		if other != nil && other.ID != cur.ID {
			return core.ErrOrgAPIKeyInUse
		}
		c, err := s.credential(ctx, owner, cur.ID, core.AccountUpsert{
			AuthKind: core.AuthKindAPIKey, KeyHint: k.KeyHint, Secrets: core.AccountSecrets{Secret: k.Secret},
		}, k.IdentityKey, nil)
		if err != nil {
			return err
		}
		out, err = store.RewriteProviderAccountCredential(ctx, s.db, key, cur.ID, c)
		return err
	})
	return out, err
}

// SetOrgAccountGrants replaces the grants of one of orgID's accounts, under the account's row
// lock so a concurrent delete cannot leave grants behind.
func (s *accountStore) SetOrgAccountGrants(ctx context.Context, orgID, accountID string, userIDs []string, grantedBy string) error {
	owner := core.OrgAccountOwner(orgID)
	if !owner.Valid() {
		return core.ErrProviderAccountNotFound
	}
	if grantedBy == "" {
		return errors.New("a grant records who made it")
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		if _, err := store.LockOwnerProviderAccount(ctx, s.db, owner, accountID); err != nil {
			return err
		}
		return store.ReplaceProviderAccountGrants(ctx, s.db, orgID, accountID, userIDs, grantedBy, s.now())
	})
}

// BindOrgProviderWorkspace records the binding; a workspace another org holds is
// core.ErrProviderWorkspaceBoundElsewhere, never naming that org.
func (s *accountStore) BindOrgProviderWorkspace(ctx context.Context, w core.OrgProviderWorkspace) (core.OrgProviderWorkspace, bool, error) {
	got, created, err := store.InsertOrgProviderWorkspace(ctx, s.db, w, s.now())
	if err != nil && orm.ConstraintName(err) == store.OrgProviderWorkspaceConstraintOneOrg {
		return core.OrgProviderWorkspace{}, false, core.ErrProviderWorkspaceBoundElsewhere
	}
	return got, created, err
}

// UnbindOrgProviderWorkspace deletes the binding and, in the same transaction, clears every
// attribution it made (source 'workspace' in that workspace), so no seat outlives its binding.
func (s *accountStore) UnbindOrgProviderWorkspace(ctx context.Context, orgID, provider, externalOrgID string) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		deleted, err := store.DeleteOrgProviderWorkspace(ctx, s.db, orgID, provider, externalOrgID)
		if err != nil {
			return err
		}
		if !deleted {
			return core.ErrProviderWorkspaceNotFound
		}
		if _, err := store.ClearWorkspaceAttributions(ctx, s.db, orgID, provider, externalOrgID, s.now()); err != nil {
			return err
		}
		// The org's releases in it go too, so binding it again starts afresh. Accounts before
		// releases, the order ReleaseOrgSeat locks them in.
		_, err = store.DeleteWorkspaceSeatReleases(ctx, s.db, orgID, provider, externalOrgID)
		return err
	})
}

// AttributeWorkspaceSeat attributes accountID to w's org under a share lock on the binding, so an
// unbind either sees the new seat (and clears it) or has already removed the binding (and this
// attributes nothing).
func (s *accountStore) AttributeWorkspaceSeat(ctx context.Context, w core.OrgProviderWorkspace, accountID string) (bool, error) {
	var done bool
	err := s.inTx(ctx, func(ctx context.Context) error {
		bound, err := store.LockOrgProviderWorkspaceShared(ctx, s.db, w.OrgID, w.Provider, w.ExternalOrgID)
		if err != nil || !bound {
			return err
		}
		done, err = store.AttributeWorkspaceSeat(ctx, s.db, w, accountID, s.now())
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
func (s *accountStore) ClearWorkspaceSeat(ctx context.Context, orgID, accountID string) (bool, error) {
	var cleared bool
	err := s.inTx(ctx, func(ctx context.Context) error {
		a, err := store.LockProviderAccountByID(ctx, s.db, accountID)
		if err != nil || a == nil || a.AttributedOrgID != orgID || a.AttributionSource != core.AttributionWorkspace {
			return err
		}
		if ws := core.ProviderWorkspaceID(*a); ws != "" {
			b, err := store.GetProviderWorkspaceBinding(ctx, s.db, a.Provider, ws)
			if err != nil {
				return err
			}
			if b != nil && b.OrgID == orgID {
				return nil // still the org's seat
			}
		}
		cleared, err = store.ClearProviderAccountAttribution(ctx, s.db, orgID, accountID,
			[]string{core.AttributionWorkspace}, s.now())
		return err
	})
	return cleared, err
}

// ReleaseOrgSeat clears one of orgID's seats and records the release in one transaction.
func (s *accountStore) ReleaseOrgSeat(ctx context.Context, orgID, accountID, releasedBy string) error {
	if releasedBy == "" {
		return errors.New("a release records who made it")
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		released, err := store.ClearProviderAccountAttribution(ctx, s.db, orgID, accountID, seatSources, s.now())
		if err != nil {
			return err
		}
		if !released {
			return core.ErrOrgSeatNotFound
		}
		return store.InsertOrgSeatRelease(ctx, s.db, orgID, accountID, releasedBy, s.now())
	})
}

// HasRejectedOrgKeys reports whether any org-owned API key needs attention (store.HasRejectedOrgKeys).
func (r *Repo) HasRejectedOrgKeys(ctx context.Context) (bool, error) {
	return store.HasRejectedOrgKeys(ctx, r.ORM())
}

// ListRejectedOrgKeysIn returns the rejected org-owned API keys of orgIDs; the inbox lists them to
// those orgs' owners and admins.
func (r *Repo) ListRejectedOrgKeysIn(ctx context.Context, orgIDs []string) ([]core.ProviderAccount, error) {
	return store.ListRejectedOrgKeysIn(ctx, r.ORM(), orgIDs)
}
