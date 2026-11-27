package inbox

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// legacyEpisodes is the Inbox's firings as it read them before InboxEpisodes:
// every firing of each episode, definitions and skipped ones included.
type legacyEpisodes struct {
	store.TriggerEventExtStore
}

func (l legacyEpisodes) InboxEpisodes(ctx context.Context, userID string, ids []string, per, _ int) (map[string][]*core.TriggerEventWithRun, error) {
	return l.FiringsSinceLastUnmanagedSuccess(ctx, userID, ids, per)
}

func (l legacyEpisodes) InboxEpisodeFailures(ctx context.Context, userID string, ids []string, per int) (map[string][]*core.TriggerEventWithRun, error) {
	return l.FiringsSinceLastSuccess(ctx, userID, ids, per)
}

// countingEpisodes counts the firing rows the episode reads return. ListInbox
// runs its reads concurrently (failingAutomations' errgroup), so the count is
// guarded.
type countingEpisodes struct {
	store.TriggerEventExtStore
	mu   sync.Mutex
	rows map[string]int
}

func (c *countingEpisodes) count(out map[string][]*core.TriggerEventWithRun) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, firings := range out {
		c.rows[id] += len(firings)
	}
}

func (c *countingEpisodes) InboxEpisodes(ctx context.Context, userID string, ids []string, per, window int) (map[string][]*core.TriggerEventWithRun, error) {
	out, err := c.TriggerEventExtStore.InboxEpisodes(ctx, userID, ids, per, window)
	c.count(out)
	return out, err
}

func (c *countingEpisodes) InboxEpisodeFailures(ctx context.Context, userID string, ids []string, per int) (map[string][]*core.TriggerEventWithRun, error) {
	out, err := c.TriggerEventExtStore.InboxEpisodeFailures(ctx, userID, ids, per)
	c.count(out)
	return out, err
}

func (f *inboxFixture) inboxWithFirings(t *testing.T, firings Firings) *Service {
	t.Helper()
	d := realDeps(f.repo)
	d.Firings = firings
	d.Storage = realStorage(f.repo)
	return NewTestInbox(t, withDeps(d))
}

func listWith(t *testing.T, f *inboxFixture, svc *Service) *reliantv1.ListInboxResponse {
	t.Helper()
	svc.storageCache.Delete(f.userID)
	resp, err := svc.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	return resp.Msg
}

