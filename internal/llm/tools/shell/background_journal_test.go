// Copyright (c) 2026 Reliant Labs
//go:build !windows

package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBackgroundJournal_FinishedProcessSurvivesRestart pins the defect of
// 2026-10-30: a daemon restart erased a finished test run's record, so
// shell_wait answered "No process with ID" and the verdict was lost. The
// journal keeps it: a new manager restored from the same directory reports the
// same status, exit code and output.
func TestBackgroundJournal_FinishedProcessSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	journal := NewBackgroundJournal(dir)
	if _, err := journal.Restore(newTestBGManager(), time.Now()); err != nil {
		t.Fatalf("Restore (empty): %v", err)
	}

	before := newTestBGManager()
	before.SetPersistenceCallback(journal.Callback())
	proc, err := before.StartProcess(context.Background(), StartProcessOptions{
		Command:    "echo first; echo second; echo third >&2; exit 3",
		WorkingDir: t.TempDir(),
		ChatID:     "chat-1",
		GrantID:    "grant-1",
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	if status, _ := awaitCompletion(t, before, proc.ID, 10*time.Second); status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	// The final output flush runs once the process is done; wait for it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(dir, proc.ID+".out"))
		if strings.Contains(string(data), "second") && strings.Contains(string(data), "third") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("output never journaled; .out holds %q", data)
		}
		time.Sleep(10 * time.Millisecond)
	}

	after := newTestBGManager()
	n, err := NewBackgroundJournal(dir).Restore(after, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("Restore = %d, %v; want 1, nil", n, err)
	}
	got, err := after.GetProcessForGrant(proc.ID, "grant-1")
	if err != nil {
		t.Fatalf("restored process not found: %v", err)
	}
	if got.StatusSafe() != "failed" || got.ExitCode == nil || *got.ExitCode != 3 || got.EndTime == nil {
		t.Fatalf("restored status=%q exit=%v end=%v; want failed, 3, set", got.StatusSafe(), got.ExitCode, got.EndTime)
	}
	if got.ChatID != "chat-1" {
		t.Fatalf("restored ChatID = %q", got.ChatID)
	}
	// Grant scoping survives the restart.
	if _, err := after.GetProcessForGrant(proc.ID, "other-grant"); err == nil {
		t.Fatal("a different grant can read the restored process")
	}
	// Lines of one stream keep their order; the two streams interleave as
	// they were read.
	if texts := combinedText(t, after, proc.ID); len(texts) != 3 {
		t.Fatalf("restored output = %q", texts)
	}
	stdout, stderr, _ := after.GetOutput(proc.ID)
	if stdout != "first\nsecond\n" || stderr != "third\n" {
		t.Fatalf("restored stdout=%q stderr=%q", stdout, stderr)
	}
	// A restored process has ended: it is never killable, and says why.
	if err := after.KillProcess(proc.ID); err == nil {
		t.Fatal("KillProcess on a restored record succeeded")
	}
}

// TestBackgroundJournal_RunningRecordComesBackStale: a record still running in
// the journal is a process the restart lost. It comes back ended, as "stale",
// with a line saying so — once, however many restarts follow.
func TestBackgroundJournal_RunningRecordComesBackStale(t *testing.T) {
	dir := t.TempDir()
	journal := NewBackgroundJournal(dir)
	proc := &BackgroundProcess{ID: "11111111-2222-3333-4444-555555555555", Command: "go test ./...", Status: "running", StartTime: time.Now().Add(-time.Minute)}
	if err := journal.recordStart(proc); err != nil {
		t.Fatalf("recordStart: %v", err)
	}
	if err := journal.appendOutput(proc.ID, []OutputLineWithSeq{{Type: "stdout", Text: "ok  pkg/a", Sequence: 1}}); err != nil {
		t.Fatalf("appendOutput: %v", err)
	}

	restartedAt := time.Now()
	for restart := 0; restart < 2; restart++ {
		m := newTestBGManager()
		if n, err := NewBackgroundJournal(dir).Restore(m, restartedAt); err != nil || n != 1 {
			t.Fatalf("restart %d: Restore = %d, %v", restart, n, err)
		}
		status, complete, exitCode, err := m.GetProcessStatus(proc.ID)
		if err != nil || !complete || status != StatusStale || exitCode != nil {
			t.Fatalf("restart %d: status=%q complete=%v exit=%v err=%v; want stale, ended, no exit code", restart, status, complete, exitCode, err)
		}
		texts := combinedText(t, m, proc.ID)
		if len(texts) != 2 || texts[0] != "ok  pkg/a" || !strings.Contains(texts[1], "daemon restarted") {
			t.Fatalf("restart %d: output = %q; want the run's line then one restart note", restart, texts)
		}
		// Waiting on it returns at once.
		p, _ := m.GetProcess(proc.ID)
		select {
		case <-p.done:
		default:
			t.Fatalf("restart %d: a restored process is not done", restart)
		}
	}
}

