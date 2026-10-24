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
	now, peak atomic.Int32
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
	r := &inflightRepo{MockRepository: &MockRepository{}}
	m := r.MockRepository
	trg := &core.Trigger{ID: "trg-1", Enabled: true, ProjectID: "p1", Name: "nightly"}
	m.ListInboxPendingFunc = func(ctx context.Context, _ string) ([]*core.InboxPending, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	m.ListTriggersFunc = func(ctx context.Context, _ core.TriggerFilters) ([]*core.Trigger, error) {
		r.enter(ctx, hold)
		return []*core.Trigger{trg}, nil
	}
	firings := func(ctx context.Context, _ string, _ []string, _ int) (map[string][]*core.TriggerEventWithRun, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	m.RecentTriggerOutcomesFunc = firings
	m.FiringsSinceLastSuccessFunc = firings
	m.ListTriggerRegistrationsFunc = func(ctx context.Context, _ string, _ []string) (map[string]*core.TriggerRegistration, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	m.ListHeldWorktreesForUserFunc = func(ctx context.Context, _ string) ([]*core.HeldWorktree, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	m.ListDaemonStorageStateFunc = func(ctx context.Context, _ string) (map[string]string, error) { return nil, nil }
	m.ListAttachedDaemonIDsForUserFunc = func(ctx context.Context, _ string, _ time.Duration) ([]string, error) {
		return nil, nil
	}
	m.ListDaemonsByUserIDFunc = func(ctx context.Context, _ string) ([]*db.Daemon, error) { return nil, nil }
	m.ListDismissedInboxItemIDsFunc = func(ctx context.Context, _ string, _ []string) (map[string]bool, error) {
		r.enter(ctx, hold)
		return nil, nil
	}
	return r
}

func userCtx() context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
}

// ListInbox's reads run together, so a request costs its longest chain of
// round trips, not their sum; and never more than inboxReadConcurrency at
// once, so a burst of requests cannot each take a handful of the pool.
func TestListInbox_ReadsRunConcurrentlyWithinTheBound(t *testing.T) {
	const hold = 40 * time.Millisecond
	repo := newInflightRepo(hold)
	svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo}))

	start := time.Now()
	_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	elapsed := time.Since(start)

	assert.EqualValues(t, inboxReadConcurrency, repo.peak.Load(), "reads overlap, up to the bound and no further")
	// Seven held reads one after another take 7×hold; the longest chain
	// (triggers, then firings, then the dismissal check) is 3×hold.
	assert.Less(t, elapsed, 6*hold, "the reads ran one after another")
	// The triggers are read once for both automation sections.
	assert.Len(t, repo.Calls("ListTriggers"), 1)
}

// Concurrent ListInbox calls of one user share one read: a burst costs one
// set of queries, whatever scope and limit each call asked for.
func TestListInbox_ConcurrentCallsShareOneRead(t *testing.T) {
	repo := newInflightRepo(100 * time.Millisecond)
	svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo}))
	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := &reliantv1.ListInboxRequest{}
			if i%2 == 0 {
				limit := int32(0)
				req.Limit = &limit // the badge's shape
			}
			_, err := svc.ListInbox(userCtx(), connect.NewRequest(req))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Len(t, repo.Calls("ListInboxPending"), 1, "%d concurrent calls read the inbox once", callers)
}

// One caller giving up does not fail the others sharing its read.
func TestListInbox_ACancelledCallerDoesNotFailASharedRead(t *testing.T) {
	repo := newInflightRepo(100 * time.Millisecond)
	svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo}))
	first, cancel := context.WithCancel(userCtx())
	done := make(chan error, 1)
	go func() {
		_, err := svc.ListInbox(first, connect.NewRequest(&reliantv1.ListInboxRequest{}))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond) // the first call's read is in flight
	cancel()
	_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	<-done
}

func TestListInbox_ARequiredReadFailingFailsTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		breakIt func(m *MockRepository)
	}{
		{"pending", func(m *MockRepository) {
			m.ListInboxPendingFunc = func(context.Context, string) ([]*core.InboxPending, error) { return nil, errors.New("boom") }
		}},
		{"automation health", func(m *MockRepository) {
			m.FiringsSinceLastSuccessFunc = func(context.Context, string, []string, int) (map[string][]*core.TriggerEventWithRun, error) {
				return nil, errors.New("boom")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newInflightRepo(0)
			tc.breakIt(repo.MockRepository)
			svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo}))
			_, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
			require.Error(t, err)
		})
	}
}

func TestListInbox_AnAdvisoryReadFailingStillAnswers(t *testing.T) {
	repo := newInflightRepo(0)
	repo.ListHeldWorktreesForUserFunc = func(context.Context, string) ([]*core.HeldWorktree, error) {
		return nil, errors.New("storage down")
	}
	repo.ListInboxPendingFunc = func(context.Context, string) ([]*core.InboxPending, error) {
		return []*core.InboxPending{{Kind: core.InboxPendingApproval, ItemKey: "a1", ProjectID: "p1", WaitingSince: time.Now()}}, nil
	}
	svc := NewTestInbox(t, WithInboxDeps(Deps{Repo: repo}))
	resp, err := svc.ListInbox(userCtx(), connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Items, 1)
	assert.EqualValues(t, 1, resp.Msg.BlockingCount)
}

// A user shaped like prod's heaviest (2026-10-23): six enabled automations,
// one of them 250 firings into a failure episode, every firing carrying a
// ~7 KB webhook body. ListInbox reads the episode to count it and never shows
// a body, so it reads none (RecentTriggerOutcomes, FiringsSinceLastSuccess):
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
