package cli

// The three new record seams, exercised through the interfaces their
// consumers will hold.
//
// F6a consumes bundleRecorder and F6b consumes sessionReporter. These tests
// take the machine backend AS those interfaces, so a method whose signature
// drifts away from the seam fails here rather than in the task that comes to
// use it.
//
// There is no apply seam to exercise: the applyRecorder interface was deleted
// as dead code (no caller ever bracketed an apply with it). The machine
// ledger's apply records are HISTORY an older forge wrote, and the storage
// semantics below are tested against the store directly — what
// `forge ledger show` and `forge ledger export` read back.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

func testRecordStore(t *testing.T, dir string) machineRecordStore {
	t.Helper()
	ensureLedgerReady(t, dir)
	store, err := recordStoreFor(dir)
	if err != nil {
		t.Fatalf("recordStoreFor(%s): %v", dir, err)
	}
	return store
}

// testBundleBlobs is a stand-in for a built bundle's bytes. The MACHINE
// backend ignores them — its blobs are already in an OCI layout it owns — so
// their content is irrelevant to these tests and only their presence in the
// seam is. The hosted backend, where the bytes ARE the contract, is tested
// against control-plane's own protojson in the hosted_dsot_* files.
func testBundleBlobs() bundleBlobs {
	return bundleBlobs{
		Repository: "ghcr.io/acme/bundles",
		Manifest:   []byte(`{"schemaVersion":2}`),
		Config:     []byte(`{"schema":"forge.dev/bundle/v1"}`),
	}
}

func testShape() release.Shape {
	return release.Shape{
		Kind: release.EnvSelfManaged,
		Workloads: []release.ShapeWorkload{
			{Name: "api", Runtime: "cluster", Cluster: "prod", Artifact: "ghcr.io/acme/api"},
		},
	}
}

// A bundle IS its content, so recording one twice for an env is a RETRY, not
// a second bundle. That is what makes a push whose RecordBundle failed
// safely re-runnable (failure mode F-3) — the alternative, two records for
// one digest, would make "which bundle is applied" ambiguous.
func TestBundleRecorder_IsIdempotentOnDigest(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-project")
	var recorder bundleRecorder = testRecordStore(t, dir)
	ctx := context.Background()

	b := release.BundleRecord{
		Env:          "prod",
		Release:      "v1.0.0",
		Digest:       "sha256:" + strings.Repeat("a", 64),
		Reference:    "ghcr.io/acme/bundles@sha256:" + strings.Repeat("a", 64),
		ConfigDigest: "sha256:" + strings.Repeat("c", 64),
		Shape:        testShape(),
	}

	blobs := testBundleBlobs()
	first, created, err := recorder.RecordBundle(ctx, b, blobs)
	if err != nil || !created {
		t.Fatalf("first record = (created=%v, %v), want created", created, err)
	}
	if first.ID == "" || first.CreatedAt.IsZero() {
		t.Errorf("the backend must stamp an id and a time, got %+v", first)
	}

	again, created, err := recorder.RecordBundle(ctx, b, blobs)
	if err != nil {
		t.Fatalf("re-record: %v", err)
	}
	if created {
		t.Error("re-recording one digest for one env must report created=false")
	}
	if again.ID != first.ID {
		t.Errorf("a retry must return the record already held (%q), got %q", first.ID, again.ID)
	}

	got, err := recorder.Bundle(ctx, first.ID)
	if err != nil || got == nil {
		t.Fatalf("Bundle(%q) = (%v, %v)", first.ID, got, err)
	}
	if got.Digest != b.Digest || len(got.Shape.Workloads) != 1 {
		t.Errorf("the record must round-trip its digest and shape, got %+v", got)
	}
}

