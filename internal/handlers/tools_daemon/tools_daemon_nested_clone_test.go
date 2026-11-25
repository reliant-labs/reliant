// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// nestedT0 is the fake clock's start: nothing here reads the wall clock.
var nestedT0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// fakeClock is the time a pass and its fake store share; wait advances it.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeNestedStore is project_daemon_repos and one checkout row in memory,
// with ClaimNextProjectDaemonRepo's rules: one at a time, shallowest first,
// never inside a repository still to clone, a claim taken over only once its
// lease ran out, and settling fenced on the claim.
type fakeNestedStore struct {
	mu          sync.Mutex
	clock       *fakeClock
	rows        []*core.ProjectDaemonRepo
	claims      map[string]string // repoID → claim
	leases      []time.Duration
	claimCalled bool
	installed   bool
	failedWith  string
}

func newFakeNestedStore(clock *fakeClock, rows ...*core.ProjectDaemonRepo) *fakeNestedStore {
	return &fakeNestedStore{clock: clock, rows: rows, claims: map[string]string{}}
}

func nestedDepth(rel string) int { return strings.Count(strings.Trim(rel, "/"), "/") }

func nestedInside(rel, dir string) bool {
	rel, dir = strings.Trim(rel, "/"), strings.Trim(dir, "/")
	return dir != "" && strings.HasPrefix(rel, dir+"/")
}

func (f *fakeNestedStore) ClaimNextProjectDaemonRepo(_ context.Context, _, _, claimID string, lease time.Duration) (*core.NestedRepoClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalled = true
	now := f.clock.Now()
	var best *core.ProjectDaemonRepo
	for _, r := range f.rows {
		ready := r.State == core.NestedRepoPending ||
			(r.State == core.NestedRepoCloning && (r.LeaseUntil == nil || r.LeaseUntil.Before(now)))
		if !ready {
			continue
		}
		blocked := false
		for _, a := range f.rows {
			if a != r && a.State.Unsettled() && nestedInside(r.RelativePath, a.RelativePath) {
				blocked = true
			}
		}
		if blocked {
			continue
		}
		if best == nil || nestedDepth(r.RelativePath) < nestedDepth(best.RelativePath) ||
			(nestedDepth(r.RelativePath) == nestedDepth(best.RelativePath) && r.RelativePath < best.RelativePath) {
			best = r
		}
	}
	if best == nil {
		return nil, nil
	}
	until := now.Add(lease)
	best.State, best.Error, best.LeaseUntil = core.NestedRepoCloning, "", &until
	f.claims[best.RepoID] = claimID
	f.leases = append(f.leases, lease)
	insideFailed := false
	for _, a := range f.rows {
		if a.State == core.NestedRepoFailed && nestedInside(best.RelativePath, a.RelativePath) {
			insideFailed = true
		}
	}
	c := *best
	return &core.NestedRepoClaim{Repo: &c, InsideFailed: insideFailed}, nil
}

func (f *fakeNestedStore) SettleProjectDaemonRepo(_ context.Context, _, _, repoID, claimID, installErr string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.RepoID != repoID || r.State != core.NestedRepoCloning || f.claims[repoID] != claimID {
			continue
		}
		r.State, r.Error, r.LeaseUntil = core.NestedRepoInstalled, "", nil
		if installErr != "" {
			r.State, r.Error = core.NestedRepoFailed, installErr
		}
		delete(f.claims, repoID)
		return true, nil
	}
	return false, nil
}

func (f *fakeNestedStore) ListProjectDaemonReposForPair(context.Context, string, string) ([]*core.ProjectDaemonRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*core.ProjectDaemonRepo, len(f.rows))
	for i, r := range f.rows {
		c := *r
		out[i] = &c
	}
	return out, nil
}

func (f *fakeNestedStore) SettleProjectDaemonFromNestedReposInScope(_ context.Context, _, _, installErr string, _ *core.OrgScope) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if installErr != "" {
		f.failedWith = installErr
		return true, nil
	}
	f.installed = true
	return true, nil
}

func nestedRow(id, name, rel, remote string) *core.ProjectDaemonRepo {
	return &core.ProjectDaemonRepo{ProjectID: "p1", DaemonID: "d1", RepoID: id, Name: name, RelativePath: rel, RemoteURL: remote, State: core.NestedRepoPending}
}

