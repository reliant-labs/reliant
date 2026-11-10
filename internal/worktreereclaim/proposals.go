// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Proposals: the needs-approval worktrees of the last pass, as the person is
// asked about them (the machine's worktree-reclaim inbox item), with a
// reviewing agent's note on each and any decision already recorded.
//
// The flow, every hop of which binds to the worktree's fingerprint:
//
//	SweepMachine         writes report.json (ClassNeedsApproval entries)
//	RecordNotes          a reviewing agent's note per proposal (notes.json)
//	PendingProposals     what the daemon's heartbeat carries to the server
//	RecordDecisionsFor   a person's approve/decline, from the inbox item
//	                     (worktree.reclaim_decide) or the CLI (approvals.json)
//	SweepMachine         an approved-only pass reclaims, capture first
//
// A decision or note about a fingerprint the report no longer lists is
// refused: it was about a state of the worktree that is gone.

const (
	notesFile = "notes.json"
	// MaxProposals bounds the proposals a heartbeat carries.
	MaxProposals = 50
	// MaxNotes bounds the notes one RecordNotes call takes, and
	// MaxNotesInputBytes the JSON a caller may hand `reliant worktree note
	// --from`: an agent shell may run it, so neither is unbounded.
	MaxNotes           = MaxProposals
	MaxNotesInputBytes = 256 << 10
	// maxNoteBytes and maxSummaryBytes bound what a proposal carries.
	maxNoteBytes    = 600
	maxSummaryBytes = 600
)

// Note is a reviewing agent's assessment of one proposal.
type Note struct {
	Key         string    `json:"key"`
	Fingerprint string    `json:"fingerprint"`
	Text        string    `json:"text"`
	At          time.Time `json:"at"`
	By          string    `json:"by,omitempty"`
}

type notesDoc struct {
	Version int    `json:"version"`
	Notes   []Note `json:"notes"`
}

// Proposal is one pending needs-approval worktree, as the person sees it.
type Proposal struct {
	SweepEntry
	Note     string    `json:"note,omitempty"`
	NoteAt   time.Time `json:"note_at,omitempty"`
	Decision Decision  `json:"decision,omitempty"`
}

// ProposalSet is the pending proposals of the last pass.
type ProposalSet struct {
	Mode       SweepMode  `json:"mode"`
	FinishedAt time.Time  `json:"finished_at"`
	Proposals  []Proposal `json:"proposals"`
	// Total is how many there are; Proposals holds at most MaxProposals.
	Total int `json:"total"`
}

// pendingOutcome reports whether a needs-approval entry is still waiting on
// (or acting on) a person's decision.
func pendingOutcome(o SweepOutcome) bool {
	switch o {
	case OutcomeAwaiting, OutcomeDeclined, OutcomeWouldReclaim:
		return true
	}
	return false
}

// PendingProposals reads the last report, its approvals and its notes: every
// needs-approval worktree not yet reclaimed, the undecided ones first and
// oldest proposal first within each. Proposals holds at most MaxProposals, so
// proposals a person already decided never crowd an undecided one out of the
// Inbox; Total counts them all.
func PendingProposals(stateDir string) (ProposalSet, error) {
	set, err := allPendingProposals(stateDir)
	if err != nil {
		return ProposalSet{}, err
	}
	if len(set.Proposals) > MaxProposals {
		set.Proposals = set.Proposals[:MaxProposals]
	}
	return set, nil
}

