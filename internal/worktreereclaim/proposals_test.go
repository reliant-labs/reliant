// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeReport writes a sweep report with the given entries to dir.
func writeReport(t *testing.T, dir string, entries ...SweepEntry) {
	t.Helper()
	b, err := json.Marshal(SweepReport{Version: 1, Mode: SweepOn, FinishedAt: time.Now().UTC(), Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(filepath.Join(dir, reportFile), b); err != nil {
		t.Fatal(err)
	}
}

func proposal(key, fp string, proposedAt time.Time) SweepEntry {
	return SweepEntry{Key: key, Fingerprint: fp, Path: "/tmp/" + key, Class: ClassNeedsApproval, Outcome: OutcomeAwaiting,
		Reason: ReasonDirty, Detail: "1 changed file(s): a.txt", Work: " M a.txt", ProposedAt: proposedAt}
}

// The heartbeat's proposals are the report's pending needs-approval entries,
// oldest proposal first, with their note and recorded decision; reclaimed and
// held entries are not proposals.
func TestPendingProposalsCarryNotesAndDecisions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now().UTC()
	reclaimed := proposal("p-gone", "fp-gone", now)
	reclaimed.Outcome = OutcomeReclaimed
	held := SweepEntry{Key: "p-held", Class: ClassHeld, Outcome: OutcomeSweepHeld}
	writeReport(t, dir, proposal("p-new", "fp-new", now), proposal("p-old", "fp-old", now.Add(-time.Hour)), reclaimed, held)
	ctx := context.Background()

	if _, err := RecordNotes(ctx, dir, []Note{{Key: "p-old", Fingerprint: "fp-old", Text: "  an abandoned experiment  "}}, "agent"); err != nil {
		t.Fatal(err)
	}
	res, err := RecordDecisionsFor(ctx, dir, []DecisionRequest{{Key: "p-new", Fingerprint: "fp-new", Decision: DecisionDecline}}, "inbox:u1")
	if err != nil || len(res) != 1 || !res[0].Recorded {
		t.Fatalf("decision = %+v, %v", res, err)
	}
	set, err := PendingProposals(dir)
	if err != nil {
		t.Fatal(err)
	}
	if set.Total != 2 || len(set.Proposals) != 2 || set.Proposals[0].Key != "p-old" || set.Proposals[1].Key != "p-new" {
		t.Fatalf("proposals %+v, want p-old then p-new", set.Proposals)
	}
	if got := set.Proposals[0]; got.Note != "an abandoned experiment" || got.NoteAt.IsZero() || got.Decision != "" {
		t.Fatalf("p-old %+v", got)
	}
	if got := set.Proposals[1]; got.Decision != DecisionDecline || got.Note != "" {
		t.Fatalf("p-new %+v", got)
	}
}

// A decision is recorded only for the state the person was shown: a stale
// fingerprint (the worktree changed) or a key the report no longer proposes
// records nothing, and the sweep then has no approval to act on.
func TestRecordDecisionsForBindsToTheFingerprintShown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeReport(t, dir, proposal("p-a", "fp-a2", time.Now()))
	res, err := RecordDecisionsFor(context.Background(), dir, []DecisionRequest{
		{Key: "p-a", Fingerprint: "fp-a1", Decision: DecisionApprove},
		{Key: "p-zzz", Fingerprint: "fp", Decision: DecisionApprove},
		{Key: "p-a", Fingerprint: "fp-a2", Decision: "maybe"},
	}, "inbox:u1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"changed", "unknown", "invalid"}
	for i, r := range res {
		if r.Recorded || r.Reason != want[i] {
			t.Fatalf("result %d = %+v, want not recorded (%s)", i, r, want[i])
		}
	}
	if PendingApprovals(dir, time.Time{}) {
		t.Fatal("an approval was recorded for a state nobody was shown")
	}
	a, _ := readApprovals(dir)
	if len(a.Approvals) != 0 {
		t.Fatalf("approvals.json = %+v", a)
	}
}

// A note about a state that has changed is refused; notes about states the
// report no longer lists are dropped when the next note is written.
func TestRecordNotesRefusesStaleStates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()
	writeReport(t, dir, proposal("p-a", "fp-1", time.Now()), proposal("p-b", "fp-b", time.Now()))
	if _, err := RecordNotes(ctx, dir, []Note{{Key: "p-a", Fingerprint: "fp-1", Text: "first"}}, "agent"); err != nil {
		t.Fatal(err)
	}
	writeReport(t, dir, proposal("p-a", "fp-2", time.Now()), proposal("p-b", "fp-b", time.Now()))
	got, err := RecordNotes(ctx, dir, []Note{
		{Key: "p-a", Fingerprint: "fp-1", Text: "about the old state"},
		{Key: "p-b", Fingerprint: "fp-b", Text: strings.Repeat("é", 1000)},
	}, "agent")
	if err == nil || !strings.Contains(err.Error(), "p-a") {
		t.Fatalf("stale note: err = %v, want it named", err)
	}
	if len(got) != 1 || got[0].Key != "p-b" || len(got[0].Text) > maxNoteBytes || !strings.HasSuffix(got[0].Text, "…") {
		t.Fatalf("recorded %+v", got)
	}
	notes, _ := readNotes(dir)
	if len(notes.Notes) != 1 || notes.Notes[0].Key != "p-b" {
		t.Fatalf("notes.json %+v: the note about fp-1 should be gone", notes.Notes)
	}
}

// When a proposal's state stays the same from pass to pass, it keeps when it
// was first proposed; a new state is a new proposal.
func TestCarryProposedAtKeepsTheFirstProposal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	writeReport(t, dir, proposal("p-a", "fp-1", first), proposal("p-b", "fp-b", first))
	now := time.Now().UTC()
	next := []SweepEntry{proposal("p-a", "fp-1", time.Time{}), proposal("p-b", "fp-b2", time.Time{}), {Key: "p-c", Class: ClassHeld}}
	carryProposedAt(dir, next, now)
	if !next[0].ProposedAt.Equal(first) || !next[1].ProposedAt.Equal(now) || !next[2].ProposedAt.IsZero() {
		t.Fatalf("proposed at: %v %v %v", next[0].ProposedAt, next[1].ProposedAt, next[2].ProposedAt)
	}
}
