// Copyright (c) 2026 Reliant Labs
//go:build !windows

package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
// (BackgroundJournal.Install).
//
// Nothing here waits on the clock. The commands signal the test through
// FIFOs (ready: "my output is written"; gate: "go on"), completion is the
// process's done channel, and an image deciding not to reap is the handoff's
// parked signal. The journal runs on a fixed clock. failureBound only fails
// a run whose event never came.

// testEpoch is the journal's clock and restart time in these tests.
var testEpoch = time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)

// failureBound fails a wait for an event that never came.
const failureBound = 30 * time.Second

// newTestJournal is a journal in dir on the fixed clock.
func newTestJournal(dir string) *BackgroundJournal {
	j := NewBackgroundJournal(dir)
	j.now = func() time.Time { return testEpoch }
	return j
}

// installJournal restores dir's journal into m (adopting what was handed off)
// and makes it m's persistence.
func installJournal(t *testing.T, m *BackgroundManager, dir string) int {
	t.Helper()
	n, err := newTestJournal(dir).Install(m, testEpoch)
	require.NoError(t, err)
	return n
}

// survivingManager is a manager journaled in dir, with file-backed output on.
func survivingManager(t *testing.T, dir string) *BackgroundManager {
	t.Helper()
	m := newTestBGManager()
	installJournal(t, m, dir)
	m.EnableFileOutput(dir)
	return m
}

// await fails the test unless ch closes within failureBound.
func await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(failureBound):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// awaitReturn runs fn, which blocks on an event, failing the test unless it
// returns within failureBound.
func awaitReturn(t *testing.T, what string, fn func() error) {
	t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- fn() }()
	select {
	case err := <-errc:
		require.NoError(t, err, what)
	case <-time.After(failureBound):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// fifo is a named pipe a command and the test meet at.
type fifo string

func newFIFO(t *testing.T, name string) fifo {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, syscall.Mkfifo(path, 0o600))
	return fifo(path)
}

// open lets a command blocked in `read _ < $fifo` go on. Opening a FIFO to
// write blocks until its reader has it open.
func (f fifo) open(t *testing.T) {
	t.Helper()
	awaitReturn(t, "the command to read the gate", func() error {
		w, err := os.OpenFile(string(f), os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		_, err = w.WriteString("go\n")
		return errors.Join(err, w.Close())
	})
}

// awaitSignal returns once a command ran `echo > $fifo`: everything it wrote
// before that is in its output files.
func (f fifo) awaitSignal(t *testing.T) {
	t.Helper()
	awaitReturn(t, "the command's ready signal", func() error {
		r, err := os.Open(string(f))
		if err != nil {
			return err
		}
		_, err = io.ReadAll(r)
		return errors.Join(err, r.Close())
	})
}

// waitDone waits for the process to be reported finished and returns its
// status and exit code.
func waitDone(t *testing.T, p *BackgroundProcess) (string, *int) {
	t.Helper()
	await(t, p.done, "process "+p.ID+" to finish")
	live := p.Live()
	return live.Status, live.ExitCode
}

func texts(t *testing.T, m *BackgroundManager, id string, afterSeq int64) []string {
	t.Helper()
	lines, _, err := m.GetCombinedOutputWithSeq(id, afterSeq)
	require.NoError(t, err)
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		require.Greater(t, l.Sequence, afterSeq)
		out = append(out, l.Text)
	}
	return out
}

func readJournalRecord(t *testing.T, dir, id string) journalRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	require.NoError(t, err)
	var rec journalRecord
	require.NoError(t, json.Unmarshal(data, &rec))
	return rec
}

