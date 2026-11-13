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

// orgKeyItems returns the rejected keys of the orgs userID administers. The candidate orgs are
// those userID has a footprint in (Repository.ListRejectedOrgKeysNear), and each is decided by
// userID's role there (cached like membership): an owner or admin sees its keys; anyone else, an
// unknown role or an outage, sees none.
func (s *Service) orgKeyItems(ctx context.Context, userID string) ([]*reliantv1.InboxItem, error) {
	if s.deps.Orgs == nil {
		return nil, nil
	}
	keys, err := s.deps.Repo.ListRejectedOrgKeysNear(ctx, userID)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	admin := map[string]bool{}
	var items []*reliantv1.InboxItem
	for _, a := range keys {
		isAdmin, decided := admin[a.OrgID]
		if !decided {
			role, ok, rerr := s.deps.Orgs.Role(ctx, userID, a.OrgID)
			isAdmin = rerr == nil && ok && (role == orgs.RoleOwner || role == orgs.RoleAdmin)
			admin[a.OrgID] = isAdmin
		}
		if isAdmin {
			items = append(items, orgKeyRejectedItem(a))
		}
	}
	return items, nil
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
