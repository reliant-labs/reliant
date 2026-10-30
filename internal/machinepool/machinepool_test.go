// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 30, 16, 0, 0, 0, time.UTC)

func member(name string, phase Phase, live int) Member {
	return Member{DaemonID: "id-" + name, Name: name, Phase: phase, LiveRuns: live, Installed: true, CatalogDigest: "cat-main", CatalogPushedAt: t0}
}

func pool(maxPer int) Pool {
	return Pool{ID: "pool-1", Name: "cicd", Selector: DefaultSelector("cicd"), MaxRunsPerMember: maxPer}
}

func TestDecide_LeastLoadedConnectedMember(t *testing.T) {
	d := Decide(pool(3), []Member{member("a", PhaseConnected, 2), member("b", PhaseConnected, 0), member("c", PhaseConnected, 1)})
	if d.Kind != Run || d.Member.Name != "b" {
		t.Fatalf("got %v on %q, want run on b (fewest live runs)", d.Kind, d.Member.Name)
	}
}

func TestDecide_TiesBreakOnNameDeterministically(t *testing.T) {
	ms := []Member{member("c", PhaseConnected, 1), member("a", PhaseConnected, 1), member("b", PhaseConnected, 1)}
	for i := 0; i < 5; i++ {
		if d := Decide(pool(0), ms); d.Member.Name != "a" {
			t.Fatalf("got %q, want a", d.Member.Name)
		}
		ms = append(ms[1:], ms[0])
	}
}

func TestDecide_FullMembersArePassedOver(t *testing.T) {
	d := Decide(pool(2), []Member{member("a", PhaseConnected, 2), member("b", PhaseConnected, 1)})
	if d.Kind != Run || d.Member.Name != "b" {
		t.Fatalf("got %v on %q, want run on b", d.Kind, d.Member.Name)
	}
	if len(d.Skipped) != 1 || d.Skipped[0].Name != "a" || !strings.HasPrefix(d.Skipped[0].Reason, ReasonFull) {
		t.Fatalf("skipped = %+v, want a full", d.Skipped)
	}
}

func TestDecide_PrefersConnectedThenStartingThenWake(t *testing.T) {
	d := Decide(pool(0), []Member{member("a", PhaseSuspended, 0), member("b", PhaseStarting, 0), member("c", PhaseConnected, 5)})
	if d.Kind != Run || d.Member.Name != "c" {
		t.Fatalf("got %v on %q, want run on connected c even though busier", d.Kind, d.Member.Name)
	}
	d = Decide(pool(0), []Member{member("a", PhaseSuspended, 0), member("b", PhaseStarting, 0)})
	if d.Kind != Run || d.Member.Name != "b" {
		t.Fatalf("got %v on %q, want run on starting b", d.Kind, d.Member.Name)
	}
	d = Decide(pool(0), []Member{member("a", PhaseSuspended, 0), member("b", PhaseDisconnected, 0)})
	if d.Kind != Wake || d.Member.Name != "a" {
		t.Fatalf("got %v on %q, want wake a", d.Kind, d.Member.Name)
	}
}

func TestDecide_QueuesWhenEveryMemberIsFullOrDown(t *testing.T) {
	d := Decide(pool(1), []Member{member("a", PhaseConnected, 1), member("b", PhaseFailed, 0), member("c", PhaseDisconnected, 0)})
	if d.Kind != Queue {
		t.Fatalf("got %v, want queue", d.Kind)
	}
	reason := d.Reason("cicd")
	for _, want := range []string{"a: full (1/1 runs)", "b: " + ReasonFailed, "c: " + ReasonDisconnected} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q lacks %q", reason, want)
		}
	}
}

func TestDecide_EmptyPoolQueuesAndSaysSo(t *testing.T) {
	d := Decide(pool(0), nil)
	if d.Kind != Queue || !strings.Contains(d.Reason("cicd"), "no members") {
		t.Fatalf("got %v %q", d.Kind, d.Reason("cicd"))
	}
}

