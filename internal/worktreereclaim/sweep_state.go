// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The machine sweep's files, all under one directory (DefaultStateDir,
// ~/.reliant/reclaim):
//
//	report.json      the last pass's SweepReport, replaced atomically
//	journal.jsonl    one JournalLine per decision, append-only, rotated at
//	                 journalMaxBytes to journal.jsonl.1
//	approvals.json   a person's approve/decline decisions (Approvals), written
//	                 by `reliant worktree reclaim approve|decline`
//	locks/           cross-process locks, one per capture key

const (
	reportFile    = "report.json"
	journalFile   = "journal.jsonl"
	approvalsFile = "approvals.json"
	// journalMaxBytes rotates the journal: the previous one is kept beside it.
	journalMaxBytes = 10 << 20
	// approvalTTL drops decisions nobody acted on: a pass that cannot match
	// one to its worktree any more never will.
	approvalTTL = 30 * 24 * time.Hour
)

// DefaultStateDir is where the machine sweep keeps its report, journal and
// approvals.
func DefaultStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reliant", "reclaim"), nil
}

// writeFileAtomic replaces path with data: a reader sees the old file or
// the new one, never a torn one.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// ReadSweepReport reads the last pass's report from stateDir ("" means
// DefaultStateDir).
func ReadSweepReport(stateDir string) (SweepReport, error) {
	dir, err := stateDirOr(stateDir)
	if err != nil {
		return SweepReport{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, reportFile))
	if err != nil {
		return SweepReport{}, err
	}
	var rep SweepReport
	if err := json.Unmarshal(b, &rep); err != nil {
		return SweepReport{}, fmt.Errorf("%s: %w", filepath.Join(dir, reportFile), err)
	}
	return rep, nil
}

func stateDirOr(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	return DefaultStateDir()
}

// JournalLine is one decision of one pass.
type JournalLine struct {
	At      time.Time `json:"at"`
	Mode    string    `json:"mode"`
	Trigger string    `json:"trigger,omitempty"`
	SweepEntry
	DaemonVersion string `json:"daemon_version,omitempty"`
	// ApprovedBy and CommandID name the Inbox approval a command's pass
	// acted on (worktree.reclaim_approve): the audit record of who approved
	// removing it.
	ApprovedBy string `json:"approved_by,omitempty"`
	CommandID  string `json:"command_id,omitempty"`
}

// appendJournal appends lines to the journal, rotating it first when it has
// outgrown journalMaxBytes.
func appendJournal(dir string, lines []JournalLine) error {
	if len(lines) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, journalFile)
	if fi, err := os.Stat(path); err == nil && fi.Size() > journalMaxBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			continue
		}
		_, _ = w.Write(append(b, '\n'))
	}
	err = w.Flush()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// ReadJournal reads every line of the journal (the rotated one first).
// Unparsable lines are skipped.
func ReadJournal(stateDir string) ([]JournalLine, error) {
	dir, err := stateDirOr(stateDir)
	if err != nil {
		return nil, err
	}
	var out []JournalLine
	found := false
	for _, name := range []string{journalFile + ".1", journalFile} {
		f, err := os.Open(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found = true
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			var l JournalLine
			if json.Unmarshal(sc.Bytes(), &l) == nil {
				out = append(out, l)
			}
		}
		f.Close()
	}
	if !found {
		return nil, os.ErrNotExist
	}
	return out, nil
}

// Decision is a person's answer about one needs-approval worktree.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionDecline Decision = "decline"
)

// Approval binds a decision to the exact state of the worktree it was made
// about: a worktree whose fingerprint has changed since (a commit, an edit, a
// new file) is a different question, and the decision does not apply to it.
type Approval struct {
	Key         string    `json:"key"`
	Path        string    `json:"path"`
	Fingerprint string    `json:"fingerprint"`
	Decision    Decision  `json:"decision"`
	DecidedAt   time.Time `json:"decided_at"`
	DecidedBy   string    `json:"decided_by,omitempty"`
}

// Approvals is approvals.json.
type Approvals struct {
	Version   int        `json:"version"`
	Approvals []Approval `json:"approvals"`
}

func (a Approvals) find(key, fingerprint string) (Approval, bool) {
	for i := len(a.Approvals) - 1; i >= 0; i-- {
		if d := a.Approvals[i]; d.Key == key && d.Fingerprint == fingerprint {
			return d, true
		}
	}
	return Approval{}, false
}

// approvalsLock serializes every read-modify-write of approvals.json and
// notes.json, the CLI's and the daemon's, with the sweep's rewrite of
// report.json: a decision or note is checked against the report and stored
// while no pass can replace it.
func approvalsLock(ctx context.Context, dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return lockFile(ctx, filepath.Join(dir, approvalsFile+".lock"))
}

func readApprovals(dir string) (Approvals, error) {
	b, err := os.ReadFile(filepath.Join(dir, approvalsFile))
	if errors.Is(err, os.ErrNotExist) {
		return Approvals{Version: 1}, nil
	}
	if err != nil {
		return Approvals{}, err
	}
	var a Approvals
	if err := json.Unmarshal(b, &a); err != nil {
		return Approvals{}, fmt.Errorf("%s: %w", filepath.Join(dir, approvalsFile), err)
	}
	return a, nil
}

func writeApprovals(dir string, a Approvals) error {
	a.Version = 1
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, approvalsFile), append(b, '\n'))
}

// RecordDecisions records decisions about entries of the last report, each
// bound to the fingerprint the report gave it. A key the report does not
// list as needing approval is refused: a decision is only ever about what a
// person was shown. It returns the decisions recorded.
func RecordDecisions(ctx context.Context, stateDir string, decision Decision, keys []string, by string) ([]Approval, error) {
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
		if e.Class == ClassNeedsApproval {
			byKey[e.Key] = e
		}
	}
	var add []Approval
	for _, k := range keys {
		e, ok := byKey[k]
		if !ok {
			return nil, fmt.Errorf("%s is not a worktree the last report lists as needing approval", k)
		}
		add = append(add, Approval{Key: k, Path: e.Path, Fingerprint: e.Fingerprint, Decision: decision, DecidedAt: time.Now().UTC(), DecidedBy: by})
	}
	return add, storeApprovalsLocked(dir, add)
}

// PendingApprovals reports whether approvals.json holds an approval made
// after since. The daemon polls it each minute, with since the start of its
// last approved-only pass, to act on a decision promptly without repeating
// a pass for one it already could not act on.
func PendingApprovals(stateDir string, since time.Time) bool {
	dir, err := stateDirOr(stateDir)
	if err != nil {
		return false
	}
	a, err := readApprovals(dir)
	if err != nil {
		return false
	}
	for _, d := range a.Approvals {
		if d.Decision == DecisionApprove && d.DecidedAt.After(since) {
			return true
		}
	}
	return false
}

// settleApprovalsLocked drops the approvals a pass consumed (the worktree was
// reclaimed, or is gone) and the decisions older than approvalTTL. The caller
// holds approvalsLock.
func settleApprovalsLocked(dir string, consumed map[string]bool, now time.Time) {
	cur, err := readApprovals(dir)
	if err != nil {
		return
	}
	kept := cur.Approvals[:0]
	for _, d := range cur.Approvals {
		if consumed[d.Key] || now.Sub(d.DecidedAt) > approvalTTL {
			continue
		}
		kept = append(kept, d)
	}
	if len(kept) == len(cur.Approvals) {
		return
	}
	cur.Approvals = kept
	_ = writeApprovals(dir, cur)
}
