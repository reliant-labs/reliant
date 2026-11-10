// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// An Inbox approval acts only when it reaches the daemon as a command
// (owner decision 2026-11-10, question 47657011: "trust the daemon command").
// The server sends worktree.reclaim_approve to the machine after it has
// authenticated the person, checked they may write that machine and that the
// worktree has no live chat; the daemon then removes exactly the approved
// worktree, at exactly the state approved, through the sweep's own pipeline:
// re-evaluated from scratch under its locks, captured into a local ref,
// parked, checked again (the capture exists and matches the files, the
// fingerprint is the approved one, no process or daemon work is using it),
// and only then retired. approvals.json is never a source of authority on its
// own: an approval written there by anything else is recorded and reported,
// and deletes nothing (SweepOptions.Authorized).
//
// The command is answered at once (PrepareApprovalCommand: the checks that
// need no git walk, and the approval recorded for audit), and the removal
// runs after it (RunApprovalCommand), because deleting a large checkout takes
// longer than a command may. Its result is kept in approval-commands.json and
// in the journal (approved_by, command_id), and a refused one puts the
// proposal back in front of the person with the reason (PendingProposals).

// AuthorizedApproval is one approval a pass may act on: the person's, as the
// server delivered it.
type AuthorizedApproval struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	Path        string `json:"path"`
	ApprovedBy  string `json:"approved_by"`
	CommandID   string `json:"command_id,omitempty"`
}

func (o SweepOptions) authorized(key, fingerprint, path string) (AuthorizedApproval, bool) {
	for _, a := range o.Authorized {
		if a.Key == key && a.Fingerprint != "" && a.Fingerprint == fingerprint && samePath(a.Path, path) {
			return a, true
		}
	}
	return AuthorizedApproval{}, false
}

func (o SweepOptions) authorizes(key, fingerprint, path string) bool {
	_, ok := o.authorized(key, fingerprint, path)
	return ok
}

// authorizedAmendments is what a command's pass writes into the last report:
// the entry of every authorized worktree it reached, whatever became of it,
// and nothing else (the other proposals stay as the last full pass left
// them). A removed one is recorded as reclaimed. One whose state changed is
// recorded in its new state, proposed again and undecided. One refused while
// it was carried out (in use, its capture missing, changed while parked) is
// held by the pass, which would drop it from the proposals; it is recorded
// instead as the proposal it was, at the state the person approved, awaiting a
// decision again, with why its approval was not carried out.
func authorizedAmendments(dir string, entries []SweepEntry, o SweepOptions) []SweepEntry {
	last := map[string]SweepEntry{}
	if rep, err := ReadSweepReport(dir); err == nil {
		for _, e := range rep.Entries {
			last[e.Key] = e
		}
	}
	var out []SweepEntry
	for _, e := range entries {
		a, ok := o.authorizedFor(e.Key, e.Path)
		if !ok {
			continue
		}
		if e.Outcome != OutcomeReclaimed && e.Class != ClassNeedsApproval {
			reason := string(e.Reason)
			if e.Outcome == OutcomeSweepDeferred {
				reason = RefusedDeferred
			}
			if prev, ok := last[e.Key]; ok && prev.Class == ClassNeedsApproval && prev.Fingerprint == a.Fingerprint {
				prev.Outcome = OutcomeAwaiting
				prev.Detail = refusedDetail(reason, e.Detail)
				e = prev
			}
		}
		out = append(out, e)
	}
	return out
}

// refusedDetail is how a proposal whose approval was not carried out says so.
func refusedDetail(reason, detail string) string {
	return "Your approval was not carried out (" + reason + "): " + detail
}

func (o SweepOptions) authorizedFor(key, path string) (AuthorizedApproval, bool) {
	for _, a := range o.Authorized {
		if a.Key == key && samePath(a.Path, path) {
			return a, true
		}
	}
	return AuthorizedApproval{}, false
}

func (o SweepOptions) authorizesKey(key string) bool {
	for _, a := range o.Authorized {
		if a.Key == key {
			return true
		}
	}
	return false
}

// ApprovalStatus is where one approval command stands.
type ApprovalStatus string

const (
	// ApprovalAccepted: the checks passed, the approval is recorded and the
	// removal is running (or about to).
	ApprovalAccepted ApprovalStatus = "accepted"
	// ApprovalReclaimed: removed, its work captured first.
	ApprovalReclaimed ApprovalStatus = "reclaimed"
	// ApprovalRefused: nothing was removed; Reason says why.
	ApprovalRefused ApprovalStatus = "refused"
)

