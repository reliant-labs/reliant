// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListInbox's org scope (org_id): which org each item is about, read before
// the scope applies, and the filter itself.

// inScope keeps the items of scope's org (all of them for a nil scope). An
// item whose org could not be read is left out of every named org, the
// default included (fail closed): it is never taken for one of no org.
func (r *inboxReads) inScope(items []*reliantv1.InboxItem, scope *core.OrgScope) []*reliantv1.InboxItem {
	if scope == nil {
		return items
	}
	kept := items[:0]
	for _, it := range items {
		if org, decided := r.itemOrg(it); decided && scope.Includes(org) {
			kept = append(kept, it)
		}
	}
	return kept
}

// itemOrgKeys is what an item's org is decided by, in order: its chat, else
// its automation, else its project. None for an item about a machine (its
// storage, its worktrees), which belongs to no org.
func itemOrgKeys(it *reliantv1.InboxItem) []string {
	switch it.Kind {
	case reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE, reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_RECLAIM:
		return nil
	}
	var keys []string
	for _, k := range [][2]string{{"chat:", it.ChatId}, {"trigger:", it.TriggerId}, {"project:", it.ProjectId}} {
		if k[1] != "" {
			keys = append(keys, k[0]+k[1])
		}
	}
	return keys
}

// itemOrg is the org an item is about: the first of its keys (itemOrgKeys)
// whose org was read, nil for one with no org yet (or a project that is
// gone). decided is false when it names something but no org of it was read
// (a failed read); an item about a machine is decided, with no org, and
// lists in every scope.
func (r *inboxReads) itemOrg(it *reliantv1.InboxItem) (org *string, decided bool) {
	keys := itemOrgKeys(it)
	if len(keys) == 0 {
		return nil, true
	}
	for _, key := range keys {
		if org, read := r.orgs[key]; read {
			return org, true
		}
	}
	return nil, false
}

// listScope is ListInbox's org filter (orgs.ListScope): nil when the request
// names no org, or the deployment has none.
func (s *Service) listScope(ctx context.Context, userID, requested string) (*core.OrgScope, error) {
	if s.deps.Orgs == nil {
		return nil, nil
	}
	return s.deps.Orgs.ListScope(ctx, userID, requested, s.deps.Repo.AssignUnassignedOrg)
}

// resolveOrg reads the org of what it is about (itemOrgKeys), unless one of
// its keys already was: a chat, or a project; a trigger's org is known from
// the triggers listed.
func (r *itemReads) resolveOrg(ctx context.Context, userID string, it *reliantv1.InboxItem) {
	keys := itemOrgKeys(it)
	r.mu.Lock()
	for _, key := range keys {
		if _, read := r.orgs[key]; read {
			r.mu.Unlock()
			return
		}
	}
	r.mu.Unlock()
	if it.ChatId != "" {
		r.chat(ctx, userID, it.ChatId)
		r.mu.Lock()
		_, read := r.orgs["chat:"+it.ChatId]
		r.mu.Unlock()
		if read {
			return
		}
	}
	// No chat, or its read failed: the project decides.
	if it.ProjectId != "" {
		r.project(ctx, userID, it.ProjectId)
	}
}
