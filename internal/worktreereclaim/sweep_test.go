// Copyright (c) 2026 Reliant Labs
//go:build linux

package worktreereclaim

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sweepFixture is a fixture whose worktrees all look ten days idle, swept
// over the worktree root and a temp dir beside it.
type sweepFixture struct {
	*fixture
	tmp   string // an agent's scratch root
	state string
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	f := newFixture(t)
	f.r.Now = func() time.Time { return time.Now().Add(10 * 24 * time.Hour) }
	s := &sweepFixture{fixture: f, tmp: filepath.Join(filepath.Dir(f.project), "tmp"), state: t.TempDir()}
	if err := os.MkdirAll(s.tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *sweepFixture) opts(mode SweepMode) SweepOptions {
	return SweepOptions{Mode: mode, StateDir: s.state, Trigger: "test",
		Roots: []SweepRoot{{Path: s.root, Depth: 4}, {Path: s.tmp, Depth: 3}}}
}

// plain adds an unlocked worktree, the way an agent's `git worktree add`
// does, on a new branch at main: clean, its HEAD on the remote.
func (s *sweepFixture) plain(path, branch string) string {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatal(err)
	}
	run(s.t, s.project, "worktree", "add", "-q", "-b", branch, path, "main")
	return path
}

func entryFor(t *testing.T, rep SweepReport, path string) SweepEntry {
	t.Helper()
	for _, e := range rep.Entries {
		if samePath(e.Path, path) {
			return e
		}
	}
	t.Fatalf("no entry for %s in %+v", path, rep.Entries)
	return SweepEntry{}
}

func captureRefs(t *testing.T, repo string) string {
	t.Helper()
	return run(t, repo, "for-each-ref", "--format=%(refname)", strings.TrimSuffix(CaptureRefRoot, "/"))
}

// Discovery finds every linked worktree from git, whoever made it: one an
// agent added in a scratch dir, one outside every root (found through its
// repository), and a Reliant workspace's; never the main checkout.
func TestSweepDiscoversUnregisteredWorktrees(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~60 git processes")
	s := newSweepFixture(t)
	scratch := s.plain(filepath.Join(s.tmp, "agent", "wt"), "agent/one")
	outside := s.plain(filepath.Join(filepath.Dir(s.project), "elsewhere", "deep", "down", "wt"), "agent/two")
	reliant := s.worktree("row-7", "feat/row").Path
	foreign := s.plain(filepath.Join(s.tmp, "kept"), "agent/kept")
	run(t, s.project, "worktree", "lock", "--reason", "a person wants this", foreign)

	rep := s.r.SweepMachine(context.Background(), s.opts(SweepDryRun))
	for _, p := range []string{scratch, outside} {
		if e := entryFor(t, rep, p); e.Owner != "unregistered" || e.Key != CaptureKey(p) {
			t.Fatalf("%s: %+v, want an unregistered worktree keyed by its path", p, e)
		}
	}
	if e := entryFor(t, rep, reliant); e.Owner != "reliant" || e.RowID != "row-7" || e.Key != "row-7" {
		t.Fatalf("reliant workspace: %+v", e)
	}
	if e := entryFor(t, rep, foreign); e.Class != ClassHeld || e.Reason != ReasonLocked {
		t.Fatalf("a worktree someone else locked: %+v, want held/locked", e)
	}
	for _, e := range rep.Entries {
		if samePath(e.Path, s.project) {
			t.Fatalf("the main checkout is a candidate: %+v", e)
		}
	}
	if len(rep.Entries) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(rep.Entries), rep.Entries)
	}
}

