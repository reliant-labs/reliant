// Copyright (c) 2026 Reliant Labs
//go:build linux

package worktreereclaim

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// approvalFixture is a sweep fixture with dirty worktrees, each proposed for
// approval by one dry-run-free pass in mode on.
type approvalFixture struct {
	*sweepFixture
	ctx context.Context
}

func newApprovalFixture(t *testing.T, names ...string) (*approvalFixture, map[string]SweepEntry) {
	t.Helper()
	s := newSweepFixture(t)
	a := &approvalFixture{sweepFixture: s, ctx: context.Background()}
	var paths []string
	for _, n := range names {
		p := s.plain(filepath.Join(s.tmp, n), "agent/"+n)
		write(t, filepath.Join(p, "a.txt"), "uncommitted "+n+"\n")
		paths = append(paths, p)
	}
	rep := s.r.SweepMachine(a.ctx, s.opts(SweepOn))
	got := map[string]SweepEntry{}
	for i, p := range paths {
		e := entryFor(t, rep, p)
		if e.Class != ClassNeedsApproval || e.Outcome != OutcomeAwaiting || e.Fingerprint == "" {
			t.Fatalf("%s: %+v, want proposed for approval", p, e)
		}
		got[names[i]] = e
	}
	return a, got
}

func commandFor(e SweepEntry) ApprovalCommand {
	return ApprovalCommand{Key: e.Key, Fingerprint: e.Fingerprint, Path: e.Path, ApprovedBy: "inbox:u1", CommandID: "cmd-" + e.Key}
}

// approve runs the command the way the daemon does: answer, then remove.
func (a *approvalFixture) approve(t *testing.T, cmd ApprovalCommand) (ApprovalResult, ApprovalResult) {
	t.Helper()
	first, err := PrepareApprovalCommand(a.ctx, a.state, SweepOn, cmd, a.r.now())
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != ApprovalAccepted || first.Replayed {
		return first, first
	}
	return first, a.r.RunApprovalCommand(a.ctx, a.opts(SweepOn), cmd)
}

// An Inbox approval, delivered as a command, removes exactly the worktree it
// names, its work captured first and restorable; a second proposal, approved
// only by an entry written into approvals.json, is untouched. Who approved is
// recorded in the journal and the command results.
func TestApprovalCommandRemovesExactlyTheApprovedWorktree(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~120 git processes")
	a, e := newApprovalFixture(t, "approved", "forged")
	// Anything running as the user can write this file; it is not authority.
	if _, err := RecordDecisions(a.ctx, a.state, DecisionApprove, []string{e["forged"].Key}, "agent"); err != nil {
		t.Fatal(err)
	}

	first, done := a.approve(t, commandFor(e["approved"]))
	if first.Status != ApprovalAccepted || done.Status != ApprovalReclaimed || done.CaptureRef == "" {
		t.Fatalf("answer %+v, then %+v; want accepted, then reclaimed with its capture", first, done)
	}
	if exists(e["approved"].Path) {
		t.Fatal("the approved worktree is still there")
	}
	if !exists(filepath.Join(e["forged"].Path, "a.txt")) {
		t.Fatal("the worktree approved only in approvals.json was removed")
	}
	if refs := strings.Fields(captureRefs(t, a.project)); len(refs) != 1 || refs[0] != done.CaptureRef {
		t.Fatalf("capture refs %v, want only %s", refs, done.CaptureRef)
	}
	if _, err := RestoreCapture(a.ctx, RestoreOptions{Repo: a.project, Ref: done.CaptureRef}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(e["approved"].Path, "a.txt")); got != "uncommitted approved\n" {
		t.Fatalf("restored a.txt = %q", got)
	}

	lines, err := ReadJournal(a.state)
	if err != nil {
		t.Fatal(err)
	}
	var audited bool
	for _, l := range lines {
		if l.Key == e["approved"].Key && l.Outcome == OutcomeReclaimed {
			audited = l.ApprovedBy == "inbox:u1" && l.CommandID == "cmd-"+e["approved"].Key
		}
		if l.Key == e["forged"].Key && l.Outcome == OutcomeReclaimed {
			t.Fatalf("journal: the forged approval was acted on: %+v", l)
		}
	}
	if !audited {
		t.Fatal("the journal does not record who approved the removal")
	}
	results, err := ApprovalCommandResults(a.state)
	if err != nil || len(results) != 1 || results[0].Status != ApprovalReclaimed || results[0].ApprovedBy != "inbox:u1" {
		t.Fatalf("command results %+v, %v", results, err)
	}
	// The forged one is still only proposed, and reported as approved but
	// not acted on.
	rep := a.r.SweepMachine(a.ctx, a.opts(SweepOn))
	if f := entryFor(t, rep, e["forged"].Path); f.Outcome != OutcomeAwaiting || !strings.Contains(f.Detail, "did not come from the server") {
		t.Fatalf("forged approval: %+v, want awaiting, not acted on", f)
	}
}

