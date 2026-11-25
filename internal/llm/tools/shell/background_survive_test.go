// Copyright (c) 2026 Reliant Labs
//go:build !windows

package shell

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/osutil"
)

// A self-update execs the new binary in the daemon's own process, so in one
// test process two managers sharing a journal directory are the image before
// the exec and the image after it: the first hands its processes off
// (HandOffForExec) and is never used again, the second adopts them
// (BackgroundJournal.Install). The commands wait on a FIFO the test opens,
// so what happens before and after the "exec" is decided by the test, never
// by a sleep.

// survivingManager is a manager journaled in dir, with file-backed output on.
func survivingManager(t *testing.T, dir string) *BackgroundManager {
	t.Helper()
	m := newTestBGManager()
	if _, err := NewBackgroundJournal(dir).Install(m, time.Now()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	m.EnableFileOutput(dir)
	return m
}

// gate is a FIFO a command blocks on (`read _ < $GATE`) until open is called.
type gate string

func newGate(t *testing.T) gate {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gate")
	require.NoError(t, syscall.Mkfifo(path, 0o600))
	return gate(path)
}

// open lets the command reading the gate go on. Opening a FIFO to write
// blocks until its reader has it open, so this returns once the command is
// past the point it waited at.
func (g gate) open(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile(string(g), os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("go\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// waitFor polls cond until it holds; the deadline only bounds a broken run.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func outputHas(m *BackgroundManager, id string, texts ...string) bool {
	lines, _, err := m.GetCombinedOutputWithSeq(id, 0)
	if err != nil {
		return false
	}
	have := map[string]bool{}
	for _, l := range lines {
		have[l.Text] = true
	}
	for _, text := range texts {
		if !have[text] {
			return false
		}
	}
	return true
}

func readJournalRecord(t *testing.T, dir, id string) journalRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	require.NoError(t, err)
	var rec journalRecord
	require.NoError(t, json.Unmarshal(data, &rec))
	return rec
}

func alive(pid int) bool {
	id, ok := osutil.IdentifyProcess(pid)
	return ok && !id.Exited
}

// A file-backed process outlives the image that started it: handed off, it
// keeps running across the "exec", the next image adopts it under the same
// id with its output and sequence numbers, reads on where the first stopped
// (an unterminated line included), and collects its real exit status. A
// piped process cannot survive the exec and is stopped by the handoff.
func TestBackgroundProcessSurvivesSelfUpdate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	before := newTestBGManager()
	_, err := NewBackgroundJournal(dir).Install(before, time.Now())
	require.NoError(t, err)

	// Started before file output was on: piped, so the handoff stops it.
	pipedGate := newGate(t)
	piped, err := before.StartProcess(context.Background(), StartProcessOptions{
		Command:    "read _ < " + string(pipedGate),
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.False(t, piped.SurvivesSelfUpdate())
	before.EnableFileOutput(dir)

	g := newGate(t)
	proc, err := before.StartProcess(context.Background(), StartProcessOptions{
		Command:    `echo before; echo err-before >&2; printf partial; read _ < ` + string(g) + `; echo " tail"; echo after; exit 7`,
		WorkingDir: t.TempDir(),
		ChatID:     "chat-1",
	})
	require.NoError(t, err)
	require.True(t, proc.SurvivesSelfUpdate())
	pid := proc.GetPID()
	rec := readJournalRecord(t, dir, proc.ID)
	require.True(t, rec.FileOutput)
	require.Equal(t, pid, rec.PID)
	require.NotZero(t, rec.StartTicks, "the journal records the process start time")
	require.Equal(t, pid, rec.PGID, "the command leads its own process group")

	waitFor(t, "the output before the handoff", func() bool { return outputHas(before, proc.ID, "before", "err-before") })
	seen, lastSeq, err := before.GetCombinedOutputWithSeq(proc.ID, 0)
	require.NoError(t, err)

	before.HandOffForExec() // the exec follows; this image is never used again

	require.True(t, alive(pid), "a handed-off process keeps running")
	rec = readJournalRecord(t, dir, proc.ID)
	require.True(t, rec.HandedOff)
	require.Equal(t, int64(len("before\n")), rec.StdoutOffset, "the unterminated line is read again by the next image")
	require.Equal(t, int64(len("err-before\n")), rec.StderrOffset)
	require.Equal(t, "killed", piped.StatusSafe(), "a piped process cannot survive the exec and is stopped")

	after := newTestBGManager()
	n, err := NewBackgroundJournal(dir).Install(after, time.Now())
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)

	adopted, err := after.GetProcess(proc.ID)
	require.NoError(t, err)
	require.Equal(t, "running", adopted.StatusSafe())
	require.Equal(t, pid, adopted.GetPID())
	require.Equal(t, "chat-1", adopted.ChatID)
	require.True(t, adopted.SurvivesSelfUpdate(), "an adopted process can be handed off again")
	resumed, resumedSeq, err := after.GetCombinedOutputWithSeq(proc.ID, 0)
	require.NoError(t, err)
	require.Equal(t, seen, resumed, "the output so far is kept, with its sequence numbers")
	require.Equal(t, lastSeq, resumedSeq)
	require.False(t, readJournalRecord(t, dir, proc.ID).HandedOff, "the handoff is spent")

	g.open(t)
	status, exitCode := awaitCompletion(t, after, proc.ID, 30*time.Second)
	require.Equal(t, "failed", status)
	require.NotNil(t, exitCode)
	require.Equal(t, 7, *exitCode, "the exit status is collected after the adoption")

	more, _, err := after.GetCombinedOutputWithSeq(proc.ID, lastSeq)
	require.NoError(t, err)
	var texts []string
	for _, l := range more {
		require.Greater(t, l.Sequence, lastSeq)
		texts = append(texts, l.Text)
	}
	require.Equal(t, []string{"partial tail", "after"}, texts, "output goes on where the first image stopped")

	waitFor(t, "the journal to record the exit", func() bool {
		r := readJournalRecord(t, dir, proc.ID)
		return r.Status == "failed" && r.ExitCode != nil && *r.ExitCode == 7
	})
	files := outputFilePaths(dir, proc.ID)
	for _, f := range files {
		_, err := os.Stat(f)
		require.ErrorIs(t, err, os.ErrNotExist, "an ended process's output files are removed: the journal has its output")
	}
}

// A handoff the exec never followed (the swap failed) is taken back: this
// image tails and reaps the process again.
func TestBackgroundHandOffUndoneWhenTheExecFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	m := survivingManager(t, dir)
	g := newGate(t)
	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    `echo one; read _ < ` + string(g) + `; echo two; exit 3`,
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	waitFor(t, "the first line", func() bool { return outputHas(m, proc.ID, "one") })

	resume := m.HandOffForExec()
	resume()

	g.open(t)
	status, exitCode := awaitCompletion(t, m, proc.ID, 30*time.Second)
	require.Equal(t, "failed", status)
	require.NotNil(t, exitCode)
	require.Equal(t, 3, *exitCode)
	require.True(t, outputHas(m, proc.ID, "one", "two"))
}

// A handed-off record whose pid now belongs to another process (a different
// start time: the pid was reused) is not adopted, and that process is left
// alone: the record is marked stale, as after any restart.
func TestBackgroundHandOffNotAdoptedOnPidReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	other := exec.Command("sleep", "600")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, other.Start())
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	id, ok := osutil.IdentifyProcess(other.Process.Pid)
	require.True(t, ok)

	const recID = "reused-pid-record"
	rec := journalRecord{
		ID: recID, Command: "dev server", Status: "running", StartTime: time.Now(),
		PID: other.Process.Pid, PGID: other.Process.Pid, StartTicks: id.Start + 1,
		FileOutput: true, HandedOff: true,
	}
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, recID+".json"), data, 0o600))
	for _, f := range outputFilePaths(dir, recID) {
		require.NoError(t, os.WriteFile(f, nil, 0o600))
	}

	m := newTestBGManager()
	_, err = NewBackgroundJournal(dir).Install(m, time.Now())
	require.NoError(t, err)
	got, err := m.GetProcess(recID)
	require.NoError(t, err)
	require.Equal(t, StatusStale, got.StatusSafe(), "another process holds the pid: not adopted")
	require.True(t, alive(other.Process.Pid), "and that process is not touched")
}

