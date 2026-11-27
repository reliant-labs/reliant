// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// inflightRepo is a MockRepository whose every read holds for `hold` and
// counts how many reads are in flight at once.
type inflightRepo struct {
	*MockRepository
	*MockProjectReader
	chats     *MockChats
	rec       *MockRecoveries
	items     *MockItems
	firings   *MockFirings
	notices   *MockNotices
	regs      *MockRegistrations
	storage   *MockStorage
	now, peak atomic.Int32
}

// deps is the service's Deps over r: its repository and its recovery list.
func (r *inflightRepo) deps() Deps {
	return Deps{Repo: r, Chats: r.chats, ProjectRows: r.MockProjectReader, Items: r.items, Firings: r.firings, Notices: r.notices, Registrations: r.regs, Storage: r.storage, Recoveries: r.rec, Marks: &MockMarkDecisionStore{}}
}

func (r *inflightRepo) enter(ctx context.Context, hold time.Duration) {
	n := r.now.Add(1)
	for {
		p := r.peak.Load()
		if n <= p || r.peak.CompareAndSwap(p, n) {
			break
		}
	}
	select {
	case <-time.After(hold):
	case <-ctx.Done():
	}
	r.now.Add(-1)
}

func newInflightRepo(hold time.Duration) *inflightRepo {
	r := &inflightRepo{MockRepository: &MockRepository{}, MockProjectReader: &MockProjectReader{}, chats: &MockChats{}, rec: &MockRecoveries{}, items: &MockItems{}, firings: &MockFirings{}, notices: &MockNotices{}, regs: &MockRegistrations{}, storage: &MockStorage{}}
	m := r.MockRepository
	// The org account notices: nothing paused, nothing on personal accounts.
	r.notices.HasNoOrgAccountPausesFunc = func(context.Context) (bool, error) { return false, nil }
	r.notices.ListPersonallyFundedTriggersFunc = func(context.Context, string) ([]*core.Trigger, error) { return nil, nil }
	trg := &core.Trigger{ID: "trg-1", UserID: "user-1", Enabled: true, ProjectID: "p1", Name: "nightly"}
	r.GetProjectFunc = func(_ context.Context, id string) (*db.Project, error) {
		return &db.Project{ID: id, UserID: "user-1"}, nil
	}
	r.items.ListInboxPendingFunc = func(ctx context.Context, _ string) ([]*core.InboxPending, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	r.items.ListAttentionIncidentsFunc = func(ctx context.Context, _ string) ([]*core.AttentionIncident, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	r.items.AttentionManagedChatsFunc = func(ctx context.Context, _ string, _ []string) (map[string]bool, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	r.rec.ListWorktreeRecoveriesFunc = func(ctx context.Context, _, _ string) ([]*core.WorktreeRecovery, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	r.notices.ListTriggersForUserFunc = func(ctx context.Context, _ core.TriggerFilters) ([]*core.Trigger, error) {
		r.enter(ctx, hold)
		return []*core.Trigger{trg}, nil
	}
	firings := func(ctx context.Context, _ string, _ []string, _ int) (map[string][]*core.TriggerEventWithRun, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	r.firings.RecentTriggerOutcomesFunc = firings
	r.firings.InboxEpisodeFailuresFunc = firings
	r.firings.InboxEpisodesFunc = func(ctx context.Context, userID string, ids []string, per, _ int) (map[string][]*core.TriggerEventWithRun, error) {
		return firings(ctx, userID, ids, per)
	}
	r.regs.ListTriggerRegistrationsForUserFunc = func(ctx context.Context, _ string, _ []string) (map[string]*core.TriggerRegistration, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	r.storage.ListHeldWorktreesForUserFunc = func(ctx context.Context, _ string) ([]*core.HeldWorktree, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	r.storage.ListDaemonStorageStateFunc = func(ctx context.Context, _ string) (map[string]string, error) { return nil, nil }
	r.storage.ListAttachedDaemonIDsForUserFunc = func(ctx context.Context, _ string, _ time.Duration) ([]string, error) {
		return nil, nil
	}
	m.ListDaemonsByUserIDFunc = func(ctx context.Context, _ string) ([]*db.Daemon, error) { return nil, nil }
	r.items.ListDismissedInboxItemIDsFunc = func(ctx context.Context, _ string, _ []string) (map[string]bool, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	return r
}

func userCtx() context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
}

// ListInbox's reads run together, so a request costs its longest chain of
// round trips, not their sum; and never more than inboxReadConcurrency plus
// inboxHealthReadConcurrency at once, so a burst of requests cannot each take
// a handful of the pool.
func TestListInbox_ReadsRunConcurrentlyWithinTheBound(t *testing.T) {
	const hold = 40 * time.Millisecond
	repo := newInflightRepo(hold)
	svc := NewTestInbox(t, WithInboxDeps(repo.deps()))

	_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)

	// The peak in flight is what proves the reads overlap. A wall-clock bound
	// on the call was removed as flaky under load (flake:removed).
	assert.GreaterOrEqual(t, repo.peak.Load(), int32(inboxReadConcurrency), "reads overlap")
	assert.LessOrEqual(t, repo.peak.Load(), int32(inboxReadConcurrency+inboxHealthReadConcurrency), "up to the bound and no further")
	// The triggers are read once for both automation sections.
	assert.Len(t, repo.notices.Calls("ListTriggersForUser"), 1)
}

// The two firing reads run together, on a budget of their own, even while the
// slow reads hold the rest: in prod (trace ed2301a0…, 2026-11-18) they waited
// for whichever token the pending read and the declaration loop left free and
// ran one after the other, a second each. Here the pending read and the
// recoveries hold their tokens until both firing reads are in flight at once,
// which on one shared budget never happens.
func TestListInbox_FiringReadsDoNotQueueBehindTheOtherReads(t *testing.T) {
	repo := newInflightRepo(0)
	bothIn := make(chan struct{})
	var inFlight atomic.Int32
	waitForBoth := func(ctx context.Context) {
		select {
		case <-bothIn:
		case <-ctx.Done():
		case <-time.After(5 * time.Second): // only when the reads queue: the test then fails
		}
	}
	firing := func(ctx context.Context, _ string, _ []string, _ int) (map[string][]*core.TriggerEventWithRun, error) {
		if inFlight.Add(1) == 2 {
			close(bothIn)
		}
		defer inFlight.Add(-1)
		waitForBoth(ctx)
		return nil, nil
	}
	repo.firings.RecentTriggerOutcomesFunc = firing
	repo.firings.InboxEpisodeFailuresFunc = firing
	repo.items.ListInboxPendingFunc = func(ctx context.Context, _ string) ([]*core.InboxPending, error) {
		waitForBoth(ctx)
		return nil, nil
	}
	repo.rec.ListWorktreeRecoveriesFunc = func(ctx context.Context, _, _ string) ([]*core.WorktreeRecovery, error) {
		waitForBoth(ctx)
		return nil, nil
	}
	svc := NewTestInbox(t, WithInboxDeps(repo.deps()))

	_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	select {
	case <-bothIn:
	default:
		t.Fatal("the two firing reads never ran at the same time")
	}
}

// gateFirstPendingRead makes the first ListInboxPending hold until the returned
// release is called; inFlight closes once it holds. Later reads return at once.
func gateFirstPendingRead(t *testing.T, repo *inflightRepo) (inFlight <-chan struct{}, release func()) {
	t.Helper()
	held, gate := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	repo.items.ListInboxPendingFunc = func(context.Context, string) ([]*core.InboxPending, error) {
		if reads.Add(1) == 1 {
			close(held)
			<-gate
		}
		return nil, nil
	}
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return held, release
}

// waitQueued waits until n ListInbox calls of userCtx's user wait on the read
// queued behind the one in flight.
func waitQueued(t *testing.T, svc *Service, n int) {
	t.Helper()
	key := sharedReadKey(userCtx(), "user-1")
	require.Eventually(t, func() bool { return svc.inboxReads.queued(key) == n }, 10*time.Second, time.Millisecond,
		"%d calls queued behind the read in flight", n)
}

// The refetch after answering a question must not get the list from before
// the answer. A ListInbox arriving while an earlier read is in flight used to
// join that read, which started before the answer committed and still listed
// the question: the row disappeared, came back, and went again on the next
// refetch. A call now joins only a read that starts after it arrived.
func TestListInbox_ACallAfterAWriteDoesNotJoinAnEarlierRead(t *testing.T) {
	repo := newInflightRepo(0)
	var answered atomic.Bool
	var reads atomic.Int32
	held, gate := make(chan struct{}), make(chan struct{})
	repo.items.ListInboxPendingFunc = func(context.Context, string) ([]*core.InboxPending, error) {
		pending := !answered.Load() // what the read's snapshot sees
		if reads.Add(1) == 1 {
			close(held)
			<-gate
		}
		if !pending {
			return nil, nil
		}
		return []*core.InboxPending{{Kind: core.InboxPendingQuestion, ItemKey: "q1", ProjectID: "p1", WaitingSince: time.Now()}}, nil
	}
	svc := NewTestInbox(t, WithInboxDeps(repo.deps()))

	first := make(chan []string, 1)
	go func() {
		first <- listItemIDs(t, svc)
	}()
	<-held
	answered.Store(true) // ResolveQuestion commits while the first read is in flight

	second := make(chan []string, 1)
	go func() {
		second <- listItemIDs(t, svc)
	}()
	waitQueued(t, svc, 1)
	close(gate)

	assert.Contains(t, <-first, "question:q1", "the first call's read started before the answer")
	assert.NotContains(t, <-second, "question:q1", "the call after the answer reads after it")
	assert.Len(t, repo.items.Calls("ListInboxPending"), 2)
}

// Concurrent ListInbox calls of one user share reads: a burst arriving during
// one read costs one more set of queries, not one per call, whatever scope and
// limit each call asked for.
func TestListInbox_ConcurrentCallsShareOneRead(t *testing.T) {
	repo := newInflightRepo(0)
	held, release := gateFirstPendingRead(t, repo)
	svc := NewTestInbox(t, WithInboxDeps(repo.deps()))

	const callers = 8
	errs := make(chan error, callers+1)
	go func() {
		_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
		errs <- err
	}()
	<-held
	for i := range callers {
		go func() {
			req := &reliantv1.ListInboxRequest{}
			if i%2 == 0 {
				limit := int32(0)
				req.Limit = &limit // the badge's shape
			}
			_, err := svc.ListInbox(userCtx(), connect.NewRequest(req))
			errs <- err
		}()
	}
	waitQueued(t, svc, callers)
	release()
	for range callers + 1 {
		require.NoError(t, <-errs)
	}
	assert.Len(t, repo.items.Calls("ListInboxPending"), 2, "%d calls during one read share one more read", callers)
}

// One caller giving up does not fail the others sharing its read, even the
// caller whose request the read was started from.
func TestListInbox_ACancelledCallerDoesNotFailASharedRead(t *testing.T) {
	repo := newInflightRepo(0)
	held, release := gateFirstPendingRead(t, repo)
	svc := NewTestInbox(t, WithInboxDeps(repo.deps()))

	inFlight := make(chan error, 1)
	go func() {
		_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
		inFlight <- err
	}()
	<-held

	// The queued read is started from the first caller to queue: this one.
	starter, cancel := context.WithCancel(userCtx())
	gaveUp := make(chan error, 1)
	go func() {
		_, err := svc.ListInbox(starter, connect.NewRequest(&reliantv1.ListInboxRequest{}))
		gaveUp <- err
	}()
	waitQueued(t, svc, 1)
	sharer := make(chan error, 1)
	go func() {
		_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
		sharer <- err
	}()
	waitQueued(t, svc, 2)

	cancel()
	require.Error(t, <-gaveUp, "the caller that gave up returns at once, before any read finishes")
	waitQueued(t, svc, 1)
	release()
	require.NoError(t, <-inFlight)
	require.NoError(t, <-sharer, "the read the cancelled caller started still answers the others")
	assert.Len(t, repo.items.Calls("ListInboxPending"), 2)
}

// listItemIDs is the IDs of the items a ListInbox of userCtx's user lists.
func listItemIDs(t *testing.T, svc *Service) []string {
	resp, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	if !assert.NoError(t, err) {
		return nil
	}
	ids := make([]string, 0, len(resp.Msg.Items))
	for _, it := range resp.Msg.Items {
		ids = append(ids, it.ItemId)
	}
	return ids
}

func TestListInbox_ARequiredReadFailingFailsTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		breakIt func(r *inflightRepo)
	}{
		{"pending", func(r *inflightRepo) {
			r.items.ListInboxPendingFunc = func(context.Context, string) ([]*core.InboxPending, error) { return nil, errors.New("boom") }
		}},
		{"automation health", func(r *inflightRepo) {
			r.firings.InboxEpisodesFunc = func(context.Context, string, []string, int, int) (map[string][]*core.TriggerEventWithRun, error) {
				return nil, errors.New("boom")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newInflightRepo(0)
			tc.breakIt(repo)
			svc := NewTestInbox(t, WithInboxDeps(repo.deps()))
			_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
			require.Error(t, err)
		})
	}
}

func TestListInbox_AnAdvisoryReadFailingStillAnswers(t *testing.T) {
	repo := newInflightRepo(0)
	repo.storage.ListHeldWorktreesForUserFunc = func(context.Context, string) ([]*core.HeldWorktree, error) {
		return nil, errors.New("storage down")
	}
	repo.items.ListInboxPendingFunc = func(context.Context, string) ([]*core.InboxPending, error) {
		return []*core.InboxPending{{Kind: core.InboxPendingApproval, ItemKey: "a1", ProjectID: "p1", WaitingSince: time.Now()}}, nil
	}
	svc := NewTestInbox(t, WithInboxDeps(repo.deps()))
	resp, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Items, 1)
	assert.EqualValues(t, 1, resp.Msg.BlockingCount)
}

// A user shaped like prod's heaviest (2026-10-23): six enabled automations,
// one of them 250 firings into a failure episode, every firing carrying a
// ~7 KB webhook body. ListInbox reads the episode to count it and never shows
// a body, so it reads none (RecentTriggerOutcomes, FiringsSinceLastUnmanagedSuccess):
// in prod the bodies were 1.8 MB of JSON per call, decoded into maps, and the
// RPC took 3-4 s. The timing is logged, not asserted (shared CI machines).
func TestListInbox_HeavyAutomationUser(t *testing.T) {
	testkit.Slow(t, "DB-backed: seeds many inbox rows in pgtest's Postgres")
	f := newInboxFixture(t)
	payload := map[string]any{"body": strings.Repeat("x", 7000), "integration": "github"}
	base := time.Now().UTC().Add(-48 * time.Hour)
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("trg-heavy-%d", i)
		f.trigger(t, id)
		n := 10
		if i == 0 {
			n = 250
		}
		for j := 0; j < n; j++ {
			tid := id
			_, err := f.repo.CreateTriggerEvent(f.ctx, &core.TriggerEvent{
				ID: fmt.Sprintf("%s-ev-%03d", id, j), TriggerID: &tid, UserID: f.userID, Kind: core.TriggerEventKindSchedule,
				DedupeKey: fmt.Sprintf("%s-dd-%d", id, j), OccurredAt: base.Add(time.Duration(j) * time.Minute).Truncate(time.Microsecond),
				Payload: payload, Outcome: core.TriggerEventFailed, OutcomeDetail: "no machine",
				CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
			})
			require.NoError(t, err)
		}
	}

	var resp *reliantv1.ListInboxResponse
	const runs = 10
	durations := make([]time.Duration, runs)
	for i := range durations {
		start := time.Now()
		resp = f.list(t, nil)
		durations[i] = time.Since(start)
	}
	slices.Sort(durations)
	t.Logf("ListInbox, heavy automation user: p50 %v, max %v over %d calls", durations[runs/2], durations[runs-1], runs)

	// A burst like 05:22's: 40 calls at once (a reconnect replay times every
	// open list and badge). Logged, not asserted.
	const burst = 40
	f.inbox.storageCache.Delete(f.userID)
	start := time.Now()
	errs := make(chan error, burst)
	for range burst {
		go func() {
			_, err := f.inbox.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{}))
			errs <- err
		}()
	}
	for range burst {
		require.NoError(t, <-errs)
	}
	t.Logf("ListInbox, heavy automation user: a burst of %d concurrent calls took %v", burst, time.Since(start))

	failing := inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING)
	require.Len(t, failing, 6, "every automation is failing")
	counts := map[string]int32{}
	for _, it := range failing {
		counts[it.TriggerId] = it.GetAutomationFailing().GetHealth().GetConsecutiveFailures()
	}
	assert.EqualValues(t, 250, counts["trg-heavy-0"], "the episode is counted in full, past the health window")
	assert.EqualValues(t, 10, counts["trg-heavy-1"])
	assert.Equal(t, "automation_failing:trg-heavy-0-ev-000", inboxByTrigger(failing, "trg-heavy-0").ItemId,
		"the item is named by the episode's first failure")
}

func inboxByTrigger(items []*reliantv1.InboxItem, triggerID string) *reliantv1.InboxItem {
	for _, it := range items {
		if it.TriggerId == triggerID {
			return it
		}
	}
	return nil
}