func landed(context.Context) (bool, error) { return true, nil }

func allowed(context.Context) (*core.OrgScope, error) { return nil, nil }

// testPass is a pass over store on the fake clock: its waits advance the
// clock and are recorded.
func testPass(store *fakeNestedStore, claimID, root string, run cloneRunner) (*nestedClonePass, *[]time.Duration) {
	var waits []time.Duration
	return &nestedClonePass{
		store: store, run: run, landed: landed, authorize: allowed,
		wait: func(ctx context.Context, d time.Duration) bool {
			if ctx.Err() != nil {
				return false
			}
			waits = append(waits, d)
			store.clock.Advance(d)
			return true
		},
		now:     store.clock.Now,
		claimID: claimID, projectID: "p1", daemonID: "d1", rootPath: root,
	}, &waits
}

func TestCloneNestedRepos_ClonesEachIntoItsPathShallowestFirst(t *testing.T) {
	store := newFakeNestedStore(&fakeClock{now: nestedT0},
		nestedRow("r-deep", "inner", "tools/inner", "git@github.com:acme/inner.git"),
		nestedRow("r-tools", "tools", "tools", "https://github.com/acme/tools"),
		nestedRow("r-forge", "forge", "forge", "git@github.com:acme/forge.git"),
	)
	var ran []cloneStep
	pass, _ := testPass(store, "c1", "/srv/projects/acme", func(_ context.Context, step cloneStep) error { ran = append(ran, step); return nil })

	if !pass.runPass(context.Background()) {
		t.Fatal("the checkout was not settled")
	}
	// A github.com SSH remote clones over https: the machine's git
	// authenticates with the user's token, which SSH never consults.
	want := []cloneStep{
		{Repo: "https://github.com/acme/forge.git", Path: "/srv/projects/acme/forge"},
		{Repo: "https://github.com/acme/tools", Path: "/srv/projects/acme/tools"},
		{Repo: "https://github.com/acme/inner.git", Path: "/srv/projects/acme/tools/inner"},
	}
	if len(ran) != len(want) {
		t.Fatalf("clones = %+v, want %+v", ran, want)
	}
	for i := range want {
		if ran[i] != want[i] {
			t.Errorf("clone %d = %+v, want %+v", i, ran[i], want[i])
		}
	}
	if !store.installed || store.failedWith != "" {
		t.Fatalf("checkout = installed %v, failed %q; want installed", store.installed, store.failedWith)
	}
	// Each claim holds past the latest the machine can still run its clone.
	for _, lease := range store.leases {
		if lease <= time.Duration(nestedCloneTimeoutMs)*time.Millisecond {
			t.Fatalf("lease %s does not cover the clone's own timeout", lease)
		}
	}
}

func TestCloneNestedRepos_AFailureNamesTheRepositoryAndGitsError(t *testing.T) {
	store := newFakeNestedStore(&fakeClock{now: nestedT0},
		nestedRow("r-forge", "forge", "forge", "git@github.com:acme/forge.git"),
		nestedRow("r-in", "plugin", "forge/plugin", "https://github.com/acme/plugin"),
		nestedRow("r-web", "web", "web", "https://github.com/acme/web"),
	)
	gitErr := "git clone failed: exit status 128: git@github.com: Permission denied (publickey)."
	var ran []string
	pass, _ := testPass(store, "c1", "/w/acme", func(_ context.Context, step cloneStep) error {
		ran = append(ran, step.Path)
		if strings.HasSuffix(step.Path, "/forge") {
			return errors.New(gitErr)
		}
		return nil
	})

	pass.runPass(context.Background())

	// Nothing is cloned inside a repository that failed: its clone would
	// create forge/ and forge could never be cloned there again.
	if strings.Join(ran, ",") != "/w/acme/forge,/w/acme/web" {
		t.Fatalf("clones = %v, want forge then web, and nothing inside forge", ran)
	}
	if store.installed {
		t.Fatal("a checkout missing a nested repository must not read as installed")
	}
	for _, want := range []string{"forge (forge/)", gitErr, "plugin (forge/plugin/)"} {
		if !strings.Contains(store.failedWith, want) {
			t.Errorf("install error %q does not name %q", store.failedWith, want)
		}
	}
	if strings.Contains(store.failedWith, "web (web/)") {
		t.Errorf("install error %q names web, which cloned", store.failedWith)
	}
	for _, r := range store.rows {
		if r.RepoID == "r-forge" && (r.State != core.NestedRepoFailed || r.Error != gitErr) {
			t.Errorf("forge's own row = %s %q, want failed with git's error", r.State, r.Error)
		}
	}
}