func requireFiles(t *testing.T, dir, id string, exist bool, why string) {
	t.Helper()
	for _, f := range outputFilePaths(dir, id) {
		_, err := os.Stat(f)
		if exist {
			require.NoError(t, err, why)
		} else {
			require.ErrorIs(t, err, os.ErrNotExist, why)
		}
	}
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
	installJournal(t, before, dir)

	// Started before file output was on: piped, so the handoff stops it.
	piped, err := before.StartProcess(context.Background(), StartProcessOptions{
		Command:    "read _ < " + string(newFIFO(t, "piped-gate")),
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.False(t, piped.SurvivesSelfUpdate())
	before.EnableFileOutput(dir)

	ready, gate := newFIFO(t, "ready"), newFIFO(t, "gate")
	proc, err := before.StartProcess(context.Background(), StartProcessOptions{
		Command: fmt.Sprintf(`echo before; echo err-before >&2; printf partial; echo > %s; read _ < %s; echo " tail"; echo after; exit 7`,
			ready, gate),
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

	ready.awaitSignal(t)
	before.HandOffForExec() // the exec follows; this image is never used again
	seen, lastSeq, err := before.GetCombinedOutputWithSeq(proc.ID, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"before", "err-before"}, texts(t, before, proc.ID, 0), "the handoff read the output up to the last whole line")

	require.True(t, alive(pid), "a handed-off process keeps running")
	rec = readJournalRecord(t, dir, proc.ID)
	require.True(t, rec.HandedOff)
	require.Equal(t, int64(len("before\n")), rec.StdoutOffset, "the unterminated line is read again by the next image")
	require.Equal(t, int64(len("err-before\n")), rec.StderrOffset)
	require.Equal(t, "killed", piped.StatusSafe(), "a piped process cannot survive the exec and is stopped")

	after := newTestBGManager()
	require.GreaterOrEqual(t, installJournal(t, after, dir), 1)

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

	gate.open(t)
	status, exitCode := waitDone(t, adopted)
	require.Equal(t, "failed", status)
	require.NotNil(t, exitCode)
	require.Equal(t, 7, *exitCode, "the exit status is collected after the adoption")
	require.Equal(t, []string{"partial tail", "after"}, texts(t, after, proc.ID, lastSeq), "output goes on where the first image stopped")

	// Status changes are journaled before done closes.
	rec = readJournalRecord(t, dir, proc.ID)
	require.Equal(t, "failed", rec.Status)
	require.Equal(t, 7, *rec.ExitCode)
	requireFiles(t, dir, proc.ID, false, "the image that reaped it removes its output files: the journal has its output")
}

// The defect a handoff must never have: the old image reaping a process that
// exits between the handoff and the exec. It must leave the zombie, its exit
// status and its output files to the next image, which collects them.
func TestBackgroundExitBetweenHandOffAndExecIsLeftToTheNextImage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	before := survivingManager(t, dir)
	ready, gate := newFIFO(t, "ready"), newFIFO(t, "gate")
	proc, err := before.StartProcess(context.Background(), StartProcessOptions{
		Command:    fmt.Sprintf(`echo one; echo > %s; read _ < %s; echo two; exit 5`, ready, gate),
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	pid := proc.GetPID()
	ready.awaitSignal(t)

	before.HandOffForExec()
	gate.open(t) // the command exits now, before the "exec"
	await(t, proc.handoff.parkedSignal(), "the old image to see the exit and decline to reap")

	require.Equal(t, "running", proc.StatusSafe(), "the old image recorded no completion")
	select {
	case <-proc.done:
		t.Fatal("the old image completed a process it handed off")
	default:
	}
	id, ok := osutil.IdentifyProcess(pid)
	require.True(t, ok, "the exited process was not reaped")
	require.True(t, id.Exited, "it is a zombie, its exit status kept")
	requireFiles(t, dir, proc.ID, true, "the old image never removes a handed-off process's files")

	after := newTestBGManager()
	installJournal(t, after, dir)
	adopted, err := after.GetProcess(proc.ID)
	require.NoError(t, err)
	status, exitCode := waitDone(t, adopted)
	require.Equal(t, "failed", status)
	require.NotNil(t, exitCode)
	require.Equal(t, 5, *exitCode, "the next image collects the exit status")
	require.Equal(t, []string{"one", "two"}, texts(t, after, proc.ID, 0))
	requireFiles(t, dir, proc.ID, false, "the image that reaped it removes the files")
}

// A handoff the exec never followed (the swap failed) is taken back: this
// image tails and reaps the process again, whether it exited during the
// handoff or after it.
func TestBackgroundHandOffUndoneWhenTheExecFails(t *testing.T) {
	for _, exitDuringHandOff := range []bool{false, true} {
		t.Run(fmt.Sprintf("exit_during_handoff=%v", exitDuringHandOff), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "journal")
			m := survivingManager(t, dir)
			ready, gate := newFIFO(t, "ready"), newFIFO(t, "gate")
			proc, err := m.StartProcess(context.Background(), StartProcessOptions{
				Command:    fmt.Sprintf(`echo one; echo > %s; read _ < %s; echo two; exit 3`, ready, gate),
				WorkingDir: t.TempDir(),
			})
			require.NoError(t, err)
			ready.awaitSignal(t)

			resume := m.HandOffForExec()
			if exitDuringHandOff {
				gate.open(t)
				await(t, proc.handoff.parkedSignal(), "the image to decline to reap a handed-off process")
				requireFiles(t, dir, proc.ID, true, "files are kept while handed off")
			}
			resume()
			if !exitDuringHandOff {
				gate.open(t)
			}

			status, exitCode := waitDone(t, proc)
			require.Equal(t, "failed", status)
			require.NotNil(t, exitCode)
			require.Equal(t, 3, *exitCode)
			require.Equal(t, []string{"one", "two"}, texts(t, m, proc.ID, 0))
			requireFiles(t, dir, proc.ID, false, "reaped here again, so removed here")
		})
	}
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
		ID: recID, Command: "dev server", Status: "running", StartTime: testEpoch,
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
	installJournal(t, m, dir)
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
	waited := make(chan struct{})
	go func() { _ = orphan.Wait(); close(waited) }()
	t.Cleanup(func() { _ = orphan.Process.Kill() })
	id, ok := osutil.IdentifyProcess(orphan.Process.Pid)
	require.True(t, ok)

	const recID = "crash-orphan"
	rec := journalRecord{
		ID: recID, Command: "dev server", Status: "running", StartTime: testEpoch,
		PID: orphan.Process.Pid, PGID: orphan.Process.Pid, StartTicks: id.Start, FileOutput: true,
	}
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, recID+".json"), data, 0o600))

	m := newTestBGManager()
	installJournal(t, m, dir)
	got, err := m.GetProcess(recID)
	require.NoError(t, err)
	require.Equal(t, StatusStale, got.StatusSafe())
	await(t, waited, "the orphan to be stopped")
}