// After a crash the next daemon is a new process and adopts nothing; a
// file-backed process the crash left running would run on untracked (no
// pipe breaks under it), so it is stopped, while it provably is the one
// journaled.
func TestBackgroundOrphanOfACrashIsStopped(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	orphan := exec.Command("sleep", "600")
	orphan.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, orphan.Start())
	waited := make(chan error, 1)
	go func() { waited <- orphan.Wait() }()
	t.Cleanup(func() { _ = orphan.Process.Kill() })
	id, ok := osutil.IdentifyProcess(orphan.Process.Pid)
	require.True(t, ok)

	const recID = "crash-orphan"
	rec := journalRecord{
		ID: recID, Command: "dev server", Status: "running", StartTime: time.Now(),
		PID: orphan.Process.Pid, PGID: orphan.Process.Pid, StartTicks: id.Start, FileOutput: true,
	}
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, recID+".json"), data, 0o600))

	m := newTestBGManager()
	_, err = NewBackgroundJournal(dir).Install(m, time.Now())
	require.NoError(t, err)
	got, err := m.GetProcess(recID)
	require.NoError(t, err)
	require.Equal(t, StatusStale, got.StatusSafe())
	select {
	case <-waited:
	case <-time.After(30 * time.Second):
		t.Fatal("the orphan was not stopped")
	}
}

// A normal stop is unchanged: KillAllRunning kills a file-backed process
// too, and the journal says so.
func TestBackgroundFileOutputProcessKilledOnNormalShutdown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	m := survivingManager(t, dir)
	g := newGate(t)
	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    `echo up; read _ < ` + string(g),
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.True(t, proc.SurvivesSelfUpdate())
	pid := proc.GetPID()
	waitFor(t, "the first line", func() bool { return outputHas(m, proc.ID, "up") })

	m.KillAllRunning()

	require.Equal(t, "killed", proc.StatusSafe())
	require.False(t, alive(pid), "a normal stop kills it")
	waitFor(t, "the journal to record the kill", func() bool {
		return readJournalRecord(t, dir, proc.ID).Status == "killed"
	})
}

// File-backed output reads like piped output: every line, split by stream,
// an unterminated last line kept, and complete once the process is reported
// finished.
func TestBackgroundFileOutputIsCompleteAtCompletion(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	m := survivingManager(t, dir)
	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    `for i in 1 2 3; do echo "line-$i"; done; echo oops >&2; printf 'no newline'`,
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	status, exitCode := awaitCompletion(t, m, proc.ID, 30*time.Second)
	require.Equal(t, "completed", status)
	require.Equal(t, 0, *exitCode)
	stdout, stderr, err := m.GetOutput(proc.ID)
	require.NoError(t, err)
	require.Equal(t, "line-1\nline-2\nline-3\nno newline\n", stdout)
	require.Equal(t, "oops\n", stderr)
	require.False(t, strings.Contains(stdout, "oops"))
}