func TestCloneNestedRepos_WaitsForTheRoot(t *testing.T) {
	store := newFakeNestedStore(&fakeClock{now: nestedT0}, nestedRow("r1", "forge", "forge", "https://github.com/acme/forge"))
	pass, _ := testPass(store, "c1", "/w/acme", func(context.Context, cloneStep) error { t.Fatal("cloned before the root landed"); return nil })
	pass.landed = func(context.Context) (bool, error) { return false, nil }

	if pass.runPass(context.Background()) {
		t.Fatal("settled a checkout whose root has not landed")
	}
	if store.claimCalled || store.installed || store.failedWith != "" {
		t.Fatalf("store touched before the root landed: %+v", store)
	}
}

// Review B1: a pass the project is no longer the machine's to set up for
// (another org's, a revoked membership, a failed lookup) claims and clones
// nothing, and settles nothing.
func TestCloneNestedRepos_AnUnauthorizedPassClaimsNothing(t *testing.T) {
	store := newFakeNestedStore(&fakeClock{now: nestedT0}, nestedRow("r1", "forge", "forge", "https://github.com/acme/forge"))
	pass, _ := testPass(store, "c1", "/w/acme", func(context.Context, cloneStep) error {
		t.Fatal("cloned for a project that is not the machine's")
		return nil
	})
	pass.authorize = func(context.Context) (*core.OrgScope, error) { return nil, errNestedProjectNotMachines }

	if pass.runPass(context.Background()) {
		t.Fatal("an unauthorized pass settled the checkout")
	}
	if store.claimCalled || store.installed || store.failedWith != "" {
		t.Fatalf("an unauthorized pass touched the store: %+v", store)
	}
}

// Review B2: the connection carrying a clone goes away and the machine
// reconnects at once. The new connection's pass clones what is free, waits
// for the old claim's lease (the machine may still be running that clone),
// then takes it over and settles the checkout, with no further reconnect.
func TestCloneNestedRepos_AQuickReconnectSettlesWithoutAnotherReconnect(t *testing.T) {
	clock := &fakeClock{now: nestedT0}
	store := newFakeNestedStore(clock,
		nestedRow("r-forge", "forge", "forge", "https://github.com/acme/forge"),
		nestedRow("r-web", "web", "web", "https://github.com/acme/web"),
	)

	// The first connection: its clone of forge is cut off by the drop.
	oldCtx, drop := context.WithCancel(context.Background())
	old, _ := testPass(store, "old", "/w/acme", func(_ context.Context, step cloneStep) error {
		if !strings.HasSuffix(step.Path, "/forge") {
			t.Fatalf("the old connection cloned %s after it went away", step.Path)
		}
		drop()
		return errCloneConnectionGone
	})
	if old.runPass(oldCtx) {
		t.Fatal("a pass whose connection went away settled the checkout")
	}
	if r := store.rows[0]; r.State != core.NestedRepoCloning {
		t.Fatalf("forge = %s; a clone the machine may still be running was settled", r.State)
	}

	// The reconnect, before the old claim runs out.
	var ran []string
	fresh, waits := testPass(store, "new", "/w/acme", func(_ context.Context, step cloneStep) error {
		ran = append(ran, step.Path)
		return nil
	})
	if !fresh.runPass(context.Background()) {
		t.Fatal("the reconnect's pass did not settle the checkout")
	}
	if strings.Join(ran, ",") != "/w/acme/web,/w/acme/forge" {
		t.Fatalf("clones = %v, want web at once, forge once the old claim ran out", ran)
	}
	if !store.installed {
		t.Fatal("checkout not installed")
	}
	var waited time.Duration
	for _, w := range *waits {
		if w < nestedWaitMin || w > nestedWaitMax {
			t.Fatalf("wait %s outside [%s, %s]", w, nestedWaitMin, nestedWaitMax)
		}
		waited += w
	}
	if waited < nestedCloneLease {
		t.Fatalf("took the old claim over after %s, before its lease (%s) ran out", waited, nestedCloneLease)
	}
}

