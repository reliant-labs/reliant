// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/machinepool"
)

// fakePoolStore is a three-member pool the tests mutate: each launch onto a
// member raises its live runs, which is what the store would count.
type fakePoolStore struct {
	mu      sync.Mutex
	pool    *core.MachinePool
	members []core.MachinePoolMemberRow
	// stealSlotOnce makes the in-transaction count one higher than the
	// read, once: a concurrent launch took the slot between choice and tx.
	stealSlotOnce string
}

var catalogJSON = `[{"slug":"pr-review","yaml_content":"x","content_hash":"h1"}]`

func newFakePool(maxPer int, names ...string) *fakePoolStore {
	s := &fakePoolStore{pool: &core.MachinePool{ID: "pool-cicd", UserID: "user-1", Name: "cicd", Selector: map[string]string{"pool": "cicd"}, MaxRunsPerMember: maxPer}}
	pushed := time.Date(2026, 10, 30, 16, 0, 0, 0, time.UTC)
	for _, n := range names {
		wf := catalogJSON
		s.members = append(s.members, core.MachinePoolMemberRow{DaemonID: "d-" + n, Name: n, Phase: "connected", Installed: true, WorkflowsJSON: &wf, CatalogPushedAt: &pushed})
	}
	return s
}

func (s *fakePoolStore) GetMachinePoolForUser(_ context.Context, userID, id string) (*core.MachinePool, error) {
	if id != s.pool.ID || userID != s.pool.UserID {
		return nil, core.ErrMachinePoolNotFound
	}
	return s.pool, nil
}

func (s *fakePoolStore) ListMachinePoolMembers(context.Context, *core.MachinePool, string) ([]core.MachinePoolMemberRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]core.MachinePoolMemberRow(nil), s.members...), nil
}

func (s *fakePoolStore) LockMachinePool(context.Context, string) error { return nil }

func (s *fakePoolStore) CountMachinePoolMemberLiveRuns(_ context.Context, _, daemonID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.members {
		if m.DaemonID == daemonID {
			if s.stealSlotOnce == daemonID {
				s.stealSlotOnce = ""
				return m.LiveRuns + 1, nil
			}
			return m.LiveRuns, nil
		}
	}
	return 0, nil
}

func (s *fakePoolStore) set(name string, f func(*core.MachinePoolMemberRow)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.members {
		if s.members[i].Name == name {
			f(&s.members[i])
		}
	}
}

// poolLauncher launches through fakeLauncher's guard handling, then records
// the run on the member it was placed on.
type poolLauncher struct {
	fakeLauncher
	store *fakePoolStore
}

func (l *poolLauncher) Launch(ctx context.Context, ev launch.Event, spec launch.Spec) (*launch.Result, error) {
	res, err := l.fakeLauncher.Launch(ctx, ev, spec)
	if err == nil && spec.DaemonID != "" {
		l.store.set(strings.TrimPrefix(spec.DaemonID, "d-"), func(m *core.MachinePoolMemberRow) { m.LiveRuns++ })
	}
	return res, err
}

func poolTrigger() *core.Trigger {
	return &core.Trigger{ID: "t-1", UserID: "user-1", ProjectID: "p-1", Name: "pr-review", MachinePoolID: "pool-cicd"}
}

func TestLaunchOnPool_SpreadsAndPinsEachRunToItsMember(t *testing.T) {
	store := newFakePool(2, "a", "b", "c")
	l := &poolLauncher{store: store}
	placer := machinepool.NewPlacer(store)
	got := map[string]int{}
	for i := 0; i < 6; i++ {
		if _, err := launchOnPool(context.Background(), placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false); err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
	}
	for _, c := range l.snapshot() {
		got[c.Spec.DaemonID]++
		if c.Spec.MachinePool == nil || c.Spec.MachinePool.ID != "pool-cicd" || len(c.Spec.MachinePool.Members) != 3 {
			t.Fatalf("spec carries no pool placement: %+v", c.Spec.MachinePool)
		}
	}
	if got["d-a"] != 2 || got["d-b"] != 2 || got["d-c"] != 2 {
		t.Fatalf("placements = %v, want 2 per member", got)
	}
	// Seventh: every member full, so the fire declines with the pool's wait.
	_, err := launchOnPool(context.Background(), placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false)
	var declined *launch.DeclinedError
	if !errors.As(err, &declined) || !isPoolWaiting(declined.Reason) {
		t.Fatalf("got %v, want a pool wait", err)
	}
}