// A member whose catalog differs from the pool's is never placed on, even
// when it is the only free one: the run waits (fail closed).
func TestDecide_CatalogDriftFailsClosed(t *testing.T) {
	stale := member("c", PhaseConnected, 0)
	stale.CatalogDigest, stale.CatalogPushedAt = "cat-old", t0.Add(time.Hour) // newest push, wrong content
	d := Decide(pool(1), []Member{member("a", PhaseConnected, 1), member("b", PhaseConnected, 1), stale})
	if d.Kind != Queue {
		t.Fatalf("got %v on %q, want queue: the only free member has a different catalog", d.Kind, d.Member.Name)
	}
	if d.ExpectedCatalog != "cat-main" {
		t.Fatalf("expected catalog %q, want the majority's", d.ExpectedCatalog)
	}
	if !strings.Contains(d.Reason("cicd"), "c: "+ReasonCatalogDiffers) {
		t.Fatalf("reason %q does not name the drift", d.Reason("cicd"))
	}
}

func TestDecide_NotInstalledOrNotSyncedIsIneligible(t *testing.T) {
	a := member("a", PhaseConnected, 0)
	a.Installed = false
	b := member("b", PhaseConnected, 0)
	b.CatalogDigest = ""
	d := Decide(pool(0), []Member{a, b, member("c", PhaseConnected, 3)})
	if d.Kind != Run || d.Member.Name != "c" {
		t.Fatalf("got %v on %q, want c", d.Kind, d.Member.Name)
	}
}

func TestExpectedCatalog_MajorityThenNewest(t *testing.T) {
	mk := func(name, digest string, at time.Time) Member {
		m := member(name, PhaseConnected, 0)
		m.CatalogDigest, m.CatalogPushedAt = digest, at
		return m
	}
	if got := ExpectedCatalog([]Member{mk("a", "x", t0), mk("b", "x", t0), mk("c", "y", t0.Add(time.Hour))}); got != "x" {
		t.Fatalf("majority: got %q", got)
	}
	if got := ExpectedCatalog([]Member{mk("a", "x", t0), mk("b", "y", t0.Add(time.Minute))}); got != "y" {
		t.Fatalf("tie: got %q, want the newest", got)
	}
	failed := mk("c", "z", t0)
	failed.Phase = PhaseFailed
	if got := ExpectedCatalog([]Member{failed}); got != "" {
		t.Fatalf("a failed member's catalog counted: %q", got)
	}
}

// The owner's acceptance case: a fake three-member pool where one member dies
// in the middle of a run. B is placed on, dies, and every later placement
// lands on A or C; B's still-live run is neither counted against A and C nor
// lets anything new land on B. When B comes back current it is used again.
func TestPlacement_MemberDiesMidRun(t *testing.T) {
	p := pool(2)
	members := map[string]*Member{}
	for _, n := range []string{"a", "b", "c"} {
		m := member(n, PhaseConnected, 0)
		members[n] = &m
	}
	snapshot := func() []Member {
		return []Member{*members["a"], *members["b"], *members["c"]}
	}
	place := func() Decision {
		d := Decide(p, snapshot())
		if d.Kind == Run {
			members[d.Member.Name].LiveRuns++
		}
		return d
	}

	// Three runs spread one per member.
	got := map[string]int{}
	for i := 0; i < 3; i++ {
		got[place().Member.Name]++
	}
	if got["a"] != 1 || got["b"] != 1 || got["c"] != 1 {
		t.Fatalf("first three runs = %v, want one per member", got)
	}

	// B's kata VM dies mid-run: its run is still live in the database.
	members["b"].Phase = PhaseDisconnected
	if eligible := Eligible(snapshot()); len(eligible) != 2 {
		t.Fatalf("eligible after B died = %d, want 2", len(eligible))
	}
	for i := 0; i < 2; i++ {
		d := place()
		if d.Kind != Run || d.Member.Name == "b" {
			t.Fatalf("placement %d after B died: %v on %q", i, d.Kind, d.Member.Name)
		}
	}
	// A and C are full (2/2), B is down: the next run waits, naming why.
	d := place()
	if d.Kind != Queue || !strings.Contains(d.Reason(p.Name), "b: "+ReasonDisconnected) {
		t.Fatalf("got %v %q, want queue naming b disconnected", d.Kind, d.Reason(p.Name))
	}

	// Per-member concurrency of 2: B's live run keeps its slot (it holds one
	// until it ends), so A and C still get their full 2 each.
	if c := PerMemberCapacity(snapshot(), 2); c != 2+1+2 {
		t.Fatalf("capacity with B down = %d, want 5 (2 + B's held 1 + 2)", c)
	}

	// B's run ends (Stage 3 / #1889) and B comes back on the pool's catalog.
	members["b"].LiveRuns, members["b"].Phase = 0, PhaseConnected
	if d := place(); d.Kind != Run || d.Member.Name != "b" {
		t.Fatalf("after B recovered: %v on %q, want b", d.Kind, d.Member.Name)
	}
}

