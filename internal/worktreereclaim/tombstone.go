// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The reclaim tombstone (designs/worktree-reclaim-tombstone-2026-11-11.md).
// A quiet worktree is not archived at once: the server MARKS it, and this
// daemon places a tombstone where anything working in it will see it:
//
//   - .reliant/RECLAIM_PENDING in each checkout, git-ignored through the
//     repository's info/exclude so it can never be committed or make the
//     checkout dirty;
//   - a line in reliant.md at the workspace root, outside every checkout (so
//     never in a tracked file), which memory injects into a chat working
//     there.
//
// A day later the server reclaims it only if the tombstone is still there
// with the same mark id, and the daemon refuses to remove an archived
// worktree whose tombstone is gone (ReasonMarked). Deleting the file is the
// cancel gesture; so is any use at all, which the server sees and answers by
// unmarking.

// TombstoneFile is the tombstone's name inside each checkout's .reliant
// directory.
const TombstoneFile = "RECLAIM_PENDING"

// tombstoneRel is the tombstone's path relative to its checkout, as git
// names an ignored file.
const tombstoneRel = ".reliant/" + TombstoneFile

// tombstoneExclude is the info/exclude pattern that keeps the tombstone out
// of git: anchored, so it matches only the checkout's own .reliant directory.
const tombstoneExclude = "/" + tombstoneRel

// memoryFile is the workspace-root memory file the tombstone's line goes in.
const memoryFile = "reliant.md"

// memoryMarker opens and closes the tombstone's block in reliant.md, so
// unmarking removes exactly what marking wrote and nothing a user added.
const (
	memoryMarkerOpen  = "<!-- reliant:reclaim-pending "
	memoryMarkerClose = "<!-- /reliant:reclaim-pending -->"
)

// ReclaimMark is the server's mark on a quiet worktree (Worktree.Mark).
type ReclaimMark struct {
	// ID names this mark; a new mark after a cancel gets a new id, so a
	// tombstone left from an earlier one never authorizes a reclaim.
	ID string `json:"id"`
	// ReclaimAt is the earliest the server reclaims it, shown to whoever
	// finds the tombstone.
	ReclaimAt time.Time `json:"reclaim_at"`
}

// TombstoneReport is what an ACTIVE answer says about the workspace's
// tombstone (Result.Tombstone). Its presence is the capability: a daemon
// that predates tombstones never sends one, and the server never reclaims a
// worktree without it.
type TombstoneReport struct {
	// MarkID is the mark every checkout's tombstone carries; empty when
	// there is none, or when the checkouts disagree or one lost its file
	// (any of which cancels).
	MarkID string `json:"mark_id,omitempty"`
}

// tombstonePath is the tombstone of the checkout at c.
func tombstonePath(c string) string { return filepath.Join(c, ".reliant", TombstoneFile) }

// tombstoneText is the file's content: the mark id the daemon reads back,
// then what it means, for whoever opens it.
func tombstoneText(m ReclaimMark) string {
	return fmt.Sprintf("mark_id: %s\nreclaim_at: %s\n\n%s\n", m.ID, m.ReclaimAt.UTC().Format(time.RFC3339), tombstoneNotice(m))
}

// tombstoneNotice is the line an agent or person reads.
func tombstoneNotice(m ReclaimMark) string {
	return fmt.Sprintf("This workspace is marked to be reclaimed at %s. If you are using it, delete `.reliant/%s` or just keep working; either cancels.",
		m.ReclaimAt.UTC().Format(time.RFC3339), TombstoneFile)
}

// readTombstone is the mark id in the checkout's tombstone, or "".
func readTombstone(c string) string {
	b, err := os.ReadFile(tombstonePath(c))
	if err != nil {
		return ""
	}
	line, _, _ := bytes.Cut(b, []byte("\n"))
	id, ok := strings.CutPrefix(strings.TrimSpace(string(line)), "mark_id:")
	if !ok {
		return ""
	}
	return strings.TrimSpace(id)
}

