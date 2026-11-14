// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"errors"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// The notices of control-plane docs/design/org-provider-accounts.md §11:
//
//   - An automation that may run only on its org's own accounts, paused
//     because none of them could run its model (core.TriggerPausedNoOrgAccount,
//     §11.4). Its owner is told, and so are the org's owners and admins, who
//     alone can add an org account: one item per pause, keyed by the
//     automation and when it was paused.
//   - The caller's automations in a team org that still run on their own
//     accounts (§11.3): the one-time notice at the release that brought the
//     rule, and again when a solo org gains a member. One item per org, keyed
//     by the org and the set, so dismissing it holds until the set gains one.

const (
	inboxNoOrgAccountPrefix       = "automation_no_org_account:"
	inboxOnPersonalAccountsPrefix = "automations_on_personal_accounts:"
)

// teamOrgTTL bounds how long an org's team-or-solo answer is reused across
// inbox reads: an org that gains a member shows the notice within it.
const teamOrgTTL = 5 * time.Minute

// maxListedTriggerIDs bounds InboxAutomationsOnPersonalAccounts.trigger_ids.
const maxListedTriggerIDs = 50

type teamOrgEntry struct {
	team    bool
	expires time.Time
}

// automationNoticeItems is both notices for userID. Each is read on its own:
// one that cannot be read does not hide the other.
func (s *Service) automationNoticeItems(ctx context.Context, userID string) ([]*reliantv1.InboxItem, error) {
	paused, pausedErr := s.noOrgAccountItems(ctx, userID)
	personal, personalErr := s.onPersonalAccountsItems(ctx, userID)
	return append(paused, personal...), errors.Join(pausedErr, personalErr)
}

// noOrgAccountItems lists the automations paused for want of an org account
// that userID owns, and, for a person, those of the orgs they administer
// (control-plane's live answer, as for rejected keys: orgKeyItems).
func (s *Service) noOrgAccountItems(ctx context.Context, userID string) ([]*reliantv1.InboxItem, error) {
	any, err := s.deps.Repo.HasNoOrgAccountPauses(ctx)
	if err != nil || !any {
		return nil, err
	}
	var adminOrgs []string
	if orgs.RequirePerson(ctx, "see an organization's paused automations") == nil {
		if jwt := callerJWT(ctx); jwt != "" && s.deps.MyOrgs != nil {
			ids, err := s.adminOrgIDs(ctx, userID, jwt)
			if err != nil {
				// The owner still hears of their own.
				logging.WarnContext(ctx, "Could not list the caller's administered orgs; listing only their own paused automations", "userID", userID, "error", err)
			}
			adminOrgs = ids
		}
	}
	triggers, err := s.deps.Repo.ListNoOrgAccountPauses(ctx, userID, adminOrgs)
	if err != nil {
		return nil, err
	}
	failed := core.TriggerEventFailed
	items := make([]*reliantv1.InboxItem, 0, len(triggers))
	for _, t := range triggers {
		detail := ""
		if ev, err := s.deps.Repo.GetLatestTriggerEvent(ctx, t.ID, &failed); err == nil && ev != nil {
			detail = ev.OutcomeDetail
		}
		items = append(items, noOrgAccountItem(t, userID, detail))
	}
	return items, nil
}

// noOrgAccountItem is the inbox item of one automation paused for want of an
// org account, as userID sees it.
func noOrgAccountItem(t *core.Trigger, userID, detail string) *reliantv1.InboxItem {
	var since time.Time
	if t.PausedAt != nil {
		since = t.PausedAt.UTC()
	}
	owner := ""
	if t.UserID != userID {
		owner = t.UserID
	}
	return &reliantv1.InboxItem{
		Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NO_ORG_ACCOUNT,
		ItemId:       inboxNoOrgAccountPrefix + t.ID + "@" + since.Format("20060102T150405.000000"),
		TriggerId:    t.ID,
		TriggerName:  t.Name,
		ProjectId:    t.ProjectID,
		ProjectName:  t.ProjectName,
		WaitingSince: since.Format(time.RFC3339Nano),
		Payload: &reliantv1.InboxItem_AutomationNoOrgAccount{AutomationNoOrgAccount: &reliantv1.InboxAutomationNoOrgAccount{
			OrgId: ptr.From(t.OrgID), OwnerUserId: owner, Detail: detail,
		}},
	}
}