// A normal stop is unchanged: KillAllRunning kills a file-backed process
// too, and the journal says so.
func TestBackgroundFileOutputProcessKilledOnNormalShutdown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	m := survivingManager(t, dir)
	ready, gate := newFIFO(t, "ready"), newFIFO(t, "gate")
	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    fmt.Sprintf(`echo up; echo > %s; read _ < %s`, ready, gate),
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.True(t, proc.SurvivesSelfUpdate())
	pid := proc.GetPID()
	ready.awaitSignal(t)

	m.KillAllRunning()

	require.Equal(t, "killed", proc.StatusSafe())
	require.False(t, alive(pid), "a normal stop kills it")
	require.Equal(t, "killed", readJournalRecord(t, dir, proc.ID).Status, "the kill is journaled before KillAllRunning returns")
	requireFiles(t, dir, proc.ID, false, "reaped here, so its files are removed")
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
	status, exitCode := waitDone(t, proc)
	require.Equal(t, "completed", status)
	require.Equal(t, 0, *exitCode)
	stdout, stderr, err := m.GetOutput(proc.ID)
	require.NoError(t, err)
	require.Equal(t, "line-1\nline-2\nline-3\nno newline\n", stdout)
	require.Equal(t, "oops\n", stderr)
}

// failHandOff makes m's journal refuse every handoff (a full disk), running
// during first, inside the window between the handoff's output read and the
// undo.
func failHandOff(m *BackgroundManager, dir string, during func()) {
	cb := newTestJournal(dir).Callback()
	cb.OnHandOff = func(string, [2]int64) error {
		during()
		return errors.New("no space left on device")
	}
	m.SetPersistenceCallback(cb)
}

