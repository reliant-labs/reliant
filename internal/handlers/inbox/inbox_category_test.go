package inbox

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/store"
)

// Every kind has a category. Machine storage is housekeeping at every severity.
func TestInboxCategory_EveryKind(t *testing.T) {
	const (
		needsYou     = reliantv1.InboxCategory_INBOX_CATEGORY_NEEDS_YOU
		alert        = reliantv1.InboxCategory_INBOX_CATEGORY_ALERT
		automation   = reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION
		housekeeping = reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING
	)
	storage := reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE
	for _, tc := range []struct {
		name string
		kind reliantv1.InboxItemKind
		id   string
		want reliantv1.InboxCategory
	}{
		{"approval", reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL, "approval:a", needsYou},
		{"question", reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION, "question:q", needsYou},
		{"waiting for machine", reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE, "waiting_for_machine:c", needsYou},
		{"personal funding", reliantv1.InboxItemKind_INBOX_ITEM_KIND_PERSONAL_FUNDING, "personal_funding:c", needsYou},
		{"recovery", reliantv1.InboxItemKind_INBOX_ITEM_KIND_RECOVERY, "recovery:r", alert},
		{"definition broken", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN, "automation_definition_broken:x", alert},
		{"needs setup", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP, "automation_needs_setup:t:repos", alert},
		{"org key rejected", reliantv1.InboxItemKind_INBOX_ITEM_KIND_ORG_PROVIDER_KEY_REJECTED, "org_provider_key_rejected:k", alert},
		{"no org account", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NO_ORG_ACCOUNT, "automation_no_org_account:t", alert},
		{"on personal accounts", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATIONS_ON_PERSONAL_ACCOUNTS, "automations_on_personal_accounts:o", alert},
		{"failing", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING, "automation_failing:ev", automation},
		{"broken declaration is still an automation failure", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING, "automation_failing:broken:t:ab", automation},
		{"launch failed", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED, "automation_launch_failed:ev", automation},
		{"run paused", reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_RUN_PAUSED, "automation_run_paused:c", automation},
		{"disk failing builds", storage, storageItemID("d1", diskwatch.SeverityFailing, 5), housekeeping},
		{"disk critical", storage, storageItemID("d1", diskwatch.SeverityCritical, 5), housekeeping},
		{"disk low", storage, storageItemID("d1", diskwatch.SeverityLow, 5), housekeeping},
		{"kept worktrees", storage, storageItemID("d1", diskwatch.SeverityNone, 0), housekeeping},
		{"near the process limit", storage, "storage:d1:procs:5", housekeeping},
		{"worktree reclaim", reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_RECLAIM, "worktree_reclaim:d1:ab", housekeeping},
		{"worktree marked", reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_MARKED, "worktree_marked:w:m", housekeeping},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, inboxCategory(&reliantv1.InboxItem{Kind: tc.kind, ItemId: tc.id}))
		})
	}
	for kind := range reliantv1.InboxItemKind_name {
		if k := reliantv1.InboxItemKind(kind); k != reliantv1.InboxItemKind_INBOX_ITEM_KIND_UNSPECIFIED {
			assert.NotEqual(t, reliantv1.InboxCategory_INBOX_CATEGORY_UNSPECIFIED, inboxCategory(&reliantv1.InboxItem{Kind: k}), "%s has no category", k)
		}
	}
}

// Only a trigger's failure streak can be habitual: broken, source and
// incident items have identities of their own.
func TestStreakItem(t *testing.T) {
	failing := reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING
	for id, want := range map[string]bool{
		"automation_failing:ev-1":            true,
		"automation_failing:broken:t:ab":     false,
		"automation_failing:source:t:123":    false,
		"automation_failing:incident:ep":     false,
		"automation_launch_failed:ev-1":      false,
		"automation_failing:ev-1:no-trigger": true,
	} {
		assert.Equal(t, want, streakItem(&reliantv1.InboxItem{Kind: failing, ItemId: id, TriggerId: "t"}), id)
	}
	assert.False(t, streakItem(&reliantv1.InboxItem{Kind: failing, ItemId: "automation_failing:ev-1"}), "an item with no trigger")
}