// tombstoneMark is the mark every checkout of the workspace carries, or ""
// when there are none, or they are missing from one or disagree.
func tombstoneMark(checkouts []string) string {
	if len(checkouts) == 0 {
		return ""
	}
	id := readTombstone(checkouts[0])
	for _, c := range checkouts[1:] {
		if readTombstone(c) != id {
			return ""
		}
	}
	return id
}

// placeTombstone writes m's tombstone into every checkout and its line into
// the workspace root's reliant.md. A checkout whose tombstone already
// carries m is left alone, so re-sending a mark changes nothing on disk.
func placeTombstone(ctx context.Context, w Worktree, checkouts []string, m ReclaimMark) error {
	if m.ID == "" || strings.ContainsAny(m.ID, "\n\r") {
		return errors.New("a reclaim mark needs a one-line id")
	}
	if len(checkouts) == 0 {
		return errors.New("no checkout to mark")
	}
	for _, c := range checkouts {
		if err := excludeTombstone(ctx, c); err != nil {
			return fmt.Errorf("keep the tombstone out of git in %s: %w", c, err)
		}
		if readTombstone(c) == m.ID {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(tombstonePath(c)), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(tombstonePath(c), []byte(tombstoneText(m))); err != nil {
			return err
		}
	}
	if !rootIsCheckout(w.Path, checkouts) {
		return writeMemoryLine(filepath.Join(w.Path, memoryFile), m)
	}
	return nil
}

// removeTombstone removes the tombstones that carry id ("" removes any) and
// the reliant.md line, leaving the info/exclude pattern (it matches nothing
// once the file is gone).
func removeTombstone(w Worktree, checkouts []string, id string) error {
	var errs []error
	for _, c := range checkouts {
		if got := readTombstone(c); got != "" && (id == "" || got == id) {
			if err := os.Remove(tombstonePath(c)); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
			// The .reliant directory goes too if the tombstone was all of it.
			_ = os.Remove(filepath.Dir(tombstonePath(c)))
		}
	}
	if !rootIsCheckout(w.Path, checkouts) {
		if err := removeMemoryLine(filepath.Join(w.Path, memoryFile), id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// rootIsCheckout reports whether the workspace root is itself one of its
// checkouts. Its reliant.md is then inside a checkout, possibly tracked, and
// is never written.
func rootIsCheckout(root string, checkouts []string) bool {
	for _, c := range checkouts {
		if samePath(c, root) {
			return true
		}
	}
	return false
}

// excludeTombstone adds the tombstone's pattern to the repository's
// info/exclude, which every linked worktree of it reads.
func excludeTombstone(ctx context.Context, checkout string) error {
	common, err := git(ctx, checkout, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	path := filepath.Join(strings.TrimSpace(common), "info", "exclude")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == tombstoneExclude {
			return nil
		}
	}
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		b = append(b, '\n')
	}
	b = append(b, []byte("# reliant: the reclaim tombstone (never committed)\n"+tombstoneExclude+"\n")...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// writeMemoryLine puts m's block in the reliant.md at path, replacing an
// earlier mark's, and keeps everything else in the file.
func writeMemoryLine(path string, m ReclaimMark) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	rest := stripMemoryBlock(string(b), "")
	block := memoryMarkerOpen + m.ID + " -->\n" + tombstoneNotice(m) + "\n" + memoryMarkerClose + "\n"
	out := block
	if strings.TrimSpace(rest) != "" {
		out = block + "\n" + rest
	}
	if out == string(b) {
		return nil
	}
	return writeFileAtomic(path, []byte(out))
}

// removeMemoryLine removes the block of mark id ("" any) from the reliant.md
// at path, and the file itself when the block was all of it.
func removeMemoryLine(path, id string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	rest := stripMemoryBlock(string(b), id)
	if rest == string(b) {
		return nil
	}
	if strings.TrimSpace(rest) == "" {
		return os.Remove(path)
	}
	return writeFileAtomic(path, []byte(rest))
}

// stripMemoryBlock is s without the tombstone block of mark id ("" any),
// and without the blank line that separated it from the rest.
func stripMemoryBlock(s, id string) string {
	start := strings.Index(s, memoryMarkerOpen)
	if start < 0 {
		return s
	}
	if id != "" && !strings.HasPrefix(s[start+len(memoryMarkerOpen):], id+" ") {
		return s
	}
	end := strings.Index(s[start:], memoryMarkerClose)
	if end < 0 {
		return s
	}
	end += start + len(memoryMarkerClose)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	if end < len(s) && s[end] == '\n' {
		end++
	}
	return s[:start] + s[end:]
}

// applyMark places or removes the tombstone an ACTIVE request asks for, and
// reports the one the workspace carries. A mark that cannot be placed is an
// error answer, so the server never counts a mark nobody can see.
func (r *Reclaimer) applyMark(ctx context.Context, w Worktree, checkouts []string, res *Result) {
	if w.Unmark != "" {
		id := w.Unmark
		if id == "*" {
			id = ""
		}
		if err := removeTombstone(w, checkouts, id); err != nil && res.Outcome == OutcomeLocked {
			res.Outcome, res.Error = OutcomeError, "could not remove the reclaim tombstone: "+err.Error()
		}
	}
	if w.Mark != nil && res.Outcome == OutcomeLocked {
		if err := placeTombstone(ctx, w, checkouts, *w.Mark); err != nil {
			res.Outcome, res.Error = OutcomeError, "could not place the reclaim tombstone: "+err.Error()
		}
	}
	res.Tombstone = &TombstoneReport{MarkID: tombstoneMark(checkouts)}
}

// MarkCommand and UnmarkCommand are the daemon commands that place and remove
// tombstones. Each answers as worktree.reconcile does for an ACTIVE worktree
// (locked, with the landed probe if asked and the tombstone report), so the
// server reads the same answer it decides on.
const (
	MarkCommand   = "worktree.mark_reclaim"
	UnmarkCommand = "worktree.unmark_reclaim"
)

// Mark places each worktree's Mark (worktree.mark_reclaim). Only ACTIVE
// worktrees with a mark are touched; anything else is answered as an error
// with nothing done, since a mark on an archived row would be meaningless.
func (r *Reclaimer) Mark(ctx context.Context, req Request) Response {
	return r.reconcileMarks(ctx, req, func(w Worktree) bool { return w.Mark != nil })
}

// Unmark removes each worktree's tombstone (worktree.unmark_reclaim): of
// the mark named by Unmark, or any when it is empty or "*".
func (r *Reclaimer) Unmark(ctx context.Context, req Request) Response {
	for i := range req.Worktrees {
		if req.Worktrees[i].Unmark == "" {
			req.Worktrees[i].Unmark = "*"
		}
		req.Worktrees[i].Mark = nil
	}
	return r.reconcileMarks(ctx, req, func(Worktree) bool { return true })
}

func (r *Reclaimer) reconcileMarks(ctx context.Context, req Request, ok func(Worktree) bool) Response {
	var send []Worktree
	refused := map[int]Result{}
	for i, w := range req.Worktrees {
		if w.State != StateActive || w.Remove || w.Retire != "" || !ok(w) {
			refused[i] = Result{ID: w.ID, Outcome: OutcomeError, Error: "a reclaim mark is placed and removed only on an active worktree"}
			continue
		}
		send = append(send, w)
	}
	got := r.Reconcile(ctx, Request{Worktrees: send}).Results
	out := make([]Result, 0, len(req.Worktrees))
	for i := range req.Worktrees {
		if res, bad := refused[i]; bad {
			out = append(out, res)
			continue
		}
		out = append(out, got[0])
		got = got[1:]
	}
	return Response{Results: out}
}