// Reasons an approval command is refused, beside the HeldReasons a pass can
// hold the worktree for (in_use, capture_missing, …).
const (
	RefusedInvalid     = "invalid"
	RefusedChanged     = "changed"       // not the state the person approved
	RefusedUnknown     = "unknown"       // the last report does not propose it
	RefusedPath        = "path_mismatch" // the report has it at another path
	RefusedNotLinked   = "not_a_linked_worktree"
	RefusedModeOff     = "reclaim_off" // the machine's reclaim is off or dry-run
	RefusedNotFound    = "not_found"   // the pass did not find it
	RefusedDeferred    = "deferred"    // busy or out of time; send it again
	approvalCommandTTL = 30 * 24 * time.Hour
	// approvalStaleAfter: an accepted command whose removal never reported
	// (the daemon stopped mid-way) may be sent again after this.
	approvalStaleAfter   = time.Hour
	approvalCommandsFile = "approval-commands.json"
	maxApprovalCommands  = 200
)

// ApprovalCommand is worktree.reclaim_approve's request about one proposal.
type ApprovalCommand struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	Path        string `json:"path"`
	ApprovedBy  string `json:"approved_by"`
	CommandID   string `json:"command_id,omitempty"`
}

// ApprovalResult is what became of one approval command.
type ApprovalResult struct {
	Key         string         `json:"key"`
	Fingerprint string         `json:"fingerprint"`
	Path        string         `json:"path,omitempty"`
	Status      ApprovalStatus `json:"status"`
	Reason      string         `json:"reason,omitempty"`
	Detail      string         `json:"detail,omitempty"`
	CaptureRef  string         `json:"capture_ref,omitempty"`
	FreedBytes  int64          `json:"freed_bytes,omitempty"`
	ApprovedBy  string         `json:"approved_by,omitempty"`
	CommandID   string         `json:"command_id,omitempty"`
	At          time.Time      `json:"at"`
	// Replayed: the command was already answered for this exact state, and
	// this is that answer; nothing was done again.
	Replayed bool `json:"replayed,omitempty"`
}

type approvalCommands struct {
	Version int              `json:"version"`
	Results []ApprovalResult `json:"results"`
}

func readApprovalCommands(dir string) (approvalCommands, error) {
	b, err := os.ReadFile(filepath.Join(dir, approvalCommandsFile))
	if errors.Is(err, os.ErrNotExist) {
		return approvalCommands{Version: 1}, nil
	}
	if err != nil {
		return approvalCommands{}, err
	}
	var c approvalCommands
	if err := json.Unmarshal(b, &c); err != nil {
		return approvalCommands{}, fmt.Errorf("%s: %w", filepath.Join(dir, approvalCommandsFile), err)
	}
	return c, nil
}

func (c approvalCommands) find(key, fingerprint string) (ApprovalResult, bool) {
	for i := len(c.Results) - 1; i >= 0; i-- {
		if r := c.Results[i]; r.Key == key && r.Fingerprint == fingerprint {
			return r, true
		}
	}
	return ApprovalResult{}, false
}

// storeApprovalResultLocked records res, replacing any earlier result for
// the same proposal state. The caller holds approvalsLock.
func storeApprovalResultLocked(dir string, res ApprovalResult, now time.Time) error {
	cur, err := readApprovalCommands(dir)
	if err != nil {
		return err
	}
	kept := cur.Results[:0]
	for _, r := range cur.Results {
		if (r.Key == res.Key && r.Fingerprint == res.Fingerprint) || now.Sub(r.At) > approvalCommandTTL {
			continue
		}
		kept = append(kept, r)
	}
	kept = append(kept, res)
	if len(kept) > maxApprovalCommands {
		kept = kept[len(kept)-maxApprovalCommands:]
	}
	cur.Version, cur.Results = 1, kept
	b, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, approvalCommandsFile), append(b, '\n'))
}

// ApprovalCommandResults is every recorded approval command, oldest first.
func ApprovalCommandResults(stateDir string) ([]ApprovalResult, error) {
	dir, err := stateDirOr(stateDir)
	if err != nil {
		return nil, err
	}
	c, err := readApprovalCommands(dir)
	return c.Results, err
}