func TestFilterCategories(t *testing.T) {
	item := func(id string, cat reliantv1.InboxCategory, habitual bool, project string) *reliantv1.InboxItem {
		return &reliantv1.InboxItem{ItemId: id, Category: cat, Habitual: habitual, ProjectId: project}
	}
	all := func() []*reliantv1.InboxItem {
		return []*reliantv1.InboxItem{
			item("needs", reliantv1.InboxCategory_INBOX_CATEGORY_NEEDS_YOU, false, "p"),
			item("alert", reliantv1.InboxCategory_INBOX_CATEGORY_ALERT, false, "p"),
			item("new-failure", reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION, false, "p"),
			item("habitual", reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION, true, "p"),
			item("habitual-elsewhere", reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION, true, "q"),
			item("disk", reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING, false, ""),
			item("reclaim", reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING, false, ""),
			// An older server's item, or a kind this one does not know.
			item("unknown", reliantv1.InboxCategory_INBOX_CATEGORY_UNSPECIFIED, false, "p"),
		}
	}
	inP := func(it *reliantv1.InboxItem) bool { return it.ProjectId != "q" }
	ids := func(items []*reliantv1.InboxItem) []string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.ItemId
		}
		return out
	}
	for _, tc := range []struct {
		name                    string
		categories              []reliantv1.InboxCategory
		filters                 inboxFilters
		want                    []string
		housekeeping, habitualN int32
	}{
		{
			name:         "the default inbox hides housekeeping and habitual failures, and counts those in scope",
			want:         []string{"needs", "alert", "new-failure", "unknown"},
			housekeeping: 2, habitualN: 1,
		},
		{
			name:      "show housekeeping",
			filters:   inboxFilters{showHousekeeping: true},
			want:      []string{"needs", "alert", "new-failure", "disk", "reclaim", "unknown"},
			habitualN: 1,
		},
		{
			name:         "show every automation failure",
			filters:      inboxFilters{showEveryAutomationFailure: true},
			want:         []string{"needs", "alert", "new-failure", "habitual", "habitual-elsewhere", "unknown"},
			housekeeping: 2,
		},
		{
			name:    "both filters on is the whole inbox",
			filters: inboxFilters{showHousekeeping: true, showEveryAutomationFailure: true},
			want:    []string{"needs", "alert", "new-failure", "habitual", "habitual-elsewhere", "disk", "reclaim", "unknown"},
		},
		{
			name:       "categories list all of them, whatever the filters, and count nothing",
			categories: []reliantv1.InboxCategory{reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING, reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION},
			want:       []string{"new-failure", "habitual", "habitual-elsewhere", "disk", "reclaim"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &reliantv1.ListInboxResponse{}
			got := filterCategories(all(), tc.categories, tc.filters, inP, resp)
			assert.Equal(t, tc.want, ids(got))
			assert.EqualValues(t, tc.housekeeping, resp.HousekeepingCount)
			assert.EqualValues(t, tc.habitualN, resp.HabitualCount)
		})
	}
}

// runFiring records a launched firing whose run ended failed or completed, as
// WorkflowStatus records it (run_status on the launch event).
func (f *inboxFixture) runFiring(t *testing.T, eventID, triggerID string, at time.Time, failed bool) {
	t.Helper()
	status, runStatus := db.Completed(), core.TriggerRunCompleted
	if failed {
		status, runStatus = db.Failed(), core.TriggerRunFailed
	}
	chatID := "chat-" + eventID
	f.launched(t, eventID, chatID, triggerID, at, status)
	require.NoError(t, store.SetLaunchEventRunStatus(f.ctx, f.repo.ORM(), chatID, runStatus))
}

func (f *inboxFixture) setFilter(t *testing.T, key string, on bool) {
	t.Helper()
	value := "false"
	if on {
		value = "true"
	}
	now := time.Now().UTC()
	require.NoError(t, store.UpsertSetting(f.ctx, f.repo.ORM(), &core.Setting{
		ID: uuid.NewString(), UserID: f.userID, Key: key, Value: value, ValueType: "string", CreatedAt: now, UpdatedAt: now,
	}))
}

func (f *inboxFixture) listCategories(t *testing.T, categories ...reliantv1.InboxCategory) *reliantv1.ListInboxResponse {
	t.Helper()
	f.inbox.storageCache.Delete(f.userID)
	resp, err := f.inbox.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{Categories: categories}))
	require.NoError(t, err)
	return resp.Msg
}