// The owner's acceptance case at the fire level: member b dies mid-run. Its
// run keeps its pin (nothing re-places it), new fires never land on it, and
// it is used again once it is back on the pool's catalog.
func TestLaunchOnPool_MemberDiesMidRun(t *testing.T) {
	store := newFakePool(1, "a", "b", "c")
	l := &poolLauncher{store: store}
	placer := machinepool.NewPlacer(store)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := launchOnPool(ctx, placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false); err != nil {
			t.Fatal(err)
		}
	}
	store.set("b", func(m *core.MachinePoolMemberRow) { m.Phase = "disconnected" })
	if _, err := launchOnPool(ctx, placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false); err != nil {
		t.Fatal(err)
	}
	calls := l.snapshot()
	if last := calls[len(calls)-1].Spec.DaemonID; last != "d-c" {
		t.Fatalf("third run went to %s, want c (a full, b down)", last)
	}
	// A retried fire of b's already-launched run resumes it: never re-placed.
	if _, err := launchOnPool(ctx, placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, true); err != nil {
		t.Fatal(err)
	}
	calls = l.snapshot()
	if resumed := calls[len(calls)-1].Spec; resumed.DaemonID != "" || resumed.MachinePool == nil {
		t.Fatalf("a resumed launch was re-placed: daemon %q", resumed.DaemonID)
	}
	// Everything full or down: the next fire waits, naming b.
	_, err := launchOnPool(ctx, placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false)
	var declined *launch.DeclinedError
	if !errors.As(err, &declined) || !strings.Contains(declined.Reason, "b: disconnected") {
		t.Fatalf("got %v, want a wait naming b", err)
	}
	// b's run ends and b reconnects current: it takes the next run.
	store.set("b", func(m *core.MachinePoolMemberRow) { m.Phase, m.LiveRuns = "connected", 0 })
	if _, err := launchOnPool(ctx, placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false); err != nil {
		t.Fatal(err)
	}
	calls = l.snapshot()
	if last := calls[len(calls)-1].Spec.DaemonID; last != "d-b" {
		t.Fatalf("after recovery the run went to %s, want b", last)
	}
}

func TestLaunchOnPool_LostSlotRacePlacesAgain(t *testing.T) {
	store := newFakePool(1, "a", "b")
	store.stealSlotOnce = "d-a"
	l := &poolLauncher{store: store}
	if _, err := launchOnPool(context.Background(), machinepool.NewPlacer(store), l, poolTrigger(), launch.Event{}, launch.Spec{}, false); err != nil {
		t.Fatal(err)
	}
	calls := l.snapshot()
	if len(calls) != 2 || calls[0].Spec.DaemonID != "d-a" || calls[1].Spec.DaemonID != "d-a" {
		// The steal is virtual (the read still shows a free), so the
		// re-place picks a again and the guard now admits it.
		t.Fatalf("calls = %d, want a declined attempt then a retry", len(calls))
	}
}