// Dry-run reports what it would reclaim, with the bytes, and writes the
// report and the journal; it captures nothing and deletes nothing.
func TestSweepDryRunReportsAndNeverDeletes(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~50 git processes")
	s := newSweepFixture(t)
	clean := s.plain(filepath.Join(s.tmp, "clean"), "agent/clean")
	write(t, filepath.Join(clean, "notes.txt"), "work in progress\n")
	run(t, clean, "add", "notes.txt")
	run(t, clean, "commit", "-q", "-m", "work")
	run(t, clean, "push", "-q", "origin", "agent/clean")
	dirty := s.plain(filepath.Join(s.tmp, "dirty"), "agent/dirty")
	write(t, filepath.Join(dirty, "a.txt"), "uncommitted\n")

	rep := s.r.SweepMachine(context.Background(), s.opts(SweepDryRun))
	e := entryFor(t, rep, clean)
	if e.Class != ClassAutoSafe || e.Outcome != OutcomeWouldReclaim || e.Bytes <= 0 {
		t.Fatalf("clean pushed worktree: %+v, want auto-safe, would reclaim, with its bytes", e)
	}
	d := entryFor(t, rep, dirty)
	if d.Class != ClassNeedsApproval || d.Outcome != OutcomeAwaiting || d.Fingerprint == "" || !strings.Contains(d.Work, "a.txt") {
		t.Fatalf("dirty worktree: %+v, want needs approval with its work named", d)
	}
	if rep.Totals.AutoSafe != 1 || rep.Totals.NeedsApproval != 1 || rep.Totals.AutoSafeBytes != e.Bytes {
		t.Fatalf("totals %+v", rep.Totals)
	}
	for _, p := range []string{clean, dirty} {
		if !exists(filepath.Join(p, ".git")) {
			t.Fatalf("dry-run deleted %s", p)
		}
	}
	if refs := captureRefs(t, s.project); refs != "" {
		t.Fatalf("dry-run captured: %s", refs)
	}
	saved, err := ReadSweepReport(s.state)
	if err != nil || len(saved.Entries) != len(rep.Entries) || saved.Mode != SweepDryRun {
		t.Fatalf("report on disk: %+v, %v", saved, err)
	}
	lines, err := ReadJournal(s.state)
	if err != nil || len(lines) != len(rep.Entries) {
		t.Fatalf("journal: %d lines, %v", len(lines), err)
	}
}

// In mode on an auto-safe worktree is captured, then deleted; its branch
// stays, a tombstone says how to restore it, and the restore works.
func TestSweepOnReclaimsAutoSafeAfterCapture(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~60 git processes")
	s := newSweepFixture(t)
	ignore(t, s.fixture, "node_modules/\n")
	clean := s.plain(filepath.Join(s.tmp, "done"), "agent/done")
	write(t, filepath.Join(clean, "node_modules", "pkg", "index.js"), "built\n")

	rep := s.r.SweepMachine(context.Background(), s.opts(SweepOn))
	e := entryFor(t, rep, clean)
	if e.Outcome != OutcomeReclaimed || e.CaptureRef == "" || e.FreedBytes <= 0 {
		t.Fatalf("got %+v, want reclaimed with a capture", e)
	}
	if exists(clean) {
		t.Fatal("the checkout is still there")
	}
	if out := run(t, s.project, "worktree", "list", "--porcelain"); strings.Contains(out, clean) {
		t.Fatalf("registration kept:\n%s", out)
	}
	run(t, s.project, "rev-parse", "--verify", "refs/heads/agent/done")
	var ts Tombstone
	if err := json.Unmarshal([]byte(readFile(t, clean+TombstoneSuffix)), &ts); err != nil || ts.Ref != e.CaptureRef {
		t.Fatalf("tombstone %+v, %v", ts, err)
	}
	if _, err := RestoreCapture(context.Background(), RestoreOptions{Repo: ts.CommonDir, Ref: ts.Ref}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(clean, "a.txt")); got != "one\n" {
		t.Fatalf("restored a.txt = %q", got)
	}
}

// An edit behind skip-worktree or assume-unchanged is invisible to `git add
// -A`, so a capture would silently omit it: such a checkout is never
// captured, a flag set after a capture fails its verification, and the sweep
// holds the checkout instead of proposing it.
func TestHiddenIndexEntriesAreNeverCaptured(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~60 git processes")
	for _, flag := range []string{"--skip-worktree", "--assume-unchanged"} {
		t.Run(flag, func(t *testing.T) {
			t.Parallel()
			s := newSweepFixture(t)
			ctx := context.Background()
			path := s.plain(filepath.Join(s.tmp, "hidden"), "agent/hidden")
			write(t, filepath.Join(path, "untracked.txt"), "work only this checkout holds\n")
			c, err := captureCheckout(ctx, path, CaptureKey(path), CaptureMeta{Path: path}, CaptureLimits{})
			if err != nil {
				t.Fatal(err)
			}
			run(t, path, "update-index", flag, "a.txt")
			write(t, filepath.Join(path, "a.txt"), "a local override\n")
			if err := verifyCapture(ctx, path, c); !errors.Is(err, errCaptureMismatch) {
				t.Fatalf("a flag set after the capture: verify = %v, want a mismatch", err)
			}
			if _, err := captureCheckout(ctx, path, CaptureKey(path), CaptureMeta{Path: path}, CaptureLimits{}); !errors.Is(err, errHiddenEntries) {
				t.Fatalf("capture = %v, want errHiddenEntries", err)
			}
			rep := s.r.SweepMachine(ctx, s.opts(SweepOn))
			if e := entryFor(t, rep, path); e.Class != ClassHeld || e.Reason != ReasonDirty || !strings.Contains(e.Detail, "hidden") {
				t.Fatalf("%+v, want held for its hidden entries", e)
			}
			if got := readFile(t, filepath.Join(path, "a.txt")); got != "a local override\n" {
				t.Fatalf("a.txt = %q, want the override kept", got)
			}
		})
	}
}

