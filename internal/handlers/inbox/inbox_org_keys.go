// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// An org's rejected AI provider keys (control-plane
// docs/design/org-provider-accounts.md §4.7): when a provider refuses one of
// an org's own API keys, routing marks it needs-attention on the org's row and
// every member's chats route around it. Only the org's owners and admins can
// fix it, so only they are told: one item per key per incident, keyed by the
// account and when it was rejected, so a key rejected again after it was
// rotated is a new item. The member whose call hit the rejection is not told
// to sign in again: they cannot fix an org key.

// inboxOrgKeyRejectedPrefix keys a rejected org key: the account and when it was rejected.
const inboxOrgKeyRejectedPrefix = "org_provider_key_rejected:"

// callerJWTKey carries the caller's session into the shared inbox read, which
// asks the control plane for their orgs as them.
type callerJWTKey struct{}

func withCallerJWT(ctx context.Context, jwt string) context.Context {
	if jwt == "" {
		return ctx
	}
	return context.WithValue(ctx, callerJWTKey{}, jwt)
}

func callerJWT(ctx context.Context) string {
	jwt, _ := ctx.Value(callerJWTKey{}).(string)
	return jwt
}

// adminOrgsTTL bounds how long a user's administered orgs are reused across inbox reads: the
// badge polls, and a role change shows within it, like orgs.CacheTTL.
const adminOrgsTTL = 30 * time.Second

type adminOrgsEntry struct {
	ids     []string
	expires time.Time
}

// orgKeyItems returns the rejected keys of the orgs userID administers. Which orgs those are is
// control-plane's live answer (ListMyOrganizations, asked as the caller), never a guess from
// local rows: an owner or admin with nothing of their own in the org still hears of it. Nothing
// is asked while no org has a rejected key. An outage, a machine credential (which has no orgs to
// list) and a deployment with no control plane show nothing (fail closed).
func (s *Service) orgKeyItems(ctx context.Context, userID string) ([]*reliantv1.InboxItem, error) {
	jwt := callerJWT(ctx)
	if s.deps.MyOrgs == nil || jwt == "" {
		return nil, nil
	}
	any, err := s.deps.Repo.HasRejectedOrgKeys(ctx)
	if err != nil || !any {
		return nil, err
	}
	orgIDs, err := s.adminOrgIDs(ctx, userID, jwt)
	if err != nil || len(orgIDs) == 0 {
		return nil, err
	}
	keys, err := s.deps.Repo.ListRejectedOrgKeysIn(ctx, orgIDs)
	if err != nil {
		return nil, err
	}
	items := make([]*reliantv1.InboxItem, 0, len(keys))
	for _, a := range keys {
		items = append(items, orgKeyRejectedItem(a))
	}
	return items, nil
}

// adminOrgIDs is the orgs userID is a live owner or admin of, per control-plane, cached for
// adminOrgsTTL.
func (s *Service) adminOrgIDs(ctx context.Context, userID, jwt string) ([]string, error) {
	if v, ok := s.adminOrgs.Load(userID); ok {
		if e := v.(adminOrgsEntry); time.Now().Before(e.expires) {
			return e.ids, nil
		}
	}
	if s.deps.MyOrgs == nil {
		return nil, nil
	}
	mine, err := s.deps.MyOrgs.ListMyOrganizations(ctx, jwt)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, o := range mine {
		if o.Role == orgs.RoleOwner || o.Role == orgs.RoleAdmin {
			ids = append(ids, o.ID)
		}
	}
	s.adminOrgs.Store(userID, adminOrgsEntry{ids: ids, expires: time.Now().Add(adminOrgsTTL)})
	return ids, nil
}

// orgKeyRejectedItem is the inbox item of one rejected org key.
func orgKeyRejectedItem(a core.ProviderAccount) *reliantv1.InboxItem {
	var since time.Time
	if a.NeedsReauthAt != nil {
		since = a.NeedsReauthAt.UTC()
	}
	return &reliantv1.InboxItem{
		Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_ORG_PROVIDER_KEY_REJECTED,
		ItemId:       inboxOrgKeyRejectedPrefix + a.ID + "@" + since.Format("20060102T150405.000000"),
		WaitingSince: since.Format(time.RFC3339Nano),
		Payload: &reliantv1.InboxItem_OrgProviderKeyRejected{OrgProviderKeyRejected: &reliantv1.InboxOrgProviderKeyRejected{
			OrgId: a.OrgID, AccountId: a.ID, Provider: a.Provider, Label: a.DisplayLabel(), KeyHint: a.KeyHint,
			Reason: a.NeedsReauthReason,
		}},
	}
}

// orgKeyItemOrg is the org a rejected-key item belongs to, for ListInbox's org scope.
func orgKeyItemOrg(it *reliantv1.InboxItem) string {
	return it.GetOrgProviderKeyRejected().GetOrgId()
}
