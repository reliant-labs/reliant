// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testNow is the fixed clock the proposal tests stamp reports with.
var testNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// writeReport writes a sweep report with the given entries to dir.
func writeReport(t *testing.T, dir string, entries ...SweepEntry) {
	t.Helper()
	b, err := json.Marshal(SweepReport{Version: 1, Mode: SweepOn, FinishedAt: testNow, Entries: entries})
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
	now := testNow
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
	writeReport(t, dir, proposal("p-a", "fp-a2", testNow))
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
	writeReport(t, dir, proposal("p-a", "fp-1", testNow), proposal("p-b", "fp-b", testNow))
	if _, err := RecordNotes(ctx, dir, []Note{{Key: "p-a", Fingerprint: "fp-1", Text: "first"}}, "agent"); err != nil {
		t.Fatal(err)
	}
	writeReport(t, dir, proposal("p-a", "fp-2", testNow), proposal("p-b", "fp-b", testNow))
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
	first := testNow.Add(-48 * time.Hour)
	writeReport(t, dir, proposal("p-a", "fp-1", first), proposal("p-b", "fp-b", first))
	now := testNow
	next := []SweepEntry{proposal("p-a", "fp-1", time.Time{}), proposal("p-b", "fp-b2", time.Time{}), {Key: "p-c", Class: ClassHeld}}
	carryProposedAt(dir, next, now)
	if !next[0].ProposedAt.Equal(first) || !next[1].ProposedAt.Equal(now) || !next[2].ProposedAt.IsZero() {
		t.Fatalf("proposed at: %v %v %v", next[0].ProposedAt, next[1].ProposedAt, next[2].ProposedAt)
	}
}

// Past the first MaxProposals, a proposal can still be noted and decided, and
// decided proposals give way to undecided ones in the capped list, so an
// undecided proposal always reaches the Inbox.
func TestProposalsPastTheCapCanBeNotedAndDecided(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()
	n := MaxProposals + 5
	entries := make([]SweepEntry, 0, n)
	for i := range n {
		entries = append(entries, proposal(fmt.Sprintf("p-%03d", i), fmt.Sprintf("fp-%03d", i), testNow.Add(time.Duration(i)*time.Minute)))
	}
	writeReport(t, dir, entries...)
	last := entries[n-1]

	if _, err := RecordNotes(ctx, dir, []Note{{Key: last.Key, Fingerprint: last.Fingerprint, Text: "past the cap"}}, "agent"); err != nil {
		t.Fatalf("note past the cap: %v", err)
	}
	var reqs []DecisionRequest
	for _, e := range entries[:MaxProposals] {
		reqs = append(reqs, DecisionRequest{Key: e.Key, Fingerprint: e.Fingerprint, Decision: DecisionDecline})
	}
	res, err := RecordDecisionsFor(ctx, dir, reqs, "inbox:u1")
	if err != nil || len(res) != MaxProposals || !res[MaxProposals-1].Recorded {
		t.Fatalf("declines = %v, %v", len(res), err)
	}
	set, err := PendingProposals(dir)
	if err != nil {
		t.Fatal(err)
	}
	if set.Total != n || len(set.Proposals) != MaxProposals {
		t.Fatalf("total %d, listed %d", set.Total, len(set.Proposals))
	}
	if got := set.Proposals[0]; got.Key != entries[MaxProposals].Key || got.Decision != "" {
		t.Fatalf("first listed %+v, want the oldest undecided", got)
	}
	if got := set.Proposals[n-MaxProposals-1]; got.Key != last.Key || got.Note != "past the cap" {
		t.Fatalf("listed %+v, want %s with its note", got, last.Key)
	}
	res, err = RecordDecisionsFor(ctx, dir, []DecisionRequest{{Key: last.Key, Fingerprint: last.Fingerprint, Decision: DecisionApprove}}, "inbox:u1")
	if err != nil || !res[0].Recorded {
		t.Fatalf("decide past the cap = %+v, %v", res, err)
	}
}

// A note batch is bounded, and notes.json keeps one note per proposal however
// many a batch carries about it.
func TestRecordNotesIsBounded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()
	writeReport(t, dir, proposal("p-a", "fp-a", testNow))
	if _, err := RecordNotes(ctx, dir, make([]Note, MaxNotes+1), "agent"); err == nil {
		t.Fatal("an oversized batch was accepted")
	}
	batch := make([]Note, 0, MaxNotes)
	for i := range MaxNotes {
		batch = append(batch, Note{Key: "p-a", Fingerprint: "fp-a", Text: fmt.Sprintf("note %d", i)})
	}
	if _, err := RecordNotes(ctx, dir, batch, "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordNotes(ctx, dir, batch, "agent"); err != nil {
		t.Fatal(err)
	}
	notes, _ := readNotes(dir)
	if len(notes.Notes) != 1 || notes.Notes[0].Text != fmt.Sprintf("note %d", MaxNotes-1) {
		t.Fatalf("notes.json %+v, want the last note alone", notes.Notes)
	}
}