// The Inbox lists the same items from its trimmed episode reads as from every
// firing of each episode, across mixes of skipped, failed, launched and
// attention-managed firings: skipped firings both inside and past the health
// window, managed runs shifting which firings that window holds, successes
// managed and not. The mixes are drawn from a fixed seed, so the test is
// deterministic.
func TestListInbox_TrimmedEpisodeReadsListWhatEveryFiringDoes(t *testing.T) {
	f := newInboxFixture(t)
	rng := rand.New(rand.NewSource(20261125))
	// A fixed instant: the episodes depend only on the firings' order.
	base := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	n := 0
	const triggersN, perTrigger = 12, 36
	for ti := range triggersN {
		trg := fmt.Sprintf("eq-%d", ti)
		f.trigger(t, trg)
		for range perTrigger {
			n++
			id := fmt.Sprintf("%s-%d", trg, n)
			at := base.Add(time.Duration(n) * time.Minute)
			switch r := rng.Intn(20); {
			case r < 9: // the common case: a firing that launched nothing
				f.firing(t, id, trg, core.TriggerEventSkipped, "", at)
			case r < 11:
				f.firing(t, id, trg, core.TriggerEventFailed, "could not launch", at)
			case r < 13:
				f.launched(t, id, id, trg, at, db.Failed())
			case r < 14:
				f.launched(t, id, id, trg, at, db.Completed())
			case r < 16:
				f.launched(t, id, id, trg, at, db.Active())
			case r < 18:
				f.launched(t, id, id, trg, at, db.Completed())
				applyInboxAttention(t, f, id, "actor-"+id, "pr:"+id, "finish", "resolved", at, 0)
			default:
				f.launched(t, id, id, trg, at, db.Failed())
				applyInboxAttention(t, f, id, "actor-"+id, "pr:"+id, "finish", "resolved", at, 0)
			}
		}
	}
	// One trigger whose episode is mostly skipped firings, past the window.
	f.trigger(t, "eq-long")
	f.launched(t, "eq-long-fail-1", "eq-long-fail-1", "eq-long", base, db.Failed())
	f.launched(t, "eq-long-fail-2", "eq-long-fail-2", "eq-long", base.Add(time.Second), db.Failed())
	for i := range 3 * triggers.HealthWindow {
		f.firing(t, fmt.Sprintf("eq-long-skip-%d", i), "eq-long", core.TriggerEventSkipped, "", base.Add(time.Duration(i+2)*time.Second))
	}

	// Launch failures (no run failed) are listed only when two of them sit
	// in the health window, so skipped firings decide it at the window's
	// edge. Each sequence is oldest first.
	const (
		skip   = "skip"
		fail   = "fail"
		active = "active"
	)
	edges := map[string][]string{
		"eq-edge-hidden":    append(append([]string{fail}, repeat(skip, triggers.HealthWindow-1)...), fail), // 2nd failure is 11th
		"eq-edge-shown":     append(append([]string{fail}, repeat(skip, triggers.HealthWindow-2)...), fail), // 2nd failure is 10th
		"eq-edge-pushed":    append([]string{fail, fail}, repeat(skip, triggers.HealthWindow-1)...),
		"eq-edge-kept":      append([]string{fail, fail}, repeat(skip, triggers.HealthWindow-2)...),
		"eq-edge-runs-mid":  append(append(append(append([]string{fail}, repeat(skip, 4)...), repeat(active, 3)...), repeat(skip, 1)...), fail),
		"eq-edge-runs-past": append(append(append(append([]string{fail}, repeat(skip, 5)...), repeat(active, 3)...), repeat(skip, 3)...), fail),
	}
	for trg, seq := range edges {
		f.trigger(t, trg)
		for i, kind := range seq {
			id := fmt.Sprintf("%s-%d", trg, i)
			at := base.Add(time.Duration(i) * time.Second)
			switch kind {
			case skip:
				f.firing(t, id, trg, core.TriggerEventSkipped, "", at)
			case fail:
				f.firing(t, id, trg, core.TriggerEventFailed, "could not launch", at)
			case active:
				f.launched(t, id, id, trg, at, db.Active())
			}
		}
	}

	// Row level: each trimmed episode is the full one, filtered, with the
	// same boundary. Every non-skipped firing of the full episode (managed
	// successes the filter passed over included) is returned, in order, and
	// nothing from before its boundary is.
	ext := store.NewTriggerEventExtStore(f.repo.ORM())
	var ids []string
	for i := range triggersN {
		ids = append(ids, fmt.Sprintf("eq-%d", i))
	}
	ids = append(ids, "eq-long")
	for trg := range edges {
		ids = append(ids, trg)
	}
	fullEp, err := ext.FiringsSinceLastUnmanagedSuccess(f.ctx, f.userID, ids, triggers.EpisodeFirings)
	require.NoError(t, err)
	trimmedEp, err := ext.InboxEpisodes(f.ctx, f.userID, ids, triggers.EpisodeFirings, triggers.HealthWindow)
	require.NoError(t, err)
	fullDiag, err := ext.FiringsSinceLastSuccess(f.ctx, f.userID, ids, triggers.EpisodeFirings)
	require.NoError(t, err)
	trimmedDiag, err := ext.InboxEpisodeFailures(f.ctx, f.userID, ids, triggers.EpisodeFirings)
	require.NoError(t, err)
	for _, id := range ids {
		assert.Equal(t, keptIDs(fullEp[id], triggers.HealthWindow), eventIDs(trimmedEp[id]), "%s: attention-filtered episode", id)
		assert.Equal(t, keptIDs(fullDiag[id], 0), eventIDs(trimmedDiag[id]), "%s: diagnostic episode", id)
	}

	want := listWith(t, f, f.inboxWithFirings(t, legacyEpisodes{store.NewTriggerEventExtStore(f.repo.ORM())}))
	got := listWith(t, f, f.inboxWithFirings(t, store.NewTriggerEventExtStore(f.repo.ORM())))
	require.NotEmpty(t, inboxByKind(want.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING),
		"the mixes must produce failing automations, or the comparison proves nothing")
	listed := map[string]bool{}
	for _, it := range want.Items {
		if it.Kind == reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING {
			listed[it.TriggerId] = true
		}
	}
	for trg, shown := range map[string]bool{"eq-edge-hidden": false, "eq-edge-shown": true, "eq-edge-pushed": false, "eq-edge-kept": true, "eq-edge-runs-mid": true, "eq-edge-runs-past": false} {
		require.Equal(t, shown, listed[trg], "%s: the edge cases must sit on both sides of the window", trg)
	}
	if !proto.Equal(want, got) {
		t.Fatalf("trimmed episode reads list something else:\nwant %v\ngot  %v", want, got)
	}
}