func TestPerMemberCapacity_ScalesWithMembers(t *testing.T) {
	ms := []Member{member("a", PhaseConnected, 0), member("b", PhaseSuspended, 0)}
	if got := PerMemberCapacity(ms, 4); got != 8 {
		t.Fatalf("2 members x 4 = %d", got)
	}
	ms = append(ms, member("c", PhaseStarting, 0))
	if got := PerMemberCapacity(ms, 4); got != 12 {
		t.Fatalf("adding a member: %d, want 12", got)
	}
	gone := member("d", PhaseRemoved, 0)
	if got := PerMemberCapacity(append(ms, gone), 4); got != 12 {
		t.Fatalf("a removed member with no runs added capacity: %d", got)
	}
	if got := PerMemberCapacity(nil, 4); got != 0 {
		t.Fatalf("empty pool: %d", got)
	}
}

func TestMatches(t *testing.T) {
	if !Matches(map[string]string{"pool": "cicd", "size": "2xl"}, map[string]string{"pool": "cicd"}) {
		t.Fatal("superset should match")
	}
	if Matches(map[string]string{"pool": "other"}, map[string]string{"pool": "cicd"}) || Matches(nil, map[string]string{"pool": "cicd"}) {
		t.Fatal("mismatch matched")
	}
	if Matches(map[string]string{"pool": "cicd"}, nil) {
		t.Fatal("an empty selector must match nothing")
	}
}

func TestCatalogDigest_IgnoresOrderAndIsContentSensitive(t *testing.T) {
	a := `[{"slug":"pr-review","relative_path":"control-plane/.reliant/workflows/pr-review.yaml","yaml_content":"x","content_hash":"h1"},{"slug":"cp-merge","yaml_content":"y","content_hash":"h2"}]`
	b := `[{"slug":"cp-merge","yaml_content":"y","content_hash":"h2"},{"slug":"pr-review","relative_path":"control-plane/.reliant/workflows/pr-review.yaml","yaml_content":"x","content_hash":"h1"}]`
	c := `[{"slug":"cp-merge","yaml_content":"y","content_hash":"h3"},{"slug":"pr-review","relative_path":"control-plane/.reliant/workflows/pr-review.yaml","yaml_content":"x","content_hash":"h1"}]`
	da, err := CatalogDigest(&a, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := CatalogDigest(&b, nil)
	dc, _ := CatalogDigest(&c, nil)
	if da != db {
		t.Fatal("order changed the digest")
	}
	if da == dc {
		t.Fatal("a content change did not change the digest")
	}
	if d, _ := CatalogDigest(nil, nil); d != "" {
		t.Fatalf("no catalog = %q, want empty", d)
	}
	bad := "{"
	if _, err := CatalogDigest(&bad, nil); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}