// TestBackgroundJournal_Retention: an ended record past retention is deleted,
// files and all; a recent one is kept.
func TestBackgroundJournal_Retention(t *testing.T) {
	dir := t.TempDir()
	journal := NewBackgroundJournal(dir)
	old := time.Now().Add(-journalRetention - time.Hour)
	recent := time.Now().Add(-time.Hour)
	for id, end := range map[string]time.Time{"old-0000": old, "recent-0000": recent} {
		end := end
		if err := journal.recordStart(&BackgroundProcess{ID: id, Status: "completed", StartTime: end.Add(-time.Minute), EndTime: &end}); err != nil {
			t.Fatal(err)
		}
		if err := journal.appendOutput(id, []OutputLineWithSeq{{Type: "stdout", Text: "x", Sequence: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	m := newTestBGManager()
	if n, err := journal.Restore(m, time.Now()); err != nil || n != 1 {
		t.Fatalf("Restore = %d, %v; want 1", n, err)
	}
	if _, err := m.GetProcess("recent-0000"); err != nil {
		t.Fatalf("recent record not restored: %v", err)
	}
	for _, name := range []string{"old-0000.json", "old-0000.out"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived retention (err=%v)", name, err)
		}
	}
}

// TestBackgroundJournal_RecordBound: past journalMaxRecords, a prune while the
// daemon runs keeps every running record and fills the rest of the bound with
// the newest ended ones — never an older ended record over a newer one — and a
// restore, where no running record is live any more, keeps exactly the newest
// journalMaxRecords.
func TestBackgroundJournal_RecordBound(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	journal := NewBackgroundJournal(dir)
	journal.now = func() time.Time { return base }

	// 120 ended records, newest at i=0, and 10 running ones older than all of
	// them, so a newest-first cut alone would drop every running record.
	const ended, running = 120, 10
	id := func(kind string, i int) string { return fmt.Sprintf("%s-%04d", kind, i) }
	for i := 0; i < ended; i++ {
		start := base.Add(-time.Duration(i+1) * time.Minute)
		end := start.Add(time.Second)
		if err := journal.recordStart(&BackgroundProcess{ID: id("ended", i), Status: "completed", StartTime: start, EndTime: &end}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < running; i++ {
		start := base.Add(-time.Duration(ended+i+1) * time.Minute)
		if err := journal.recordStart(&BackgroundProcess{ID: id("running", i), Status: "running", StartTime: start}); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(id string) bool {
		_, err := os.Stat(filepath.Join(dir, id+".json"))
		return err == nil
	}

	journal.mu.Lock()
	err := journal.pruneLocked()
	journal.mu.Unlock()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for i := 0; i < running; i++ {
		if !exists(id("running", i)) {
			t.Fatalf("prune dropped running record %s", id("running", i))
		}
	}
	keptEnded := journalMaxRecords - running
	for i := 0; i < ended; i++ {
		if got, want := exists(id("ended", i)), i < keptEnded; got != want {
			t.Fatalf("after prune, ended record %d exists=%v; want %v (the newest %d ended are kept)", i, got, want, keptEnded)
		}
	}

	// Add ended records back so the journal is over the bound again, then
	// restore: the running records are now lost processes, and the bound is
	// strict.
	for i := keptEnded; i < ended; i++ {
		start := base.Add(-time.Duration(i+1) * time.Minute)
		end := start.Add(time.Second)
		if err := journal.recordStart(&BackgroundProcess{ID: id("ended", i), Status: "completed", StartTime: start, EndTime: &end}); err != nil {
			t.Fatal(err)
		}
	}
	m := newTestBGManager()
	n, err := journal.Restore(m, base)
	if err != nil || n != journalMaxRecords {
		t.Fatalf("Restore = %d, %v; want %d", n, err, journalMaxRecords)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	records := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			records++
		}
	}
	if records != journalMaxRecords {
		t.Fatalf("journal holds %d records after restore; want %d", records, journalMaxRecords)
	}
	for i := 0; i < ended; i++ {
		if got, want := exists(id("ended", i)), i < journalMaxRecords; got != want {
			t.Fatalf("after restore, ended record %d exists=%v; want %v", i, got, want)
		}
	}
}

// TestBackgroundJournal_OutputKeepsTheEnd: past a segment the output rotates,
// so a long run keeps its end — where the verdict is — and says the start was
// dropped.
func TestBackgroundJournal_OutputKeepsTheEnd(t *testing.T) {
	dir := t.TempDir()
	journal := NewBackgroundJournal(dir)
	end := time.Now()
	id := "long-run-0000"
	if err := journal.recordStart(&BackgroundProcess{ID: id, Status: "completed", StartTime: end, EndTime: &end}); err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("y", 1000)
	var seq int64
	for i := 0; i < 3*journalSegmentBytes/len(line); i++ {
		seq++
		if err := journal.appendOutput(id, []OutputLineWithSeq{{Type: "stdout", Text: line, Sequence: seq}}); err != nil {
			t.Fatal(err)
		}
	}
	seq++
	if err := journal.appendOutput(id, []OutputLineWithSeq{{Type: "stdout", Text: "PASS", Sequence: seq}}); err != nil {
		t.Fatal(err)
	}

	m := newTestBGManager()
	if _, err := journal.Restore(m, time.Now()); err != nil {
		t.Fatal(err)
	}
	texts := combinedText(t, m, id)
	if texts[len(texts)-1] != "PASS" {
		t.Fatalf("last restored line = %q, want PASS", texts[len(texts)-1])
	}
	if !strings.Contains(texts[0], "Earlier output") {
		t.Fatalf("first restored line = %q, want the dropped-output note", texts[0])
	}
	if int64(len(texts)) > seq/2 {
		t.Fatalf("restored %d of %d lines; rotation kept too much", len(texts), seq)
	}
}

// TestBackgroundJournal_UnjournalableIDsTouchNothing: an id that is not a
// plain token never becomes a path.
func TestBackgroundJournal_UnjournalableIDsTouchNothing(t *testing.T) {
	dir := t.TempDir()
	journal := NewBackgroundJournal(filepath.Join(dir, "j"))
	if _, err := journal.Restore(newTestBGManager(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := journal.recordStart(&BackgroundProcess{ID: "../escape", Status: "running"}); err == nil {
		t.Fatal("recordStart accepted a path-like id")
	}
	_ = journal.appendOutput("../escape", []OutputLineWithSeq{{Text: "x"}})
	if _, err := os.Stat(filepath.Join(dir, "escape.json")); !os.IsNotExist(err) {
		t.Fatal("a path-like id escaped the journal directory")
	}
}

// TestBackgroundJournal_MissingDirectoryIsNotRecreated: a journal whose
// directory was removed records nothing rather than recreate it, and never
// fails the command it was asked to record.
func TestBackgroundJournal_MissingDirectoryIsNotRecreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gone")
	cb := NewBackgroundJournal(dir).Callback()
	err := cb.OnCreate(&BackgroundProcess{ID: "abc-123", Status: "running"})
	if err == nil {
		t.Fatal("OnCreate reported success with no directory")
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatal("OnCreate recreated the journal directory")
	}
	if cb.OnStatusChange("abc-123", "completed", nil, nil) != nil || cb.OnOutput("abc-123", []OutputLineWithSeq{{Text: "x"}}) != nil {
		t.Fatal("a journal write failure surfaced as an error")
	}
}