// keptIDs is what inboxEpisodeSQL keeps of a full episode: every firing but
// the skipped ones without a chat past the first window skipped firings.
func keptIDs(firings []*core.TriggerEventWithRun, window int) []string {
	var out []string
	skipped := 0
	for _, f := range firings {
		if f.Event.Outcome == core.TriggerEventSkipped && f.Event.ChatID == nil {
			if skipped++; skipped > window {
				continue
			}
		}
		out = append(out, f.Event.ID)
	}
	return out
}

func eventIDs(firings []*core.TriggerEventWithRun) []string {
	var out []string
	for _, f := range firings {
		out = append(out, f.Event.ID)
	}
	return out
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// An episode of hundreds of skipped firings — an event trigger whose filter
// declined everything since its last success — is no longer shipped to the
// Inbox firing by firing: its episode reads return the failures and at most a
// health window of skipped firings, however long the episode is.
func TestListInbox_EpisodeReadsDoNotReturnEverySkippedFiring(t *testing.T) {
	f := newInboxFixture(t)
	f.trigger(t, "noisy")
	base := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	f.launched(t, "noisy-fail-1", "noisy-fail-1", "noisy", base, db.Failed())
	f.launched(t, "noisy-fail-2", "noisy-fail-2", "noisy", base.Add(time.Second), db.Failed())
	const skipped = 300
	for i := range skipped {
		f.firing(t, fmt.Sprintf("noisy-skip-%d", i), "noisy", core.TriggerEventSkipped, "", base.Add(time.Duration(i+2)*time.Second))
	}

	full, err := store.NewTriggerEventExtStore(f.repo.ORM()).FiringsSinceLastUnmanagedSuccess(f.ctx, f.userID, []string{"noisy"}, triggers.EpisodeFirings)
	require.NoError(t, err)
	require.Len(t, full["noisy"], skipped+2, "the episode itself is every firing since the last success")

	counting := &countingEpisodes{TriggerEventExtStore: store.NewTriggerEventExtStore(f.repo.ORM()), rows: map[string]int{}}
	items := inboxByKind(listWith(t, f, f.inboxWithFirings(t, counting)).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, items, 1)
	assert.Equal(t, "automation_failing:noisy-fail-1", items[0].ItemId)
	assert.EqualValues(t, 2, items[0].GetAutomationFailing().GetHealth().GetConsecutiveFailures())
	// Both episode reads: the two failures each, plus one window of skipped
	// firings for the attention-filtered episode.
	assert.LessOrEqual(t, counting.rows["noisy"], 2*2+triggers.HealthWindow,
		"the Inbox's episode reads must not return the episode's %d skipped firings", skipped)
}