// PrepareApprovalCommand answers one approval command at once: it refuses
// what it can tell is wrong without walking the checkout (a state that is not
// the one approved, a path that is not the proposal's, a main checkout, a
// machine whose reclaim is not on), records the approval (for audit, and as
// what the pass checks), and accepts the rest for RunApprovalCommand. A
// command already answered for the same state returns that answer (Replayed)
// and does nothing: sending the same approval twice removes nothing twice.
func PrepareApprovalCommand(ctx context.Context, stateDir string, mode SweepMode, cmd ApprovalCommand, now time.Time) (ApprovalResult, error) {
	dir, err := stateDirOr(stateDir)
	if err != nil {
		return ApprovalResult{}, err
	}
	res := ApprovalResult{Key: cmd.Key, Fingerprint: cmd.Fingerprint, Path: cmd.Path, ApprovedBy: cmd.ApprovedBy, CommandID: cmd.CommandID, At: now.UTC()}
	refuse := func(reason, detail string) (ApprovalResult, error) {
		res.Status, res.Reason, res.Detail = ApprovalRefused, reason, detail
		return res, nil
	}
	if cmd.Key == "" || cmd.Fingerprint == "" || cmd.Path == "" || strings.TrimSpace(cmd.ApprovedBy) == "" {
		return refuse(RefusedInvalid, "an approval names its proposal's key, fingerprint and path, and who approved it")
	}

	unlock, err := approvalsLock(ctx, dir)
	if err != nil {
		return ApprovalResult{}, err
	}
	defer unlock()
	prior, err := readApprovalCommands(dir)
	if err != nil {
		return ApprovalResult{}, err
	}
	if p, ok := prior.find(cmd.Key, cmd.Fingerprint); ok {
		switch {
		case p.Status == ApprovalReclaimed,
			p.Status == ApprovalAccepted && now.Sub(p.At) < approvalStaleAfter:
			p.Replayed = true
			return p, nil
		}
	}
	rep, err := ReadSweepReport(dir)
	if err != nil {
		return refuse(RefusedUnknown, "this machine has no reclaim report")
	}
	var entry *SweepEntry
	for i := range rep.Entries {
		if rep.Entries[i].Key == cmd.Key && rep.Entries[i].Class == ClassNeedsApproval && pendingOutcome(rep.Entries[i].Outcome) {
			entry = &rep.Entries[i]
		}
	}
	switch {
	case entry == nil:
		return refuse(RefusedUnknown, "this machine no longer proposes it")
	case entry.Fingerprint != cmd.Fingerprint:
		return refuse(RefusedChanged, "it changed after it was proposed; it is proposed again in its new state")
	case !samePath(entry.Path, cmd.Path):
		return refuse(RefusedPath, fmt.Sprintf("the proposal is at %s, not %s", entry.Path, cmd.Path))
	case !isLinkedWorktree(cmd.Path):
		return refuse(RefusedNotLinked, "it is not a linked worktree (a main checkout is never removed)")
	}
	// Recorded for audit whatever happens next: who approved which state.
	if err := storeApprovalsLocked(dir, []Approval{{Key: cmd.Key, Path: entry.Path, Fingerprint: cmd.Fingerprint,
		Decision: DecisionApprove, DecidedAt: now.UTC(), DecidedBy: cmd.ApprovedBy}}); err != nil {
		return ApprovalResult{}, err
	}
	if mode != SweepOn {
		res, _ = refuse(RefusedModeOff, "worktree reclaim is "+string(mode)+" on this machine; the approval is recorded and nothing is removed")
		return res, storeApprovalResultLocked(dir, res, now)
	}
	res.Status = ApprovalAccepted
	return res, storeApprovalResultLocked(dir, res, now)
}

// RunApprovalCommand removes the worktree an accepted command approved: a
// pass over that worktree alone, authorized for exactly that key, path and
// fingerprint, through the sweep's capture, verify and retire pipeline. Its
// result replaces the accepted one. opts is the machine's sweep
// configuration; its Authorized is set here.
func (r *Reclaimer) RunApprovalCommand(ctx context.Context, opts SweepOptions, cmd ApprovalCommand) ApprovalResult {
	opts.Authorized = []AuthorizedApproval{{Key: cmd.Key, Fingerprint: cmd.Fingerprint, Path: cmd.Path, ApprovedBy: cmd.ApprovedBy, CommandID: cmd.CommandID}}
	opts.Trigger = "approval"
	res := ApprovalResult{Key: cmd.Key, Fingerprint: cmd.Fingerprint, Path: cmd.Path, ApprovedBy: cmd.ApprovedBy, CommandID: cmd.CommandID}
	rep := r.SweepMachine(ctx, opts)
	res.Status, res.Reason, res.Detail = ApprovalRefused, RefusedNotFound, "the pass did not find it at its path"
	for _, e := range rep.Entries {
		if e.Key != cmd.Key || !samePath(e.Path, cmd.Path) {
			continue
		}
		res.Detail = e.Detail
		switch {
		case e.Outcome == OutcomeReclaimed:
			res.Status, res.Reason, res.CaptureRef, res.FreedBytes = ApprovalReclaimed, "", e.CaptureRef, e.FreedBytes
		case e.Class == ClassNeedsApproval && e.Fingerprint != cmd.Fingerprint,
			strings.Contains(e.Detail, "changed since it was approved"):
			res.Reason = RefusedChanged
		case e.Outcome == OutcomeSweepDeferred:
			res.Reason = RefusedDeferred
		case e.Reason != "":
			res.Reason = string(e.Reason)
		default:
			res.Reason = string(e.Outcome)
		}
	}
	res.At = r.now().UTC()
	dir, err := stateDirOr(opts.StateDir)
	if err == nil {
		if unlock, err := approvalsLock(context.WithoutCancel(ctx), dir); err == nil {
			_ = storeApprovalResultLocked(dir, res, res.At)
			unlock()
		}
	}
	return res
}