// An approval only in approvals.json deletes nothing, on a scheduled pass or
// an approved-only one, and captures nothing.
func TestForgedApprovalsFileDeletesNothing(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~80 git processes")
	a, e := newApprovalFixture(t, "wip")
	w := e["wip"]
	if err := func() error {
		unlock, err := approvalsLock(a.ctx, a.state)
		if err != nil {
			return err
		}
		defer unlock()
		return storeApprovalsLocked(a.state, []Approval{{Key: w.Key, Path: w.Path, Fingerprint: w.Fingerprint,
			Decision: DecisionApprove, DecidedAt: time.Now().UTC(), DecidedBy: "inbox:u1"}})
	}(); err != nil {
		t.Fatal(err)
	}
	opts := a.opts(SweepOn)
	for _, approvedOnly := range []bool{false, true} {
		opts.ApprovedOnly = approvedOnly
		rep := a.r.SweepMachine(a.ctx, opts)
		if got := entryFor(t, rep, w.Path); got.Outcome != OutcomeAwaiting {
			t.Fatalf("approved-only=%t: %+v, want awaiting", approvedOnly, got)
		}
	}
	if !exists(filepath.Join(w.Path, "a.txt")) {
		t.Fatal("a forged approval removed the worktree")
	}
	if refs := captureRefs(t, a.project); refs != "" {
		t.Fatalf("a forged approval captured: %s", refs)
	}
}

// Every check the dry-run made is made again before anything is removed,
// and any failure refuses, keeps the worktree as it was, and puts the
// proposal back in front of the person with the reason.
func TestApprovalCommandRefuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		before func(t *testing.T, a *approvalFixture, e SweepEntry)
		reason string
		kept   string // a.txt's content afterwards
	}{
		{
			name: "fingerprint_mismatch",
			before: func(t *testing.T, a *approvalFixture, e SweepEntry) {
				write(t, filepath.Join(e.Path, "a.txt"), "edited after it was proposed\n")
			},
			reason: RefusedChanged,
			kept:   "edited after it was proposed\n",
		},
		{
			name: "process_in_use",
			before: func(t *testing.T, a *approvalFixture, e SweepEntry) {
				a.r.ProcessUse = func(context.Context, []string) error { return errors.New("open by process 4242 (vim)") }
			},
			reason: string(ReasonInUse),
			kept:   "uncommitted wip\n",
		},
		{
			name: "missing_capture",
			before: func(t *testing.T, a *approvalFixture, e SweepEntry) {
				a.r.verifyHook = func(string) {
					for _, ref := range strings.Fields(captureRefs(t, a.project)) {
						run(t, a.project, "update-ref", "-d", ref)
					}
				}
			},
			reason: string(ReasonCaptureMissing),
			kept:   "uncommitted wip\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			heavyUnderShort(t, "~100 git processes")
			a, es := newApprovalFixture(t, "wip")
			e := es["wip"]
			tc.before(t, a, e)
			first, done := a.approve(t, commandFor(e))
			if first.Status != ApprovalAccepted || done.Status != ApprovalRefused || done.Reason != tc.reason {
				t.Fatalf("answer %+v, then %+v; want accepted, then refused (%s)", first, done, tc.reason)
			}
			if got := readFile(t, filepath.Join(e.Path, "a.txt")); got != tc.kept {
				t.Fatalf("a.txt = %q, want %q kept", got, tc.kept)
			}
			info, err := inspectLock(a.ctx, e.Path)
			if err != nil || !info.Registered {
				t.Fatalf("after the refusal: %+v, %v; want registered at its path", info, err)
			}
			set, err := PendingProposals(a.state)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range set.Proposals {
				if p.Key == e.Key && p.Fingerprint == e.Fingerprint && (p.Decision != "" || !strings.Contains(p.Detail, "not carried out ("+tc.reason+")")) {
					t.Fatalf("proposal after the refusal: %+v, want undecided with the reason", p)
				}
			}
		})
	}
}