// A handoff the journal refuses is undone, and then the process is stopped,
// as any process that cannot survive the exec is. One reaper throughout: the
// stop only signals and waits for the process's own waiter, which reaps it
// once and only then removes its files.
func TestBackgroundFailedHandOffOfALiveProcessIsStoppedByItsOneReaper(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	m := survivingManager(t, dir)
	ready, gate := newFIFO(t, "ready"), newFIFO(t, "gate")
	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    fmt.Sprintf(`echo one; echo > %s; read _ < %s`, ready, gate),
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	pid := proc.GetPID()
	ready.awaitSignal(t)
	failHandOff(m, dir, func() {
		require.True(t, alive(pid), "the process is alive when the handoff fails")
		requireFiles(t, dir, proc.ID, true, "files are kept during a handoff")
		require.Zero(t, proc.handoff.reapCount())
	})

	m.HandOffForExec()

	status, _ := waitDone(t, proc)
	require.Equal(t, "killed", status, "a process that cannot be handed off is stopped")
	require.False(t, alive(pid))
	require.Equal(t, 1, proc.handoff.reapCount(), "reaped exactly once, by its own waiter")
	requireFiles(t, dir, proc.ID, false, "removed by the one reaper, after it reaped")
	rec := readJournalRecord(t, dir, proc.ID)
	require.False(t, rec.HandedOff)
	require.Equal(t, "killed", rec.Status)
	require.Equal(t, []string{"one"}, texts(t, m, proc.ID, 0))
}

// A process that exits inside the window of a handoff the journal refuses is
// not stopped: the undo restarts its parked waiter, which reaps it once with
// its own exit status; its files stay until then.
func TestBackgroundFailedHandOffOfAProcessThatExitsInTheWindowIsReapedOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	m := survivingManager(t, dir)
	ready, gate := newFIFO(t, "ready"), newFIFO(t, "gate")
	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    fmt.Sprintf(`echo one; echo > %s; read _ < %s; echo two; exit 3`, ready, gate),
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	pid := proc.GetPID()
	ready.awaitSignal(t)
	failHandOff(m, dir, func() {
		gate.open(t)
		awaitReturn(t, "the child to exit", func() error { return waitExited(pid) })
		await(t, proc.handoff.parkedSignal(), "the waiter to decline to reap during the handoff")
		require.Zero(t, proc.handoff.reapCount(), "not reaped while handing off")
		requireFiles(t, dir, proc.ID, true, "files are kept until the reap")
		select {
		case <-proc.done:
			t.Fatal("completed while handing off")
		default:
		}
	})

	m.HandOffForExec()

	status, exitCode := waitDone(t, proc)
	require.Equal(t, "failed", status, "it exited by itself: not stopped, not marked killed")
	require.NotNil(t, exitCode)
	require.Equal(t, 3, *exitCode)
	require.Equal(t, 1, proc.handoff.reapCount(), "reaped exactly once, by its restarted waiter")
	requireFiles(t, dir, proc.ID, false, "removed by the one reaper, after it reaped")
	require.Equal(t, []string{"one", "two"}, texts(t, m, proc.ID, 0))
	rec := readJournalRecord(t, dir, proc.ID)
	require.False(t, rec.HandedOff)
	require.Equal(t, "failed", rec.Status)
	require.Equal(t, 3, *rec.ExitCode)
}