// onPersonalAccountsItems lists, per team org, userID's automations there that
// may still spend their own accounts. An org whose shape cannot be read is
// left out (never a notice on a guess).
func (s *Service) onPersonalAccountsItems(ctx context.Context, userID string) ([]*reliantv1.InboxItem, error) {
	if s.deps.Members == nil {
		return nil, nil
	}
	triggers, err := s.deps.Repo.ListPersonallyFundedTriggers(ctx, userID)
	if err != nil || len(triggers) == 0 {
		return nil, err
	}
	byOrg := map[string][]*core.Trigger{}
	var orgIDs []string
	for _, t := range triggers {
		org := ptr.From(t.OrgID)
		if org == "" {
			continue
		}
		if _, ok := byOrg[org]; !ok {
			orgIDs = append(orgIDs, org)
		}
		byOrg[org] = append(byOrg[org], t)
	}
	sort.Strings(orgIDs)
	var items []*reliantv1.InboxItem
	for _, org := range orgIDs {
		if !s.isTeamOrg(ctx, userID, org) {
			continue
		}
		items = append(items, onPersonalAccountsItem(org, byOrg[org]))
	}
	return items, nil
}

// isTeamOrg reports whether org has two or more live members, cached for
// teamOrgTTL. An unknown count or a failed read is not a team.
func (s *Service) isTeamOrg(ctx context.Context, userID, org string) bool {
	if v, ok := s.teamOrgs.Load(org); ok {
		if e := v.(teamOrgEntry); time.Now().Before(e.expires) {
			return e.team
		}
	}
	n, known, err := s.deps.Members.LiveMembersFresh(ctx, userID, org)
	if err != nil {
		logging.WarnContext(ctx, "Could not read whether an org is a team; not listing automations on personal accounts", "orgID", org, "error", err)
		return false
	}
	team := known && n >= 2
	s.teamOrgs.Store(org, teamOrgEntry{team: team, expires: time.Now().Add(teamOrgTTL)})
	return team
}

// onPersonalAccountsItem is the item of org's automations on personal
// accounts (oldest first, as listed).
func onPersonalAccountsItem(org string, triggers []*core.Trigger) *reliantv1.InboxItem {
	ids := make([]string, 0, len(triggers))
	newest := time.Time{}
	for _, t := range triggers {
		ids = append(ids, t.ID)
		if t.CreatedAt.After(newest) {
			newest = t.CreatedAt
		}
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	listed := ids
	if len(listed) > maxListedTriggerIDs {
		listed = listed[:maxListedTriggerIDs]
	}
	return &reliantv1.InboxItem{
		Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATIONS_ON_PERSONAL_ACCOUNTS,
		ItemId:       inboxOnPersonalAccountsPrefix + org + "@" + hex.EncodeToString(sum[:8]),
		WaitingSince: newest.UTC().Format(time.RFC3339Nano),
		Payload: &reliantv1.InboxItem_AutomationsOnPersonalAccounts{AutomationsOnPersonalAccounts: &reliantv1.InboxAutomationsOnPersonalAccounts{
			OrgId: org, Count: int32(len(ids)), TriggerIds: listed,
		}},
	}
}

// automationNoticeOrg is the org one of these notices belongs to, for
// ListInbox's org scope.
func automationNoticeOrg(it *reliantv1.InboxItem) (string, bool) {
	switch it.Kind {
	case reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NO_ORG_ACCOUNT:
		return it.GetAutomationNoOrgAccount().GetOrgId(), true
	case reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATIONS_ON_PERSONAL_ACCOUNTS:
		return it.GetAutomationsOnPersonalAccounts().GetOrgId(), true
	}
	return "", false
}