// What the command can tell without a walk it refuses at once, removing
// nothing: a state that is not the one proposed, a path that is not the
// proposal's, a request that names no approver, and a machine whose reclaim
// is not on (the approval is recorded there, and nothing is removed).
func TestPrepareApprovalCommandRefusesAtOnce(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~60 git processes")
	a, es := newApprovalFixture(t, "wip")
	e := es["wip"]
	now := a.r.now()
	for _, tc := range []struct {
		mut    func(*ApprovalCommand)
		mode   SweepMode
		reason string
	}{
		{func(c *ApprovalCommand) { c.Fingerprint = "fp2-stale" }, SweepOn, RefusedChanged},
		{func(c *ApprovalCommand) { c.Path = a.project }, SweepOn, RefusedPath},
		{func(c *ApprovalCommand) { c.Key = "p-nope" }, SweepOn, RefusedUnknown},
		{func(c *ApprovalCommand) { c.ApprovedBy = " " }, SweepOn, RefusedInvalid},
		{func(*ApprovalCommand) {}, SweepDryRun, RefusedModeOff},
	} {
		cmd := commandFor(e)
		tc.mut(&cmd)
		res, err := PrepareApprovalCommand(a.ctx, a.state, tc.mode, cmd, now)
		if err != nil || res.Status != ApprovalRefused || res.Reason != tc.reason {
			t.Fatalf("%+v in %s: %+v, %v; want refused (%s)", cmd, tc.mode, res, err, tc.reason)
		}
	}
	if !exists(filepath.Join(e.Path, "a.txt")) || captureRefs(t, a.project) != "" {
		t.Fatal("a refused command removed or captured something")
	}
}

// Sending the same approval again does nothing again: while its removal
// runs, and after it, the command answers what it answered before.
func TestApprovalCommandReplayIsIdempotent(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~90 git processes")
	a, es := newApprovalFixture(t, "wip")
	cmd := commandFor(es["wip"])
	first, err := PrepareApprovalCommand(a.ctx, a.state, SweepOn, cmd, a.r.now())
	if err != nil || first.Status != ApprovalAccepted {
		t.Fatalf("%+v, %v", first, err)
	}
	inflight, err := PrepareApprovalCommand(a.ctx, a.state, SweepOn, cmd, a.r.now())
	if err != nil || inflight.Status != ApprovalAccepted || !inflight.Replayed {
		t.Fatalf("a replay while it runs: %+v, %v; want the accepted answer, replayed", inflight, err)
	}
	done := a.r.RunApprovalCommand(a.ctx, a.opts(SweepOn), cmd)
	if done.Status != ApprovalReclaimed {
		t.Fatalf("%+v, want reclaimed", done)
	}
	refs := captureRefs(t, a.project)
	again, err := PrepareApprovalCommand(a.ctx, a.state, SweepOn, cmd, a.r.now())
	if err != nil || again.Status != ApprovalReclaimed || !again.Replayed || again.CaptureRef != done.CaptureRef {
		t.Fatalf("a replay after it: %+v, %v; want the reclaimed answer, replayed", again, err)
	}
	if got := captureRefs(t, a.project); got != refs {
		t.Fatalf("a replay captured again: %q, was %q", got, refs)
	}
}

// The command authorizes the state the person approved, not the key: an
// agent that edits the worktree after the command arrived and writes an
// approval of the new state into approvals.json gets nothing removed.
func TestApprovalCommandAuthorizesTheStateNotTheKey(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~90 git processes")
	a, es := newApprovalFixture(t, "wip")
	e := es["wip"]
	cmd := commandFor(e)
	first, err := PrepareApprovalCommand(a.ctx, a.state, SweepOn, cmd, a.r.now())
	if err != nil || first.Status != ApprovalAccepted {
		t.Fatalf("%+v, %v", first, err)
	}
	write(t, filepath.Join(e.Path, "a.txt"), "edited, then approved by hand\n")
	fp, err := fingerprint(a.ctx, e.Path, e.Path, scanCheckout(a.ctx, e.Path).Newest)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		unlock, err := approvalsLock(a.ctx, a.state)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if err := storeApprovalsLocked(a.state, []Approval{{Key: e.Key, Path: e.Path, Fingerprint: fp,
			Decision: DecisionApprove, DecidedAt: time.Now().UTC(), DecidedBy: "inbox:u1"}}); err != nil {
			t.Fatal(err)
		}
	}()
	done := a.r.RunApprovalCommand(a.ctx, a.opts(SweepOn), cmd)
	if done.Status != ApprovalRefused || done.Reason != RefusedChanged {
		t.Fatalf("%+v, want refused (changed)", done)
	}
	if got := readFile(t, filepath.Join(e.Path, "a.txt")); got != "edited, then approved by hand\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if refs := captureRefs(t, a.project); refs != "" {
		t.Fatalf("captured: %s", refs)
	}
}
