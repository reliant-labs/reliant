package inbox

import (
	"context"
	"slices"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// The person's Inbox filters (settings, user-global). The settings handler
// reads and writes the same keys (GetPreferences / UpdatePreferences).
const (
	showHousekeepingKey           = "inbox.show_housekeeping"
	showEveryAutomationFailureKey = "inbox.show_every_automation_failure"
)

// inboxFilters are the person's Inbox filters; the zero value is the default
// Inbox: no housekeeping, and only automations that were healthy before.
type inboxFilters struct {
	showHousekeeping           bool
	showEveryAutomationFailure bool
}

// readFilters reads the person's Inbox filters in one query. nil Preferences
// (a deployment that wires none) is the defaults.
func (s *Service) readFilters(ctx context.Context, userID string) (inboxFilters, error) {
	var f inboxFilters
	if s.deps.Preferences == nil {
		return f, nil
	}
	rows, err := s.deps.Preferences.ListSettingsByKey(ctx, userID, "inbox.%")
	if err != nil {
		return f, err
	}
	for _, r := range rows {
		// User-global only: the filters are the person's, not a project's.
		if r.ProjectID != nil {
			continue
		}
		switch r.Key {
		case showHousekeepingKey:
			f.showHousekeeping = r.Value == "true"
		case showEveryAutomationFailureKey:
			f.showEveryAutomationFailure = r.Value == "true"
		}
	}
	return f, nil
}

// inboxCategory is what an item is about (reliantv1.InboxCategory), which
// decides whether the default Inbox lists it.
func inboxCategory(it *reliantv1.InboxItem) reliantv1.InboxCategory {
	switch it.Kind {
	case reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_PERSONAL_FUNDING:
		return reliantv1.InboxCategory_INBOX_CATEGORY_NEEDS_YOU
	case reliantv1.InboxItemKind_INBOX_ITEM_KIND_RECOVERY,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_ORG_PROVIDER_KEY_REJECTED,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NO_ORG_ACCOUNT,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATIONS_ON_PERSONAL_ACCOUNTS:
		return reliantv1.InboxCategory_INBOX_CATEGORY_ALERT
	case reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_RUN_PAUSED:
		return reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION
	case reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_RECLAIM,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_MARKED:
		return reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING
	}
	return reliantv1.InboxCategory_INBOX_CATEGORY_UNSPECIFIED
}

// filterCategories keeps the items ListInbox lists, in order. With categories
// named, exactly those, all of them; otherwise the Inbox as the person's
// filters shape it, counting what they kept out. inProject says whether an
// item is in the request's project scope (nil: every item is): only those
// are counted, so a count says what this scope would list. Scope is not an
// access decision; readInbox already made those.
func filterCategories(items []*reliantv1.InboxItem, categories []reliantv1.InboxCategory, f inboxFilters, inProject func(*reliantv1.InboxItem) bool, resp *reliantv1.ListInboxResponse) []*reliantv1.InboxItem {
	if len(categories) > 0 {
		return slices.DeleteFunc(items, func(it *reliantv1.InboxItem) bool {
			return !slices.Contains(categories, it.Category)
		})
	}
	counted := func(it *reliantv1.InboxItem) bool { return inProject == nil || inProject(it) }
	return slices.DeleteFunc(items, func(it *reliantv1.InboxItem) bool {
		switch {
		case it.Category == reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING && !f.showHousekeeping:
			if counted(it) {
				resp.HousekeepingCount++
			}
			return true
		case it.Category == reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION && it.Habitual && !f.showEveryAutomationFailure:
			if counted(it) {
				resp.HabitualCount++
			}
			return true
		}
		return false
	})
}

// streakItem reports whether an AUTOMATION_FAILING item is a trigger's
// failure streak (automation_failing:<first failing event>), the one failing
// item that may be habitual. A broken declaration, a source that cannot
// authenticate and an attention incident each have a stable identity of
// their own already, and are never habitual.
func streakItem(it *reliantv1.InboxItem) bool {
	rest, ok := strings.CutPrefix(it.ItemId, inboxFailingPrefix)
	if !ok || it.Kind != reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING || it.TriggerId == "" {
		return false
	}
	for _, infix := range []string{brokenItemInfix, "source:", "incident:"} {
		if strings.HasPrefix(rest, infix) {
			return false
		}
	}
	return true
}

// habitualCandidate is an item that is habitual when its trigger failed in
// the QuietWindow before start.
type habitualCandidate struct {
	key       string
	triggerID string
	start     time.Time
}

// readHabitual decides which candidates are habitual (triggers.Habitual), in
// one read of each trigger's newest failure before its candidate began, keyed
// by candidate key. Advisory: an item the read could not decide is listed as
// news (not habitual), and the failure is logged.
func (s *Service) readHabitual(ctx context.Context, userID string, candidates []habitualCandidate) map[string]bool {
	if len(candidates) == 0 {
		return nil
	}
	ids := make([]string, len(candidates))
	before := make([]time.Time, len(candidates))
	for i, c := range candidates {
		ids[i], before[i] = c.triggerID, c.start
	}
	prior, err := s.deps.Firings.PriorTriggerFailures(ctx, userID, ids, before, triggers.QuietWindow)
	if err != nil {
		if ctx.Err() == nil {
			logging.WarnContext(ctx, "Failed to read prior automation failures for inbox; listing every failure", "error", err)
		}
		return nil
	}
	out := make(map[string]bool, len(candidates))
	for i, c := range candidates {
		if i < len(prior) && triggers.Habitual(c.start, prior[i]) {
			out[c.key] = true
		}
	}
	return out
}

// pendingItemID is the item id inboxItemFromPending gives p.
func pendingItemID(p *core.InboxPending) string {
	return inboxItemFromPending(p).ItemId
}