// An automation that flaps — fails, succeeds, fails again within a day — is
// one Inbox item for the period it is unhealthy: the first streak is news,
// a later one is habitual and listed only with "every automation failure".
// A failure after a quiet day is news again.
func TestListInbox_FlappingAutomationIsOneItemPerUnhealthyPeriod(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "trg-flap")
	base := time.Now().UTC().Add(-10 * time.Hour)
	failing := func(resp *reliantv1.ListInboxResponse) []*reliantv1.InboxItem {
		return inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	}

	f.runFiring(t, "flap-1", "trg-flap", base, true)
	resp := f.list(t, nil)
	first := failing(resp)
	require.Len(t, first, 1, "the first failure after a healthy spell is listed")
	assert.Equal(t, "automation_failing:flap-1", first[0].ItemId)
	assert.False(t, first[0].Habitual)
	assert.Equal(t, reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION, first[0].Category)
	assert.Zero(t, resp.HabitualCount)

	f.runFiring(t, "flap-2", "trg-flap", base.Add(time.Hour), false)
	assert.Empty(t, failing(f.list(t, nil)), "a success ends the streak")

	f.runFiring(t, "flap-3", "trg-flap", base.Add(2*time.Hour), true)
	resp = f.list(t, nil)
	assert.Empty(t, failing(resp), "a streak a day after the last failure is habitual: not listed by default")
	assert.EqualValues(t, 1, resp.HabitualCount)

	f.setFilter(t, showEveryAutomationFailureKey, true)
	resp = f.list(t, nil)
	again := failing(resp)
	require.Len(t, again, 1, "every automation failure lists the habitual streak")
	assert.Equal(t, "automation_failing:flap-3", again[0].ItemId, "the item id is unchanged: a dismissal still holds")
	assert.True(t, again[0].Habitual)
	assert.Zero(t, resp.HabitualCount)

	all := failing(f.listCategories(t, reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION))
	require.Len(t, all, 1)
	assert.True(t, all[0].Habitual)
}

func TestListInbox_FailureAfterAQuietDayIsNews(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "trg-quiet")
	now := time.Now().UTC()
	f.runFiring(t, "q-1", "trg-quiet", now.Add(-30*time.Hour), true)
	f.runFiring(t, "q-2", "trg-quiet", now.Add(-29*time.Hour), false)
	f.runFiring(t, "q-3", "trg-quiet", now.Add(-time.Hour), true)

	resp := f.list(t, nil)
	failing := inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, failing, 1)
	assert.Equal(t, "automation_failing:q-3", failing[0].ItemId)
	assert.False(t, failing[0].Habitual, "29 healthy hours: this failure is a transition")
	assert.Zero(t, resp.HabitualCount)
}

// A successful run separates launch failures: a new launch failure must not
// be hidden just because another failure occurred within the quiet window.
func TestListInbox_LaunchFailureAfterSuccessIsNews(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "trg-lf")
	base := time.Date(2026, time.January, 2, 9, 0, 0, 0, time.UTC)
	f.firing(t, "lf-1", "trg-lf", core.TriggerEventFailed, "daemon gone", base)
	resp := f.list(t, nil)
	launchFailed := inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED)
	require.Len(t, launchFailed, 1)
	assert.False(t, launchFailed[0].Habitual)
	assert.Equal(t, reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION, launchFailed[0].Category)

	f.runFiring(t, "lf-2", "trg-lf", base.Add(time.Hour), false)
	f.firing(t, "lf-3", "trg-lf", core.TriggerEventFailed, "gone again", base.Add(2*time.Hour))
	resp = f.list(t, nil)
	launchFailed = inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED)
	require.Len(t, launchFailed, 1)
	assert.Equal(t, "automation_launch_failed:lf-3", launchFailed[0].ItemId)
	assert.False(t, launchFailed[0].Habitual)
	assert.Zero(t, resp.HabitualCount)

	launchFailed = inboxByKind(f.listCategories(t, reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED)
	require.Len(t, launchFailed, 1)
	assert.Equal(t, "automation_launch_failed:lf-3", launchFailed[0].ItemId)
	assert.False(t, launchFailed[0].Habitual)
}