// A worktree with work only it holds is never deleted without a person's
// approval of that exact state: on its own it is only proposed; approved, it
// is captured and reclaimed; changed after the approval, it is proposed
// again.
func TestSweepNeverAutoDeletesNeedsApproval(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~90 git processes")
	s := newSweepFixture(t)
	ctx := context.Background()
	dirty := s.plain(filepath.Join(s.tmp, "wip"), "agent/wip")
	write(t, filepath.Join(dirty, "a.txt"), "uncommitted\n")
	other := s.plain(filepath.Join(s.tmp, "wip2"), "agent/wip2")
	write(t, filepath.Join(other, "draft.md"), "draft\n")

	for range 2 {
		rep := s.r.SweepMachine(ctx, s.opts(SweepOn))
		for _, p := range []string{dirty, other} {
			if e := entryFor(t, rep, p); e.Class != ClassNeedsApproval || e.Outcome != OutcomeAwaiting {
				t.Fatalf("%s: %+v, want awaiting approval", p, e)
			}
			if !exists(filepath.Join(p, ".git")) {
				t.Fatalf("%s was deleted without approval", p)
			}
		}
	}
	if refs := captureRefs(t, s.project); refs != "" {
		t.Fatalf("captured without approval: %s", refs)
	}
	rep, _ := ReadSweepReport(s.state)
	keys := []string{entryFor(t, rep, dirty).Key, entryFor(t, rep, other).Key}
	if _, err := RecordDecisions(ctx, s.state, DecisionApprove, keys, "test"); err != nil {
		t.Fatal(err)
	}
	if !PendingApprovals(s.state, time.Time{}) {
		t.Fatal("approvals are not pending")
	}
	// The second one changes after it was approved.
	write(t, filepath.Join(other, "draft.md"), "draft, edited after approval\n")

	opts := s.opts(SweepOn)
	opts.ApprovedOnly = true
	// An approval nothing verifies as a person's (the daemon's case: any
	// process running as the user can write approvals.json) deletes nothing.
	unverified := s.r.SweepMachine(ctx, opts)
	if e := entryFor(t, unverified, dirty); e.Outcome != OutcomeAwaiting || !exists(filepath.Join(dirty, "a.txt")) {
		t.Fatalf("unverified approval: %+v, want still awaiting and kept", e)
	}
	if refs := captureRefs(t, s.project); refs != "" {
		t.Fatalf("captured on an unverified approval: %s", refs)
	}
	opts.ApprovalsVerified = true
	pass := s.r.SweepMachine(ctx, opts)
	e := entryFor(t, pass, dirty)
	if e.Outcome != OutcomeReclaimed || e.CaptureRef == "" || exists(dirty) {
		t.Fatalf("approved worktree: %+v, want reclaimed", e)
	}
	if o := entryFor(t, pass, other); o.Outcome == OutcomeReclaimed || !exists(filepath.Join(other, "draft.md")) {
		t.Fatalf("a worktree that changed after its approval was reclaimed: %+v", o)
	}
	if _, err := RestoreCapture(ctx, RestoreOptions{Repo: s.project, Ref: e.CaptureRef}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dirty, "a.txt")); got != "uncommitted\n" {
		t.Fatalf("restored a.txt = %q, want the uncommitted edit", got)
	}
}