// The machine ledger's APPLY STORAGE, which is what `forge ledger show` and
// `forge ledger export` read. Nothing in forge writes one any more — these are
// records an older forge left behind — but the file's semantics still have to
// hold, because a reader joins outcomes to applies by id and derives a state
// from the pair.
//
// An apply is two lines: begun, then reported. The gap between them is a real
// state — one with no outcome past its deadline reads as ABANDONED, which is
// deliberately distinct from failed, because "we could not look" is its own
// answer and a stored "assume success" would be a lie (failure mode F-2).
func TestMachineLedgerApplyStorage_JoinsOutcomesAndDerivesState(t *testing.T) {
	dir := newLedgerTestProject(t, "apply-project")
	store := testRecordStore(t, dir).store
	now := time.Now().UTC().Truncate(time.Second)

	begun, err := store.BeginApply(release.Apply{
		Env:        "prod",
		BundleID:   "bundle-1",
		CreatedAt:  now,
		DeadlineAt: now.Add(10 * time.Minute),
	}, false)
	if err != nil {
		t.Fatalf("BeginApply: %v", err)
	}
	if begun.ID == "" {
		t.Fatal("the backend must stamp an apply id")
	}

	// In flight, so a second apply is refused — the same serialization the
	// hosted ledger enforces under its env row lock (failure mode F-5).
	_, err = store.BeginApply(release.Apply{
		Env:        "prod",
		BundleID:   "bundle-2",
		CreatedAt:  now,
		DeadlineAt: now.Add(10 * time.Minute),
	}, false)
	if err == nil {
		t.Fatal("a second apply on an env with one in flight must be refused without --supersede")
	}

	// With supersede it lands, and records that it overrode one.
	superseding, err := store.BeginApply(release.Apply{
		Env:        "prod",
		BundleID:   "bundle-2",
		CreatedAt:  now,
		DeadlineAt: now.Add(10 * time.Minute),
	}, true)
	if err != nil {
		t.Fatalf("BeginApply --supersede: %v", err)
	}
	if !superseding.SupersededInFlight {
		t.Error("an apply that overrode one in flight must record that it did — it is the audit trail")
	}

	// Reporting the outcome is idempotent on an identical report...
	outcome := release.ApplyOutcome{
		ApplyID:    superseding.ID,
		Status:     release.ApplySucceeded,
		Summary:    "2 workloads updated",
		FinishedAt: now.Add(time.Minute),
	}
	if err := store.FinishApply("prod", outcome); err != nil {
		t.Fatalf("FinishApply: %v", err)
	}
	if err := store.FinishApply("prod", outcome); err != nil {
		t.Fatalf("re-reporting the SAME outcome is a retry and must succeed: %v", err)
	}

	// ...and refuses a CONTRADICTORY one: an apply ended once.
	conflicting := outcome
	conflicting.Status = release.ApplyFailed
	if err := store.FinishApply("prod", conflicting); err == nil {
		t.Error("an apply that already reported succeeded must not also report failed")
	}
}

// A session is PRESENCE (owner decision O-8): repeated heartbeats for one
// (env, host, worktree) must REPLACE the row, not accumulate. A heartbeat
// every 60s that appended would grow without bound while saying nothing —
// the useful fact is the current state of each session.
func TestSessionReporter_CompactsHeartbeats(t *testing.T) {
	dir := newLedgerTestProject(t, "session-project")
	var reporter sessionReporter = testRecordStore(t, dir)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Second)

	session := func(lastSeen time.Time) release.LocalSession {
		return release.LocalSession{
			ID:         "sess-1",
			Env:        "dev",
			Worktree:   release.Worktree{Key: "", Label: "main", Host: "host-abc"},
			StartedAt:  start,
			LastSeenAt: lastSeen,
		}
	}

	for i := range 5 {
		if err := reporter.ReportSession(ctx, session(start.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
	}

	sessions, err := testStore(t, dir).SessionsFor("dev")
	if err != nil {
		t.Fatalf("SessionsFor: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("5 heartbeats of one session must compact to 1 row, got %d", len(sessions))
	}
	if !sessions[0].LastSeenAt.Equal(start.Add(4 * time.Minute)) {
		t.Errorf("the surviving row must hold the NEWEST last-seen, got %s", sessions[0].LastSeenAt)
	}

	// A different worktree of the same env is a DIFFERENT session: the whole
	// point of presence is that a LOCAL env has no single "what runs".
	other := session(start)
	other.ID = "sess-2"
	other.Worktree = release.Worktree{Key: "wt-2", Label: "feat-x", Host: "host-abc"}
	if err := reporter.ReportSession(ctx, other); err != nil {
		t.Fatalf("second worktree: %v", err)
	}
	sessions, err = testStore(t, dir).SessionsFor("dev")
	if err != nil {
		t.Fatalf("SessionsFor: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("two worktrees of one env are two sessions, got %d", len(sessions))
	}
}

// Gates are the POST-promote evidence the retired file ledger could not hold
// at all: a promotion line is already written when the wait or the smoke
// test finishes, and an append-only line cannot be edited
// (release.Promotion documents "a file ledger leaves it empty"). A per-env
// gates file fixes that without rewriting history.
func TestMachineLedger_HoldsPostPromoteGates(t *testing.T) {
	dir := newLedgerTestProject(t, "gates-project")
	store := testStore(t, dir)

	promoted := testPromote(t, dir, release.Promotion{
		Env:      "prod",
		Release:  "v1.0.0",
		Kind:     release.KindPromote,
		Resolved: map[string]string{"api": sha("a")},
	})

	gates := []release.Gate{
		{Name: "smoke", Status: release.GateStatusPassed},
		{Name: "rollout", Status: release.GateStatusPassed},
	}
	if err := store.AppendGates("prod", promoted.ID, gates); err != nil {
		t.Fatalf("AppendGates: %v", err)
	}

	got, err := store.Gates("prod", promoted.ID)
	if err != nil {
		t.Fatalf("Gates: %v", err)
	}
	if len(got) != 2 || got[0].Name != "smoke" || got[1].Status != release.GateStatusPassed {
		t.Fatalf("the evidence must read back joined to its promotion, got %+v", got)
	}

	// And evidence for a different promotion does not bleed in.
	if other, err := store.Gates("prod", "some-other-promotion"); err != nil || len(other) != 0 {
		t.Fatalf("gates must be joined by promotion id, got (%+v, %v)", other, err)
	}
}