// Needs-you items are listed whatever the filters, and categories narrow to
// exactly what is asked for.
func TestListInbox_CategoriesNarrowTheList(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now().UTC().Add(-time.Minute)
	f.seed(t, "chat-c", "wf", db.Active(), now)
	f.approval(t, "appr-c", "chat-c", now)
	f.trigger(t, "trg-c")
	f.firing(t, "c-1", "trg-c", core.TriggerEventFailed, "boom", now)

	resp := f.list(t, nil)
	require.Len(t, resp.Items, 2)
	for _, it := range resp.Items {
		assert.NotEqual(t, reliantv1.InboxCategory_INBOX_CATEGORY_UNSPECIFIED, it.Category, it.ItemId)
	}

	needs := f.listCategories(t, reliantv1.InboxCategory_INBOX_CATEGORY_NEEDS_YOU)
	require.Len(t, needs.Items, 1)
	assert.Equal(t, "approval:appr-c", needs.Items[0].ItemId)
	assert.EqualValues(t, 1, needs.BlockingCount)

	auto := f.listCategories(t, reliantv1.InboxCategory_INBOX_CATEGORY_AUTOMATION)
	require.Len(t, auto.Items, 1)
	assert.Equal(t, "automation_launch_failed:c-1", auto.Items[0].ItemId)
	assert.Zero(t, auto.BlockingCount, "counts are of what is listed")

	assert.Empty(t, f.listCategories(t, reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING).Items)
}

// The filters read failing is advisory: the default Inbox is listed.
func TestReadFilters(t *testing.T) {
	ctx := context.Background()
	project := "p"
	s := &Service{deps: Deps{Preferences: &MockPreferences{ListSettingsByKeyFunc: func(context.Context, string, string) ([]*core.Setting, error) {
		return []*core.Setting{
			{Key: showHousekeepingKey, Value: "true"},
			{Key: showEveryAutomationFailureKey, Value: "true", ProjectID: &project}, // not the person's: a project's
			{Key: "inbox.something_else", Value: "true"},
		}, nil
	}}}}
	got, err := s.readFilters(ctx, "u")
	require.NoError(t, err)
	assert.Equal(t, inboxFilters{showHousekeeping: true}, got)

	got, err = (&Service{}).readFilters(ctx, "u")
	require.NoError(t, err)
	assert.Equal(t, inboxFilters{}, got, "no Preferences wired is the default inbox")
}

// newHousekeepingFixture is newInboxFixture for a person whose Inbox lists
// machine housekeeping too (inbox.show_housekeeping): the tests of the
// housekeeping items themselves read them from the Inbox as before.
func newHousekeepingFixture(t *testing.T) *inboxFixture {
	t.Helper()
	f := newInboxFixture(t)
	f.setFilter(t, showHousekeepingKey, true)
	return f
}

// By default a machine's housekeeping is not in the Inbox: it is counted, and
// the machine's page lists it by category even when the disk is critical.
func TestListInbox_HousekeepingIsOffTheDefaultInbox(t *testing.T) {
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID}))
	putCacheVolume(t, f, daemonID, 3<<30, true, 0) // under pressure, above the free floor: low

	resp := f.list(t, nil)
	assert.Empty(t, inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE), "a disk under pressure, not yet critical, is housekeeping")
	assert.EqualValues(t, 1, resp.HousekeepingCount)

	machine := f.listCategories(t, reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING, reliantv1.InboxCategory_INBOX_CATEGORY_ALERT)
	storage := inboxByKind(machine.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE)
	require.Len(t, storage, 1, "the machine's page lists it")
	assert.Equal(t, reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING, storage[0].Category)
	assert.Zero(t, machine.HousekeepingCount, "a categories list hides nothing")

	putCacheVolume(t, f, daemonID, 1<<30, true, 0)
	resp = f.list(t, nil)
	assert.Empty(t, inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE))
	assert.EqualValues(t, 1, resp.HousekeepingCount)
	machine = f.listCategories(t, reliantv1.InboxCategory_INBOX_CATEGORY_HOUSEKEEPING)
	storage = inboxByKind(machine.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE)
	require.Len(t, storage, 1, "a critical disk is still listed on the machine page")
	assert.Contains(t, storage[0].ItemId, ":critical")
}