// The capture is verified against the parked checkout immediately before
// deletion; a change in between puts the checkout back and deletes nothing.
func TestSweepVerifyMismatchAbortsDeletion(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~50 git processes")
	s := newSweepFixture(t)
	clean := s.plain(filepath.Join(s.tmp, "racy"), "agent/racy")
	s.r.verifyHook = func(parked string) { write(t, filepath.Join(parked, "late.txt"), "written while parked\n") }

	rep := s.r.SweepMachine(context.Background(), s.opts(SweepOn))
	e := entryFor(t, rep, clean)
	if e.Outcome == OutcomeReclaimed || e.Reason != ReasonRecentlyActive || !strings.Contains(e.Detail, "changed after it was captured") {
		t.Fatalf("got %+v, want held: changed after capture", e)
	}
	if got := readFile(t, filepath.Join(clean, "late.txt")); got != "written while parked\n" {
		t.Fatalf("the late write was lost: %q", got)
	}
	info, err := inspectLock(context.Background(), clean)
	if err != nil || !info.Registered || info.Locked {
		t.Fatalf("after the abort: %+v, %v; want registered at its path and unlocked as before", info, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(s.tmp, reclaimQuarantineDir, "*", "*")); len(matches) != 0 {
		t.Fatalf("quarantine left behind: %v", matches)
	}
}

// A squash-merged branch whose PR branch is gone is landed work: the sweep
// finds its patch on the remote's default branch, from a patch table of the
// base built once for every worktree of the repository, even when the base
// holds commits with no patch at all (an --allow-empty commit).
func TestSweepRecognisesSquashMergedBranchesFromOneBaseScan(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~90 git processes")
	s := newSweepFixture(t)
	empty := run(t, s.origin, "commit-tree", "-p", "main", "-m", "an empty commit", run(t, s.origin, "rev-parse", "main^{tree}"))
	run(t, s.origin, "update-ref", "refs/heads/main", empty)
	var paths []string
	for _, name := range []string{"one", "two"} {
		w := Worktree{Path: s.plain(filepath.Join(s.tmp, name), "feat/"+name)}
		s.commitIn(w, "feat/"+name, name+".txt", name+"\n", true)
		s.squashMerge("feat/" + name)
		paths = append(paths, w.Path)
	}
	run(t, s.project, "fetch", "-q", "--prune", "origin")
	run(t, s.project, "remote", "set-head", "origin", "main")
	unmerged := Worktree{Path: s.plain(filepath.Join(s.tmp, "three"), "feat/three")}
	s.commitIn(unmerged, "feat/three", "three.txt", "three\n", false)

	s.r.PRMerged = nil
	rep := s.r.SweepMachine(context.Background(), s.opts(SweepDryRun))
	for _, p := range paths {
		if e := entryFor(t, rep, p); e.Class != ClassAutoSafe || e.Base != "main" {
			t.Fatalf("squash-merged %s: %+v, want auto-safe against main", p, e)
		}
	}
	if e := entryFor(t, rep, unmerged.Path); e.Class != ClassNeedsApproval || e.Reason != ReasonUnpushed {
		t.Fatalf("unmerged branch: %+v, want needs approval (unpushed)", e)
	}
}

// A reclaim the daemon did not finish is undone by the next pass: the parked
// checkout goes back to its path with the lock it had.
func TestSweepRecoversAnInterruptedReclaim(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~40 git processes")
	s := newSweepFixture(t)
	ctx := context.Background()
	orig := s.worktree("row-9", "feat/crash").Path
	key := "row-9"
	q := sweepQuarantinePath(orig, key)
	if err := os.MkdirAll(filepath.Dir(q), 0o700); err != nil {
		t.Fatal(err)
	}
	for suffix, data := range map[string]string{".origin": orig, ".prelock": LockReason(key), ".capture": "{}"} {
		write(t, q+suffix, data)
	}
	run(t, s.project, "worktree", "unlock", orig)
	run(t, s.project, "worktree", "move", orig, q)
	run(t, s.project, "worktree", "lock", "--reason", reclaimingLock(key, "1"), q)

	// The parked copy is the repository's only worktree, and discovery never
	// walks into a quarantine: the daemon names the repository it knows.
	opts := s.opts(SweepDryRun)
	opts.Anchors = []string{s.project}
	rep := s.r.SweepMachine(ctx, opts)
	if e := entryFor(t, rep, orig); e.Outcome != OutcomeMovedBack {
		t.Fatalf("got %+v, want moved back", e)
	}
	info, err := inspectLock(ctx, orig)
	if err != nil || !info.Registered || info.Reason != LockReason(key) {
		t.Fatalf("after recovery: %+v, %v", info, err)
	}
	if exists(q) || exists(q+".origin") {
		t.Fatal("the quarantine is still there")
	}
}

// parkLikeACrash leaves orig the way reclaimEntry does just after its
// `git worktree move` and before its reclaiming lock: parked at a fresh
// quarantine path with the markers, unlocked.
func parkLikeACrash(t *testing.T, s *sweepFixture, orig, key, prelock string) string {
	t.Helper()
	q := sweepQuarantinePath(orig, key)
	if err := os.MkdirAll(filepath.Dir(q), 0o700); err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(reclaimMarker{Key: key, Orig: orig})
	for suffix, data := range map[string]string{".origin": orig, ".prelock": prelock, ".capture": string(marker)} {
		write(t, q+suffix, data)
	}
	if prelock != "" {
		run(t, s.project, "worktree", "unlock", orig)
	}
	run(t, s.project, "worktree", "move", orig, q)
	return q
}

// A crash between the move and the reclaiming lock leaves a parked,
// unlocked checkout with the markers. The next pass recovers it like any
// interrupted reclaim: home, with its old lock, and the quarantine gone, so
// its branch is no longer checked out at the parking path.
func TestSweepRecoversACheckoutParkedBeforeItsLock(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~40 git processes")
	s := newSweepFixture(t)
	ctx := context.Background()
	for _, tc := range []struct{ name, key, prelock, orig string }{
		{"reliant workspace", "row-11", LockReason("row-11"), s.worktree("row-11", "feat/parked").Path},
		{"agent worktree", "", "", s.plain(filepath.Join(s.tmp, "parked"), "agent/parked")},
	} {
		key := tc.key
		if key == "" {
			key = CaptureKey(tc.orig)
		}
		q := parkLikeACrash(t, s, tc.orig, key, tc.prelock)
		opts := s.opts(SweepDryRun)
		opts.Anchors = []string{s.project}
		rep := s.r.SweepMachine(ctx, opts)
		if e := entryFor(t, rep, tc.orig); e.Outcome != OutcomeMovedBack || e.Key != key {
			t.Fatalf("%s: got %+v, want moved back under key %s", tc.name, e, key)
		}
		info, err := inspectLock(ctx, tc.orig)
		if err != nil || !info.Registered || info.Locked != (tc.prelock != "") || info.Reason != tc.prelock {
			t.Fatalf("%s: after recovery: %+v, %v; want registered at its path, lock %q", tc.name, info, err, tc.prelock)
		}
		if exists(q) || exists(q+".origin") {
			t.Fatalf("%s: the quarantine is still there", tc.name)
		}
	}
}

// Recovery never touches a reclaim that is live: while another process
// holds the key's lock (as reclaimEntry does from its first check to its
// last step), a pass that finds the parked checkout holds it, moves
// nothing and keeps the markers; a process with no admission lease (a CLI
// scan) never moves one either. Once the lock is free, the daemon's pass
// recovers it.
func TestSweepRecoveryWaitsForALiveReclaim(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~40 git processes")
	s := newSweepFixture(t)
	ctx := context.Background()
	orig := s.plain(filepath.Join(s.tmp, "live"), "agent/live")
	key := CaptureKey(orig)
	q := parkLikeACrash(t, s, orig, key, "")
	run(t, s.project, "worktree", "lock", "--reason", reclaimingLock(key, "1"), q)
	opts := s.opts(SweepDryRun)
	opts.Anchors = []string{s.project}

	live := SweepEntry{Key: key}
	unlock, err := s.r.keyLock(ctx, s.state, &live)
	if err != nil {
		t.Fatal(err)
	}
	other := &Reclaimer{Root: s.r.Root, Now: s.r.Now, Acquire: s.r.Acquire, ProcessUse: s.r.ProcessUse, Cwds: s.r.Cwds}
	assertParked := func(why string) {
		t.Helper()
		info, err := inspectLock(ctx, q)
		if err != nil || !info.Registered || info.Reason != reclaimingLock(key, "1") || !exists(q+".origin") || exists(orig) {
			t.Fatalf("%s: the parked checkout was touched: %+v, %v", why, info, err)
		}
	}
	rep := other.SweepMachine(ctx, opts)
	if e := entryFor(t, rep, q); e.Outcome == OutcomeMovedBack || e.Reason != ReasonQuarantined {
		t.Fatalf("while a reclaim is live: %+v, want held", e)
	}
	assertParked("a live reclaim")
	unlock()

	cli := &Reclaimer{Root: s.r.Root, Now: s.r.Now}
	cli.SweepMachine(ctx, opts)
	assertParked("a scan with no admission lease")

	rep = other.SweepMachine(ctx, opts)
	if e := entryFor(t, rep, orig); e.Outcome != OutcomeMovedBack {
		t.Fatalf("once the lock is free: %+v, want moved back", e)
	}
}
