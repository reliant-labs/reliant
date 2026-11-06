// Copyright (c) 2026 Reliant Labs
package shell

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// The background journal keeps the daemon's background-process records on its
// own disk, so a daemon restart does not erase them.
//
// The registry is in memory. Before the journal, a restart — an upgrade, a
// crash, the machine restarting — forgot every process it had started: an
// agent waiting on a test run got "No process with ID" from shell_wait and an
// empty shell_list, and the verdict and output were gone with no word of why.
// On 2026-10-30 a machine restart did exactly that to several agents' scoped
// test runs mid-recovery.
//
// Each process is two files under the journal directory:
//
//   - <id>.json — the record (command, status, exit code, times, scope),
//     rewritten whole, atomically, on start and on every status change;
//   - <id>.out  — its output, one JSON line per output line, appended as the
//     manager flushes it. Past journalSegmentBytes it rotates to <id>.out.1,
//     so the journal keeps the latest 1–2 segments: the end of a run is where
//     its verdict is.
//
// On start the daemon restores the records into the registry. A record still
// "running" there belongs to a process the daemon lost track of — a graceful
// stop kills its processes and journals them as killed first — so it comes
// back "stale", with a final stderr line saying the restart lost it. Records
// that ended more than journalRetention ago, and all but the newest
// journalMaxRecords, are deleted.
//
// The journal is wired into the manager as its PersistenceCallback; it is the
// only persistence a daemon has (the API server has no BackgroundManager).

const (
	// journalSegmentBytes is when an output file rotates.
	journalSegmentBytes = 256 << 10
	// journalRetention is how long an ended process's record is kept.
	journalRetention = 24 * time.Hour
	// journalMaxRecords bounds how many records are kept, and so restored:
	// a restored record's output is held in memory, like a live one's.
	journalMaxRecords = 100
	// journalPruneEvery throttles the prune a status change triggers.
	journalPruneEvery = 10 * time.Minute

	// StatusStale is a restored record whose process the daemon lost track of.
	StatusStale = "stale"
)