func TestLaunchOnPool_CatalogDriftIsNeverPlacedOn(t *testing.T) {
	store := newFakePool(0, "a", "b", "c")
	stale := `[{"slug":"pr-review","yaml_content":"old","content_hash":"h0"}]`
	store.set("a", func(m *core.MachinePoolMemberRow) { m.WorkflowsJSON = &stale })
	l := &poolLauncher{store: store}
	for i := 0; i < 4; i++ {
		if _, err := launchOnPool(context.Background(), machinepool.NewPlacer(store), l, poolTrigger(), launch.Event{}, launch.Spec{}, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range l.snapshot() {
		if c.Spec.DaemonID == "d-a" {
			t.Fatal("placed on a member whose catalog differs from the pool's")
		}
	}
}

func TestLaunchOnPool_DeletedPoolIsAVerdict(t *testing.T) {
	store := newFakePool(0, "a")
	tr := poolTrigger()
	tr.MachinePoolID = "gone"
	_, err := launchOnPool(context.Background(), machinepool.NewPlacer(store), &poolLauncher{store: store}, tr, launch.Event{}, launch.Spec{}, false)
	var v *launch.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("got %v, want a validation verdict", err)
	}
}

func TestCapacityLimit(t *testing.T) {
	store := newFakePool(0, "a", "b", "c")
	placer := machinepool.NewPlacer(store)
	ctx := context.Background()
	cases := []struct {
		name    string
		trigger *core.Trigger
		policy  *reliantv1.WorkflowConcurrency
		want    uint32
		capped  bool
	}{
		{"no policy", poolTrigger(), nil, 0, false},
		{"fixed", poolTrigger(), &reliantv1.WorkflowConcurrency{MaxRunning: 6}, 6, true},
		{"per member on a pool", poolTrigger(), &reliantv1.WorkflowConcurrency{PerMember: 4}, 12, true},
		{"per member capped by max", poolTrigger(), &reliantv1.WorkflowConcurrency{PerMember: 4, MaxRunning: 10}, 10, true},
		{"per member on one machine", &core.Trigger{ID: "t", DaemonID: "d"}, &reliantv1.WorkflowConcurrency{PerMember: 4}, 4, true},
	}
	for _, tc := range cases {
		got, capped, err := capacityLimit(ctx, placer, tc.trigger, tc.policy)
		if err != nil || got != tc.want || capped != tc.capped {
			t.Errorf("%s: got %d %v %v, want %d %v", tc.name, got, capped, err, tc.want, tc.capped)
		}
	}
	// Adding a member raises the cap with no workflow edit.
	wf := catalogJSON
	store.members = append(store.members, core.MachinePoolMemberRow{DaemonID: "d-d", Name: "d", Phase: "suspended", Installed: true, WorkflowsJSON: &wf})
	if got, _, _ := capacityLimit(ctx, placer, poolTrigger(), &reliantv1.WorkflowConcurrency{PerMember: 4}); got != 16 {
		t.Fatalf("after adding a member: %d, want 16", got)
	}
	if _, _, err := capacityLimit(ctx, nil, poolTrigger(), &reliantv1.WorkflowConcurrency{PerMember: 4}); !errors.Is(err, errNoPlacer) {
		t.Fatalf("no placer: %v", err)
	}
}

// A scheduled fire of an uncapped pool trigger that cannot be placed records
// a pending event (redriven later) instead of skipping its slot; its next
// fire, with a free member, launches it.
func TestFire_PoolWaitIsPendingThenLaunches(t *testing.T) {
	repo := newFakeRepo()
	tr := testTrigger(t, nil)
	tr.DaemonID, tr.MachinePoolID, tr.UserID, tr.ProjectID = "", "pool-cicd", "user-1", "p-1"
	repo.triggers[tr.ID] = tr
	store := newFakePool(1, "a")
	store.set("a", func(m *core.MachinePoolMemberRow) { m.LiveRuns = 1 })
	l := &poolLauncher{store: store}
	f := NewFirer(repo, l).WithPlacer(machinepool.NewPlacer(store))
	req := FireRequest{TriggerID: tr.ID, FireWorkflowID: "fire-1", ScheduledAt: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}

	out, err := f.Fire(context.Background(), req)
	if err != nil || out.Outcome != string(core.TriggerEventPending) || !isPoolWaiting(out.Reason) {
		t.Fatalf("first fire = %+v, %v; want pending on the pool", out, err)
	}
	if evs := repo.eventsFor(tr.ID); len(evs) != 1 || evs[0].Outcome != core.TriggerEventPending {
		t.Fatalf("events = %+v, want one pending", evs)
	}

	store.set("a", func(m *core.MachinePoolMemberRow) { m.LiveRuns = 0 })
	out, err = f.Fire(context.Background(), req)
	if err != nil || out.Outcome != string(core.TriggerEventLaunched) {
		t.Fatalf("redriven fire = %+v, %v; want launched", out, err)
	}
	calls := l.snapshot()
	if last := calls[len(calls)-1].Spec; last.DaemonID != "d-a" || last.MachinePool == nil {
		t.Fatalf("launched spec = daemon %q pool %v", last.DaemonID, last.MachinePool)
	}
}