// allPendingProposals is PendingProposals without the MaxProposals cap: what
// a note or decision is checked against, so a proposal past the first page
// can still be noted and decided.
func allPendingProposals(stateDir string) (ProposalSet, error) {
	dir, err := stateDirOr(stateDir)
	if err != nil {
		return ProposalSet{}, err
	}
	rep, err := ReadSweepReport(dir)
	if err != nil {
		return ProposalSet{}, err
	}
	approvals, _ := readApprovals(dir)
	notes, _ := readNotes(dir)
	set := ProposalSet{Mode: rep.Mode, FinishedAt: rep.FinishedAt}
	for _, e := range rep.Entries {
		if e.Class != ClassNeedsApproval || !pendingOutcome(e.Outcome) || e.Fingerprint == "" {
			continue
		}
		p := Proposal{SweepEntry: e}
		if d, ok := approvals.find(e.Key, e.Fingerprint); ok {
			p.Decision = d.Decision
		}
		if n, ok := notes.find(e.Key, e.Fingerprint); ok {
			p.Note, p.NoteAt = n.Text, n.At
		}
		p.Work = truncateUTF8(p.Work, maxSummaryBytes)
		set.Proposals = append(set.Proposals, p)
	}
	sort.SliceStable(set.Proposals, func(i, j int) bool {
		a, b := set.Proposals[i], set.Proposals[j]
		if (a.Decision == "") != (b.Decision == "") {
			return a.Decision == ""
		}
		if !a.ProposedAt.Equal(b.ProposedAt) {
			return a.ProposedAt.Before(b.ProposedAt)
		}
		return a.Key < b.Key
	})
	set.Total = len(set.Proposals)
	return set, nil
}

// carryProposedAt stamps each needs-approval entry with when its state was
// first proposed: the last report's stamp for the same key and fingerprint,
// else now.
func carryProposedAt(dir string, entries []SweepEntry, now time.Time) {
	prev := map[string]time.Time{}
	if last, err := ReadSweepReport(dir); err == nil {
		for _, e := range last.Entries {
			if e.Fingerprint != "" && !e.ProposedAt.IsZero() {
				prev[e.Key+"\x00"+e.Fingerprint] = e.ProposedAt
			}
		}
	}
	for i := range entries {
		e := &entries[i]
		if e.Class != ClassNeedsApproval || e.Fingerprint == "" {
			continue
		}
		if t, ok := prev[e.Key+"\x00"+e.Fingerprint]; ok {
			e.ProposedAt = t
		} else if e.ProposedAt.IsZero() {
			e.ProposedAt = now
		}
	}
}

func readNotes(dir string) (notesDoc, error) {
	b, err := os.ReadFile(filepath.Join(dir, notesFile))
	if errors.Is(err, os.ErrNotExist) {
		return notesDoc{Version: 1}, nil
	}
	if err != nil {
		return notesDoc{}, err
	}
	var d notesDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return notesDoc{}, fmt.Errorf("%s: %w", filepath.Join(dir, notesFile), err)
	}
	return d, nil
}

func (d notesDoc) find(key, fingerprint string) (Note, bool) {
	for i := len(d.Notes) - 1; i >= 0; i-- {
		if n := d.Notes[i]; n.Key == key && n.Fingerprint == fingerprint {
			return n, true
		}
	}
	return Note{}, false
}

// RecordNotes stores notes about proposals of the last report. A note whose
// key and fingerprint the report does not list as a pending proposal is
// refused, and named in the error; the others are stored. Notes about
// states the report no longer lists are dropped. A call takes at most
// MaxNotes notes, and notes.json holds at most one note per pending
// proposal, each at most maxNoteBytes.
func RecordNotes(ctx context.Context, stateDir string, notes []Note, by string) ([]Note, error) {
	if len(notes) > MaxNotes {
		return nil, fmt.Errorf("%d notes: at most %d at once", len(notes), MaxNotes)
	}
	dir, err := stateDirOr(stateDir)
	if err != nil {
		return nil, err
	}
	// The report is read under the lock the sweep writes it under, so the
	// pending set checked here is the one the notes are stored against.
	unlock, err := approvalsLock(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	set, err := allPendingProposals(dir)
	if err != nil {
		return nil, fmt.Errorf("no reclaim report to note: %w", err)
	}
	pending := map[string]bool{}
	for _, p := range set.Proposals {
		pending[p.Key+"\x00"+p.Fingerprint] = true
	}
	var refused []string
	var add []Note
	at := map[string]int{} // key -> index in add: the last note about a key wins
	now := time.Now().UTC()
	for _, n := range notes {
		if !pending[n.Key+"\x00"+n.Fingerprint] {
			refused = append(refused, n.Key)
			continue
		}
		text := strings.TrimSpace(n.Text)
		if text == "" {
			continue
		}
		note := Note{Key: n.Key, Fingerprint: n.Fingerprint, Text: truncateUTF8(text, maxNoteBytes), At: now, By: by}
		if i, ok := at[n.Key]; ok {
			add[i] = note
			continue
		}
		at[n.Key] = len(add)
		add = append(add, note)
	}
	cur, err := readNotes(dir)
	if err != nil {
		return nil, err
	}
	replaced := map[string]bool{}
	for _, n := range add {
		replaced[n.Key] = true
	}
	kept := cur.Notes[:0]
	for _, n := range cur.Notes {
		if !replaced[n.Key] && pending[n.Key+"\x00"+n.Fingerprint] {
			kept = append(kept, n)
		}
	}
	cur.Notes, cur.Version = append(kept, add...), 1
	b, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, notesFile), append(b, '\n')); err != nil {
		return nil, err
	}
	if len(refused) > 0 {
		return add, fmt.Errorf("not pending proposals of the last report (changed or decided since): %s", strings.Join(refused, ", "))
	}
	return add, nil
}