// journalIDPattern is what a journal file name may be built from. Ids are
// uuids the manager generates; anything else never reaches the file system.
var journalIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// journalRecord is the on-disk record of one background process.
type journalRecord struct {
	ID             string     `json:"id"`
	Command        string     `json:"command"`
	Status         string     `json:"status"`
	StartTime      time.Time  `json:"start_time"`
	EndTime        *time.Time `json:"end_time,omitempty"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	PID            int        `json:"pid,omitempty"`
	WorkingDir     string     `json:"working_dir"`
	WorktreeID     string     `json:"worktree_id,omitempty"`
	PackageCommand bool       `json:"package_command,omitempty"`
	SessionID      string     `json:"session_id,omitempty"`
	ChatID         string     `json:"chat_id,omitempty"`
	ThreadID       string     `json:"thread_id,omitempty"`
	KeepAlive      bool       `json:"keep_alive,omitempty"`
	GrantID        string     `json:"grant_id,omitempty"`
}

// BackgroundJournal persists background-process records to a directory.
type BackgroundJournal struct {
	dir string
	now func() time.Time

	// mu serializes every write: a kill's status change and the completion
	// it races must land in the order the manager made them.
	mu        sync.Mutex
	lastPrune time.Time
}

// NewBackgroundJournal returns a journal kept in dir, created on first write.
func NewBackgroundJournal(dir string) *BackgroundJournal {
	return &BackgroundJournal{dir: dir, now: time.Now}
}

// Callback is the journal as the manager's persistence hooks.
//
// A journal that cannot write (a full disk) never fails the command: the
// process runs, untracked across a restart, and the failure is a warning.
func (j *BackgroundJournal) Callback() *PersistenceCallback {
	return &PersistenceCallback{
		OnCreate: func(process *BackgroundProcess) error {
			if err := j.recordStart(process); err != nil {
				logging.Warn("Background journal: could not record process", "id", process.ID, "error", err)
				return fmt.Errorf("%w: %v", ErrPersistenceSkipped, err)
			}
			return nil
		},
		OnStatusChange: func(processID, status string, exitCode *int, endTime *time.Time) error {
			if err := j.recordStatus(processID, status, exitCode, endTime); err != nil {
				logging.Warn("Background journal: could not record status", "id", processID, "status", status, "error", err)
			}
			return nil
		},
		OnOutput: func(processID string, lines []OutputLineWithSeq) error {
			if err := j.appendOutput(processID, lines); err != nil {
				logging.Warn("Background journal: could not record output", "id", processID, "error", err)
			}
			return nil
		},
	}
}

func (j *BackgroundJournal) recordPath(id string) string { return filepath.Join(j.dir, id+".json") }
func (j *BackgroundJournal) outputPath(id string) string { return filepath.Join(j.dir, id+".out") }

func (j *BackgroundJournal) recordStart(process *BackgroundProcess) error {
	if !journalIDPattern.MatchString(process.ID) {
		return fmt.Errorf("unjournalable process id %q", process.ID)
	}
	pid := process.GetPID()
	process.outputMu.RLock()
	rec := journalRecord{
		ID:             process.ID,
		Command:        process.Command,
		Status:         process.Status,
		StartTime:      process.StartTime,
		EndTime:        process.EndTime,
		ExitCode:       process.ExitCode,
		PID:            pid,
		WorkingDir:     process.WorkingDir,
		WorktreeID:     process.WorktreeID,
		PackageCommand: process.PackageCommand,
		SessionID:      process.SessionID,
		ChatID:         process.ChatID,
		ThreadID:       process.ThreadID,
		KeepAlive:      process.KeepAlive,
		GrantID:        process.GrantID,
	}
	process.outputMu.RUnlock()

	j.mu.Lock()
	defer j.mu.Unlock()
	// The directory is made by Restore, never here: a journal whose directory
	// is gone (a removed data dir) records nothing rather than recreate it.
	return j.writeRecordLocked(rec)
}

func (j *BackgroundJournal) recordStatus(id, status string, exitCode *int, endTime *time.Time) error {
	if !journalIDPattern.MatchString(id) {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	rec, err := j.readRecordLocked(id)
	if errors.Is(err, fs.ErrNotExist) {
		// Never journaled (its start could not be written): nothing to update.
		return nil
	}
	if err != nil {
		return err
	}
	rec.Status = status
	rec.ExitCode = exitCode
	rec.EndTime = endTime
	if err := j.writeRecordLocked(rec); err != nil {
		return err
	}
	if j.now().Sub(j.lastPrune) >= journalPruneEvery {
		j.lastPrune = j.now()
		if err := j.pruneLocked(); err != nil {
			logging.Warn("Background journal: prune failed", "dir", j.dir, "error", err)
		}
	}
	return nil
}

func (j *BackgroundJournal) appendOutput(id string, lines []OutputLineWithSeq) error {
	if len(lines) == 0 || !journalIDPattern.MatchString(id) {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := os.Stat(j.recordPath(id)); err != nil {
		// No record (never journaled, or pruned): output alone restores nothing.
		return nil
	}
	return j.appendOutputLocked(id, lines)
}

func (j *BackgroundJournal) readRecordLocked(id string) (journalRecord, error) {
	var rec journalRecord
	data, err := os.ReadFile(j.recordPath(id))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("unreadable record %s: %w", id, err)
	}
	return rec, nil
}

// writeRecordLocked replaces the record atomically: a crash mid-write leaves
// the previous record, never a torn one.
func (j *BackgroundJournal) writeRecordLocked(rec journalRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(j.dir, rec.ID+".json.tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), j.recordPath(rec.ID)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

func (j *BackgroundJournal) removeLocked(id string) {
	for _, p := range []string{j.recordPath(id), j.outputPath(id), j.outputPath(id) + ".1"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logging.Warn("Background journal: could not remove", "path", p, "error", err)
		}
	}
}

// loadRecordsLocked reads every record in the journal, deleting what is past
// retention, over the record bound, unreadable, or a leftover temp file. The
// survivors come back newest first. protectRunning keeps every "running"
// record outside the bound; Restore passes false, since no process this
// daemon could still be tracking has been journaled yet.
func (j *BackgroundJournal) loadRecordsLocked(protectRunning bool) ([]journalRecord, error) {
	entries, err := os.ReadDir(j.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := j.now()
	var records []journalRecord
	for _, e := range entries {
		name := e.Name()
		if strings.Contains(name, ".json.tmp-") {
			_ = os.Remove(filepath.Join(j.dir, name))
			continue
		}
		id, ok := strings.CutSuffix(name, ".json")
		if !ok || !journalIDPattern.MatchString(id) {
			continue
		}
		rec, err := j.readRecordLocked(id)
		if err != nil || rec.ID != id {
			logging.Warn("Background journal: dropping unreadable record", "id", id, "error", err)
			j.removeLocked(id)
			continue
		}
		if rec.EndTime != nil && now.Sub(*rec.EndTime) > journalRetention {
			j.removeLocked(id)
			continue
		}
		records = append(records, rec)
	}
	// Output files whose record is gone.
	for _, e := range entries {
		name := e.Name()
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".1"), ".out")
		if base == name || !journalIDPattern.MatchString(base) {
			continue
		}
		if _, err := os.Stat(j.recordPath(base)); errors.Is(err, fs.ErrNotExist) {
			_ = os.Remove(filepath.Join(j.dir, name))
		}
	}
	// The bound: with protectRunning, every running record is kept (a live
	// process's record is never dropped for count alone) and the newest ended
	// records fill what is left of journalMaxRecords; otherwise the newest
	// journalMaxRecords are kept, whatever their status. Either way an ended
	// record is never kept over a newer ended one.
	sort.SliceStable(records, func(a, b int) bool {
		if !records[a].StartTime.Equal(records[b].StartTime) {
			return records[a].StartTime.After(records[b].StartTime)
		}
		return records[a].ID < records[b].ID
	})
	room := journalMaxRecords
	if protectRunning {
		for _, rec := range records {
			if rec.Status == "running" {
				room--
			}
		}
	}
	kept := records[:0]
	for _, rec := range records {
		switch {
		case protectRunning && rec.Status == "running":
		case room > 0:
			room--
		default:
			j.removeLocked(rec.ID)
			continue
		}
		kept = append(kept, rec)
	}
	return kept, nil
}

// pruneLocked applies retention and the bound while the daemon runs: a
// "running" record there may be a live process, so it is kept.
func (j *BackgroundJournal) pruneLocked() error {
	_, err := j.loadRecordsLocked(true)
	return err
}

// readOutputLocked reads a record's journaled output, oldest first, and
// whether an earlier segment was dropped.
func (j *BackgroundJournal) readOutputLocked(id string) ([]OutputLineWithSeq, bool) {
	var lines []OutputLineWithSeq
	read := func(path string) bool {
		f, err := os.Open(path)
		if err != nil {
			return false
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 2*maxOutputLineBytes+1024)
		for sc.Scan() {
			var line OutputLineWithSeq
			if json.Unmarshal(sc.Bytes(), &line) == nil {
				lines = append(lines, line)
			}
		}
		return true
	}
	rotated := read(j.outputPath(id) + ".1")
	read(j.outputPath(id))
	return lines, rotated
}

// Restore loads the journal's records into m, as of restartedAt (when this
// daemon started). It returns how many it restored. Records m already holds
// are left alone.
func (j *BackgroundJournal) Restore(m *BackgroundManager, restartedAt time.Time) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		return 0, err
	}
	// Restore runs before this daemon journals anything (installBackgroundJournal
	// sets the callback after it), so every "running" record here is one the
	// restart lost — about to become stale — and the bound applies to it.
	records, err := j.loadRecordsLocked(false)
	if err != nil {
		return 0, err
	}
	j.lastPrune = j.now()

	restored := 0
	for _, rec := range records {
		lines, rotated := j.readOutputLocked(rec.ID)
		var notes []string
		if rec.Status == "running" {
			notes = append(notes, fmt.Sprintf(
				"[reliant] The daemon restarted at %s while this command was running, so it lost track of it: "+
					"its exit status and any later output are unknown. Re-run it if you still need its result.",
				restartedAt.UTC().Format(time.RFC3339)))
			end := restartedAt
			rec.Status = StatusStale
			rec.EndTime = &end
			if err := j.writeRecordLocked(rec); err != nil {
				logging.Warn("Background journal: could not mark record stale", "id", rec.ID, "error", err)
			}
		}

		var seq int64
		if n := len(lines); n > 0 {
			seq = lines[n-1].Sequence
		}
		var noteLines []OutputLineWithSeq
		for _, note := range notes {
			seq++
			noteLines = append(noteLines, OutputLineWithSeq{Type: "stderr", Text: note, Sequence: seq})
		}
		if len(noteLines) > 0 {
			// Journaled too, so a second restart still says why.
			if err := j.appendOutputLocked(rec.ID, noteLines); err != nil {
				logging.Warn("Background journal: could not record restart note", "id", rec.ID, "error", err)
			}
			lines = append(lines, noteLines...)
		}
		if rotated {
			first := int64(0)
			if len(lines) > 0 {
				first = lines[0].Sequence - 1
			}
			lines = append([]OutputLineWithSeq{{
				Type:     "stderr",
				Text:     "[reliant] Earlier output of this command was not kept across the daemon restart.",
				Sequence: first,
			}}, lines...)
		}

		if m.restoreRecord(rec, lines, seq) {
			restored++
		}
	}
	return restored, nil
}

// appendOutputLocked appends lines to id's output file, rotating it first
// when they would take it past journalSegmentBytes. The caller holds j.mu.
func (j *BackgroundJournal) appendOutputLocked(id string, lines []OutputLineWithSeq) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	path := j.outputPath(id)
	if info, err := os.Stat(path); err == nil && info.Size()+int64(buf.Len()) > journalSegmentBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(buf.Bytes())
	cerr := f.Close()
	return errors.Join(werr, cerr)
}

// restoreRecord adds an ended process, as the journal kept it, to the
// registry. It has no command or process tree: it can be listed, read and
// waited on (it has ended), never killed. False if m already has the id.
func (m *BackgroundManager) restoreRecord(rec journalRecord, lines []OutputLineWithSeq, seq int64) bool {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	for _, line := range lines {
		buf := stdout
		if line.Type == "stderr" {
			buf = stderr
		}
		buf.WriteString(line.Text)
		buf.WriteByte('\n')
	}
	done := make(chan struct{})
	close(done)
	process := &BackgroundProcess{
		ID:             rec.ID,
		Command:        rec.Command,
		Status:         rec.Status,
		StartTime:      rec.StartTime,
		EndTime:        rec.EndTime,
		ExitCode:       rec.ExitCode,
		WorkingDir:     rec.WorkingDir,
		WorktreeID:     rec.WorktreeID,
		PackageCommand: rec.PackageCommand,
		SessionID:      rec.SessionID,
		ChatID:         rec.ChatID,
		ThreadID:       rec.ThreadID,
		KeepAlive:      rec.KeepAlive,
		GrantID:        rec.GrantID,
		stdout:         stdout,
		stderr:         stderr,
		combinedOutput: lines,
		outputSeq:      seq,
		done:           done,
		subscribers:    make(map[string]chan OutputLineWithSeq),
	}
	if process.combinedOutput == nil {
		process.combinedOutput = make([]OutputLineWithSeq, 0)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.processes[rec.ID]; exists {
		return false
	}
	m.processes[rec.ID] = process
	return true
}