// Review B3: while one pass clones a parent, another pass on the checkout
// clones neither the parent nor anything inside it, and the pass whose claim
// is taken over records nothing.
func TestCloneNestedRepos_ConcurrentPassesNeverCloneOneTreeTwice(t *testing.T) {
	clock := &fakeClock{now: nestedT0}
	store := newFakeNestedStore(clock,
		nestedRow("r-forge", "forge", "forge", "https://github.com/acme/forge"),
		nestedRow("r-plugin", "plugin", "forge/plugin", "https://github.com/acme/plugin"),
	)
	var second *nestedClonePass
	var secondRan []string
	first, _ := testPass(store, "first", "/w/acme", func(ctx context.Context, step cloneStep) error {
		if strings.HasSuffix(step.Path, "/forge") {
			// Mid-clone of forge, a second trigger's pass runs; it waits
			// for forge's claim and gives up when its context ends.
			waitCtx, cancel := context.WithCancel(ctx)
			second.wait = func(context.Context, time.Duration) bool { cancel(); return false }
			if second.runPass(waitCtx) {
				t.Error("the second pass settled the checkout mid-clone")
			}
		}
		return nil
	})
	second, _ = testPass(store, "second", "/w/acme", func(_ context.Context, step cloneStep) error {
		secondRan = append(secondRan, step.Path)
		return nil
	})

	if !first.runPass(context.Background()) {
		t.Fatal("the first pass did not settle the checkout")
	}
	if len(secondRan) != 0 {
		t.Fatalf("the second pass cloned %v while forge was cloning", secondRan)
	}

	// A claim taken over after its lease ran out: the old pass records nothing.
	store.rows[0].State, store.rows[0].Error = core.NestedRepoPending, ""
	if c, _ := store.ClaimNextProjectDaemonRepo(context.Background(), "p1", "d1", "stale", nestedCloneLease); c == nil {
		t.Fatal("no claim")
	}
	clock.Advance(nestedCloneLease + time.Second)
	if c, _ := store.ClaimNextProjectDaemonRepo(context.Background(), "p1", "d1", "taker", nestedCloneLease); c == nil {
		t.Fatal("the expired claim was not taken over")
	}
	if ok, _ := store.SettleProjectDaemonRepo(context.Background(), "p1", "d1", "r-forge", "stale", "boom"); ok {
		t.Fatal("a pass whose claim was taken over recorded its outcome")
	}
}

func TestNextNestedWake(t *testing.T) {
	soon := nestedT0.Add(5 * time.Second)
	past := nestedT0.Add(-time.Minute)
	far := nestedT0.Add(time.Hour)
	for _, tc := range []struct {
		name string
		rows []*core.ProjectDaemonRepo
		want time.Duration
	}{
		{"no lease", []*core.ProjectDaemonRepo{{State: core.NestedRepoPending}}, nestedWaitMax},
		{"earliest lease", []*core.ProjectDaemonRepo{{State: core.NestedRepoCloning, LeaseUntil: &far}, {State: core.NestedRepoCloning, LeaseUntil: &soon}}, 5 * time.Second},
		{"lease already out", []*core.ProjectDaemonRepo{{State: core.NestedRepoCloning, LeaseUntil: &past}}, nestedWaitMin},
	} {
		if got := nextNestedWake(tc.rows, nestedT0); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestNestedFailureReason(t *testing.T) {
	if got := nestedFailureReason([]*core.ProjectDaemonRepo{{Name: "a", RelativePath: "a", State: core.NestedRepoInstalled}}); got != "" {
		t.Fatalf("reason with no failure = %q", got)
	}
	long := strings.Repeat("x", 3000)
	got := nestedFailureReason([]*core.ProjectDaemonRepo{{Name: "a", RelativePath: "a", State: core.NestedRepoFailed, Error: long}})
	if len(got) > nestedFailureReasonMax || !strings.HasPrefix(got, "the project cloned, but its nested repository a (a/): ") {
		t.Fatalf("reason = %q (len %d)", got[:80], len(got))
	}
}
