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
	// heldOf is the admitted workflow's held capacity slots per member.
	heldOf map[string]int
}

var catalogJSON = `[{"slug":"pr-review","yaml_content":"x","content_hash":"h1"}]`

// agreed is catalogJSON's digest: the catalog every fake member holds.
var agreed, _ = machinepool.CatalogDigest(&catalogJSON, nil)

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

// CountTriggerCapacityHeldByMember models this workflow's held slots as
// heldOf, independent of each member's pool-wide LiveRuns.
func (s *fakePoolStore) CountTriggerCapacityHeldByMember(context.Context, string, string, string, string) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heldOf, nil
}

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
	return &core.Trigger{ID: "t-1", UserID: "user-1", ProjectID: "p-1", Name: "pr-review", Workflow: "pr-review", MachinePoolID: "pool-cicd"}
}

func TestLaunchOnPool_SpreadsAndPinsEachRunToItsMember(t *testing.T) {
	store := newFakePool(2, "a", "b", "c")
	l := &poolLauncher{store: store}
	placer := machinepool.NewPlacer(store)
	got := map[string]int{}
	for i := 0; i < 6; i++ {
		if _, err := launchOnPool(context.Background(), placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false, agreed); err != nil {
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
	_, err := launchOnPool(context.Background(), placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false, agreed)
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
		if _, err := launchOnPool(ctx, placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false, agreed); err != nil {
			t.Fatal(err)
		}
	}
	store.set("b", func(m *core.MachinePoolMemberRow) { m.Phase = "disconnected" })
	if _, err := launchOnPool(ctx, placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false, agreed); err != nil {
		t.Fatal(err)
	}
	calls := l.snapshot()
	if last := calls[len(calls)-1].Spec.DaemonID; last != "d-c" {
		t.Fatalf("third run went to %s, want c (a full, b down)", last)
	}
	// A retried fire of b's already-launched run resumes it: never re-placed.
	if _, err := launchOnPool(ctx, placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, true, agreed); err != nil {
		t.Fatal(err)
	}
	calls = l.snapshot()
	if resumed := calls[len(calls)-1].Spec; resumed.DaemonID != "" || resumed.MachinePool == nil {
		t.Fatalf("a resumed launch was re-placed: daemon %q", resumed.DaemonID)
	}
	// Everything full or down: the next fire waits, naming b.
	_, err := launchOnPool(ctx, placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false, agreed)
	var declined *launch.DeclinedError
	if !errors.As(err, &declined) || !strings.Contains(declined.Reason, "b: disconnected") {
		t.Fatalf("got %v, want a wait naming b", err)
	}
	// b's run ends and b reconnects current: it takes the next run.
	store.set("b", func(m *core.MachinePoolMemberRow) { m.Phase, m.LiveRuns = "connected", 0 })
	if _, err := launchOnPool(ctx, placer, l, poolTrigger(), launch.Event{}, launch.Spec{}, false, agreed); err != nil {
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
	if _, err := launchOnPool(context.Background(), machinepool.NewPlacer(store), l, poolTrigger(), launch.Event{}, launch.Spec{}, false, agreed); err != nil {
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
		if _, err := launchOnPool(context.Background(), machinepool.NewPlacer(store), l, poolTrigger(), launch.Event{}, launch.Spec{}, false, agreed); err != nil {
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
	// The fire reads the pool first: a missing pool is a verdict there...
	_, _, _, err := bindPoolCatalog(context.Background(), machinepool.NewPlacer(store), tr)
	if !isVerdict(err) {
		t.Fatalf("bind: got %v, want a verdict", err)
	}
	// ...and at placement, should it vanish in between.
	_, err = launchOnPool(context.Background(), machinepool.NewPlacer(store), &poolLauncher{store: store}, tr, launch.Event{}, launch.Spec{}, false, agreed)
	var v *launch.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("launch: got %v, want a validation verdict", err)
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

// catalogResolver serves catalog A from machines d-a/d-b and catalog B from
// d-c, and B as the union (B was pushed last). A's pr-review admits 1 run
// per member; B's admits a fixed 100.
type catalogResolver struct{ machines []string }

func (r *catalogResolver) wf(concurrency *reliantv1.WorkflowConcurrency) *reliantv1.Workflow {
	return &reliantv1.Workflow{Name: "pr-review", Concurrency: concurrency}
}

func (r *catalogResolver) ResolveRunWorkflow(context.Context, string, string, string) (*reliantv1.Workflow, error) {
	r.machines = append(r.machines, "")
	return r.wf(&reliantv1.WorkflowConcurrency{MaxRunning: 100}), nil
}

func (r *catalogResolver) ResolveRunWorkflowOn(_ context.Context, _, _, _, daemonID string) (*reliantv1.Workflow, error) {
	r.machines = append(r.machines, daemonID)
	if daemonID == "d-c" {
		return r.wf(&reliantv1.WorkflowConcurrency{MaxRunning: 100}), nil
	}
	return r.wf(&reliantv1.WorkflowConcurrency{PerMember: 1}), nil
}

// The #1915 review blocker: two members hold catalog A, a third (and the
// union) hold a newer B. The fire must read its declaration and concurrency
// from A, the catalog its run is placed on, never from B.
func TestPoolFireReadsTheAgreedCatalogNotTheUnion(t *testing.T) {
	store := newFakePool(0, "a", "b", "c")
	stale := `[{"slug":"pr-review","yaml_content":"y","content_hash":"B"}]`
	newer := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	store.set("c", func(m *core.MachinePoolMemberRow) { m.WorkflowsJSON, m.CatalogPushedAt = &stale, &newer })
	placer := machinepool.NewPlacer(store)
	resolver := &catalogResolver{}
	ctx := context.Background()

	bound, catalog, wait, err := bindPoolCatalog(ctx, placer, poolTrigger())
	if err != nil || wait != "" || catalog != agreed || bound.CatalogDaemonID != "d-a" {
		t.Fatalf("bind = %+v %q %q %v; want d-a on catalog A", bound, catalog, wait, err)
	}
	policy, err := capacityPolicy(ctx, resolver, bound)
	if err != nil || policy.GetPerMember() != 1 || policy.GetMaxRunning() != 0 {
		t.Fatalf("policy = %v %v, want catalog A's per_member 1 (read on %v)", policy, err, resolver.machines)
	}
	limit, capped, err := capacityLimit(ctx, placer, bound, policy)
	if err != nil || !capped || limit != 2 {
		t.Fatalf("limit = %d %v %v, want 2 (a, b on A; c drifted)", limit, capped, err)
	}
	// Unbound, the same trigger reads the union's B: what the fix prevents.
	if p, _ := capacityPolicy(ctx, resolver, poolTrigger()); p.GetMaxRunning() != 100 {
		t.Fatalf("test premise: the union should serve B, got %v", p)
	}
	// And the run is placed on A only.
	l := &poolLauncher{store: store}
	for i := 0; i < 3; i++ {
		if _, err := launchOnPool(ctx, placer, l, bound, launch.Event{}, launch.Spec{}, false, catalog); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range l.snapshot() {
		if c.Spec.DaemonID == "d-c" {
			t.Fatal("placed on the member whose catalog the fire did not read")
		}
	}
}

// A push between reading the declaration and placing changes the pool's
// agreed catalog: the run is not launched on decisions read from the old one.
func TestLaunchOnPool_CatalogChangedDuringFireWaits(t *testing.T) {
	store := newFakePool(0, "a")
	_, err := launchOnPool(context.Background(), machinepool.NewPlacer(store), &poolLauncher{store: store}, poolTrigger(), launch.Event{}, launch.Spec{}, false, "an-older-catalog")
	var declined *launch.DeclinedError
	if !errors.As(err, &declined) || !isPoolWaiting(declined.Reason) || !strings.Contains(declined.Reason, "catalog changed") {
		t.Fatalf("got %v, want a pool wait naming the catalog change", err)
	}
}

func TestBindPoolCatalog_UnsyncedPoolWaits(t *testing.T) {
	store := newFakePool(0, "a")
	store.set("a", func(m *core.MachinePoolMemberRow) { m.WorkflowsJSON = nil })
	_, _, wait, err := bindPoolCatalog(context.Background(), machinepool.NewPlacer(store), poolTrigger())
	if err != nil || !isPoolWaiting(wait) {
		t.Fatalf("got %q %v, want a pool wait", wait, err)
	}
}

// Several workflows share the pool and member b dies holding runs: only
// THIS workflow's held slots on b count toward its cap.
func TestCapacityLimit_DeadMemberCountsOnlyThisWorkflowsSlots(t *testing.T) {
	store := newFakePool(0, "a", "b")
	store.set("b", func(m *core.MachinePoolMemberRow) { m.Phase, m.LiveRuns = "disconnected", 4 })
	policy := &reliantv1.WorkflowConcurrency{PerMember: 4}
	placer := machinepool.NewPlacer(store)
	if got, _, _ := capacityLimit(context.Background(), placer, poolTrigger(), policy); got != 4 {
		t.Fatalf("cap = %d, want 4: four unrelated runs on dead b raised it", got)
	}
	store.heldOf = map[string]int{"d-b": 1}
	if got, _, _ := capacityLimit(context.Background(), placer, poolTrigger(), policy); got != 5 {
		t.Fatalf("cap = %d, want 5 (a's 4 + this workflow's 1 held on b)", got)
	}
}