// DecisionRequest is one decision, bound to the fingerprint the person saw.
type DecisionRequest struct {
	Key         string   `json:"key"`
	Fingerprint string   `json:"fingerprint"`
	Decision    Decision `json:"decision"`
}

// DecisionResult says whether one decision was recorded.
type DecisionResult struct {
	Key      string `json:"key"`
	Recorded bool   `json:"recorded"`
	// Reason is why not: "changed" (the worktree's state is no longer the
	// one decided about), "unknown" (no such proposal), "invalid".
	Reason string `json:"reason,omitempty"`
	Path   string `json:"path,omitempty"`
}

// RecordDecisionsFor records decisions that each name the fingerprint the
// person was shown. A decision whose fingerprint is not the report's current
// one for that key is not recorded: the worktree changed since, and the
// person was asked about a state that no longer exists.
//
// The report is read, and each fingerprint checked, under the approvals lock,
// which a sweep also holds while it rewrites the report: a pass cannot replace
// the entry a decision was checked against before the decision is stored.
func RecordDecisionsFor(ctx context.Context, stateDir string, reqs []DecisionRequest, by string) ([]DecisionResult, error) {
	dir, err := stateDirOr(stateDir)
	if err != nil {
		return nil, err
	}
	unlock, err := approvalsLock(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	rep, err := ReadSweepReport(dir)
	if err != nil {
		return nil, fmt.Errorf("no sweep report to decide from: %w", err)
	}
	byKey := map[string]SweepEntry{}
	for _, e := range rep.Entries {
		if e.Class == ClassNeedsApproval && pendingOutcome(e.Outcome) {
			byKey[e.Key] = e
		}
	}
	results := make([]DecisionResult, 0, len(reqs))
	var add []Approval
	now := time.Now().UTC()
	for _, r := range reqs {
		res := DecisionResult{Key: r.Key}
		e, ok := byKey[r.Key]
		switch {
		case r.Decision != DecisionApprove && r.Decision != DecisionDecline:
			res.Reason = "invalid"
		case !ok:
			res.Reason = "unknown"
		case r.Fingerprint == "" || r.Fingerprint != e.Fingerprint:
			res.Reason, res.Path = "changed", e.Path
		default:
			res.Recorded, res.Path = true, e.Path
			add = append(add, Approval{Key: r.Key, Path: e.Path, Fingerprint: e.Fingerprint, Decision: r.Decision, DecidedAt: now, DecidedBy: by})
		}
		results = append(results, res)
	}
	if len(add) == 0 {
		return results, nil
	}
	if err := storeApprovalsLocked(dir, add); err != nil {
		return nil, err
	}
	return results, nil
}

// storeApprovalsLocked adds decisions, each replacing any earlier one about
// its key. The caller holds approvalsLock.
func storeApprovalsLocked(dir string, add []Approval) error {
	cur, err := readApprovals(dir)
	if err != nil {
		return err
	}
	drop := map[string]bool{}
	for _, d := range add {
		drop[d.Key] = true
	}
	kept := cur.Approvals[:0]
	for _, d := range cur.Approvals {
		if !drop[d.Key] {
			kept = append(kept, d)
		}
	}
	cur.Approvals = append(kept, add...)
	return writeApprovals(dir, cur)
}

// truncateUTF8 shortens s to at most n bytes on a rune boundary, marking the
// cut.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
