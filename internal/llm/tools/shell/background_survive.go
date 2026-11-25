// Copyright (c) 2026 Reliant Labs
package shell

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/osutil"
	"github.com/reliant-labs/reliant/internal/proctree"
)

// Background processes across a self-update (forge
// docs/proposals/self-upgrade.md §5, phase R5).
//
// An auto-updating daemon installs a release by exec'ing the new binary in
// its own process (cliupdate.Daemon.ExecPending): the pid stays, so the
// commands it started are still its children. One that is file-backed
// (background_fileoutput.go) loses nothing to the exec either, so it is not
// stopped:
//
//  1. Hand off (HandOffForExec, just before the exec): its output is read up
//     to the last whole line and journaled, and the offsets the next image
//     reads on from are journaled with it. Everything else is stopped, as at
//     any restart: a piped process, a credentials-none one (it dies with the
//     daemon, by decision of 2026-11-10), an adopted foreground command.
//  2. The exit is watched without reaping (waitExited), so one that exits
//     between the handoff and the exec stays a zombie whose status the next
//     image collects.
//  3. Re-adopt (BackgroundJournal.Install, in the next image): a handed-off
//     record whose pid is alive, has this process as its parent and has the
//     start time journaled for it is the same process. It keeps its id,
//     output and sequence numbers, is tailed from the handed-off offsets,
//     reaped with wait4 for its real exit status, and can be killed.
//
// Nothing else changes: a normal stop kills them all (KillAllRunning), and
// after a crash or a stop the next daemon is a new process, which never
// adopts. It stops a file-backed process the crash left running instead
// (stopOrphan), since without a pipe to break nothing else would: a piped
// one dies of EPIPE.

// EnableFileOutput makes the processes started from now on write their output
// to files in dir (the journal's directory), so they can survive an in-place
// restart. A no-op where that cannot work (Windows).
func (m *BackgroundManager) EnableFileOutput(dir string) {
	if !fileOutputSupported || dir == "" {
		return
	}
	m.mu.Lock()
	m.fileOutDir = dir
	m.mu.Unlock()
}

// SurvivesSelfUpdate reports whether the process keeps running across this
// daemon restarting in place: a file-backed one, which HandOffForExec hands
// to the next image instead of stopping. A process that is not running
// answers false.
func (p *BackgroundProcess) SurvivesSelfUpdate() bool {
	return p.fileOut != nil && p.StatusSafe() == "running"
}

// attachOutput gives cmd its output: files when the manager has file output
// on and the command can outlive this image, else the pipe writers os/exec
// copies into. started closes the child's side of the files once cmd has
// started (or discards them when it never did).
func (m *BackgroundManager) attachOutput(process *BackgroundProcess, cmd *exec.Cmd) (pipes []*outputLineWriter, started func(err error)) {
	m.mu.RLock()
	dir := m.fileOutDir
	m.mu.RUnlock()
	// A credentials-none command dies with the daemon image whatever its
	// output is (credsandbox's init watches a pipe the exec closes): pipes.
	if dir != "" && !credsandbox.IsInit(cmd) {
		child, out, err := createFileOutput(dir, process.ID)
		if err == nil {
			out.bind(process)
			process.fileOut = out
			cmd.Stdout, cmd.Stderr = child[0], child[1]
			return nil, func(err error) {
				closeFiles(child[:])
				if err != nil {
					out.discard()
					process.fileOut = nil
				}
			}
		}
		logging.Warn("Background process output files could not be made; it uses pipes and will not survive a self-update", "id", process.ID, "error", err)
	}
	stdoutLines := &outputLineWriter{process: process, buffer: process.stdout, outputType: "stdout"}
	stderrLines := &outputLineWriter{process: process, buffer: process.stderr, outputType: "stderr"}
	cmd.Stdout = stdoutLines
	cmd.Stderr = stderrLines
	cmd.WaitDelay = BackgroundWaitDelay
	return []*outputLineWriter{stdoutLines, stderrLines}, func(error) {}
}

// handoffState is a file-backed process's part in a self-update: handed off
// to the next image, or being reaped by this one. Never both.
type handoffState struct {
	mu        sync.Mutex
	handedOff bool
	reaping   bool
	resumed   chan struct{} // closed when a handoff is undone
}

// claimReap is the waiter's: nil when this image may reap the process, else
// the channel that closes if the handoff is undone.
func (h *handoffState) claimReap() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handedOff {
		return h.resumed
	}
	h.reaping = true
	return nil
}

// begin hands the process off, unless this image is already reaping it.
func (h *handoffState) begin() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reaping {
		return false
	}
	if !h.handedOff {
		h.handedOff = true
		h.resumed = make(chan struct{})
	}
	return true
}

// undo takes a handoff back: the exec did not happen.
func (h *handoffState) undo() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handedOff {
		h.handedOff = false
		close(h.resumed)
	}
}

// waitForFileProcess waits for a file-backed process. It sees the exit
// without reaping (waitExited), and reaps with reap (cmd.Wait, or
// os.Process.Wait for an adopted process) only when the process was not
// handed off: a handed-off one is the next image's to reap.
func (m *BackgroundManager) waitForFileProcess(process *BackgroundProcess, reap func() error) {
	pid := process.GetPID()
	for {
		if err := waitExited(pid); err != nil {
			logging.Debug("Background process exit not observable; reaping it", "id", process.ID, "pid", pid, "error", err)
		}
		resumed := process.handoff.claimReap()
		if resumed == nil {
			break
		}
		<-resumed
	}
	err := reap()
	process.recordSurvivors()
	process.tree.Release()
	process.fileOut.finish()
	m.handleProcessCompletion(process, err)
}

// reapAdopted reaps an adopted process: it is this process's child, so wait4
// gives its exit status, as an *exec.ExitError the way cmd.Wait reports one.
func reapAdopted(proc *os.Process) func() error {
	return func() error {
		state, err := proc.Wait()
		if err != nil {
			return err
		}
		if !state.Success() {
			return &exec.ExitError{ProcessState: state}
		}
		return nil
	}
}

// flushProcessOutput journals the process's output lines not journaled yet.
func (m *BackgroundManager) flushProcessOutput(process *BackgroundProcess) {
	process.flushMu.Lock()
	defer process.flushMu.Unlock()
	process.flushedSeq = m.flushNewOutputLines(process, process.flushedSeq)
}

// HandOffForExec readies the background processes for this process to exec
// a new image of itself (a self-update): each file-backed one is handed off
// to the next image, which adopts it (BackgroundJournal.Install), and every
// other one is stopped, as KillAllRunning would. Call it last before the
// exec. resume takes the handoff back when the exec failed and this image
// keeps running: the processes are tailed and waited for here again.
func (m *BackgroundManager) HandOffForExec() (resume func()) {
	m.persistenceMu.RLock()
	cb := m.persistenceCallback
	m.persistenceMu.RUnlock()

	m.mu.RLock()
	candidates := make([]*BackgroundProcess, 0, len(m.processes))
	for _, process := range m.processes {
		candidates = append(candidates, process)
	}
	m.mu.RUnlock()

	var stop []string
	var handed []*BackgroundProcess
	for _, process := range candidates {
		if process.StatusSafe() != "running" {
			continue
		}
		if process.fileOut != nil && cb != nil && cb.OnHandOff != nil {
			if handedOff, ended := m.handOff(process, cb); handedOff || ended {
				if handedOff {
					handed = append(handed, process)
				}
				continue
			}
		}
		stop = append(stop, process.ID)
	}
	if len(handed) > 0 {
		logging.Info("Background processes handed off to the next daemon image", "count", len(handed))
	}
	m.killConcurrently(stop, "Failed to stop background process before a self-update")
	return func() {
		for _, process := range handed {
			process.handoff.undo()
			process.fileOut.startTailing()
		}
	}
}

// handOff hands one file-backed process off. ended reports one that had
// already exited and is being reaped here: there is nothing to hand off or
// stop. A handoff that cannot be journaled is taken back (false): the
// process is stopped instead, since the next image could not adopt it.
func (m *BackgroundManager) handOff(process *BackgroundProcess, cb *PersistenceCallback) (handedOff, ended bool) {
	if !process.handoff.begin() {
		select {
		case <-process.done:
		case <-time.After(KillGraceTimeout):
		}
		return false, true
	}
	offsets := process.fileOut.pause()
	m.flushProcessOutput(process)
	if err := cb.OnHandOff(process.ID, offsets); err != nil {
		logging.Warn("Background process could not be handed off to the next daemon image; it is stopped", "id", process.ID, "error", err)
		process.handoff.undo()
		process.fileOut.startTailing()
		return false, false
	}
	return true, false
}

// killConcurrently kills ids at once: each kill waits up to KillGraceTimeout,
// and they are independent (KillAllRunning).
func (m *BackgroundManager) killConcurrently(ids []string, failure string) {
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.KillProcess(id); err != nil {
				logging.Warn(failure, "id", id, "error", err)
			}
		}()
	}
	wg.Wait()
}

// adoption is a handed-off process this image found alive and its own.
type adoption struct {
	proc     *os.Process
	identity osutil.ProcessIdentity
	out      *fileOutput
}

// adoptable checks a handed-off record against the process table: the pid
// must be alive (a zombie counts: its status is waiting to be collected),
// this process's child, and the process journaled for it (its start time),
// not a later holder of a reused pid. It then reopens the output files.
func adoptable(rec journalRecord, dir string) (*adoption, error) {
	if !fileOutputSupported {
		return nil, errors.ErrUnsupported
	}
	id, ok := osutil.IdentifyProcess(rec.PID)
	switch {
	case !ok:
		return nil, fmt.Errorf("pid %d is gone", rec.PID)
	case !id.Same(rec.StartTicks):
		return nil, fmt.Errorf("pid %d is another process now (start %d, journaled %d)", rec.PID, id.Start, rec.StartTicks)
	case id.PPID != os.Getpid():
		return nil, fmt.Errorf("pid %d is not this daemon's child (parent %d)", rec.PID, id.PPID)
	}
	out, err := openFileOutput(dir, rec.ID, [2]int64{rec.StdoutOffset, rec.StderrOffset})
	if err != nil {
		return nil, err
	}
	proc, err := os.FindProcess(rec.PID)
	if err != nil {
		out.discardKeepFiles()
		return nil, err
	}
	return &adoption{proc: proc, identity: id, out: out}, nil
}

// adopt registers a handed-off process as running, under its own id, with
// the output journaled so far; resumeAdopted starts tailing and waiting for
// it once the journal is the manager's persistence. False if m already has
// the id.
func (m *BackgroundManager) adopt(rec journalRecord, a *adoption, lines []OutputLineWithSeq, seq int64) bool {
	process := restoredProcess(rec, lines, seq)
	process.Status, process.EndTime, process.ExitCode = "running", nil, nil
	process.done = make(chan struct{})
	process.cmd = &exec.Cmd{Process: a.proc}
	proc := a.proc
	process.cancelFunc = func() { _ = proc.Kill() }
	process.tree = proctree.Default().Reattach(rec.PID, rec.Cgroup, proctree.Owner{ChatID: rec.ChatID, Command: rec.Command})
	process.identity = a.identity
	process.activity = NewOutputActivity(m.now)
	process.oomSnap = bgMemReader.SnapshotOOMKills()
	process.flushedSeq = seq
	process.fileOut = a.out
	a.out.bind(process)

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.processes[rec.ID]; exists {
		a.out.discardKeepFiles()
		return false
	}
	m.processes[rec.ID] = process
	m.adopted = append(m.adopted, process)
	return true
}

// resumeAdopted starts tailing, waiting for and journaling the processes
// adopted since the last call.
func (m *BackgroundManager) resumeAdopted() {
	m.mu.Lock()
	adopted := m.adopted
	m.adopted = nil
	m.mu.Unlock()
	m.persistenceMu.RLock()
	flush := m.persistenceCallback != nil && m.persistenceCallback.OnOutput != nil
	m.persistenceMu.RUnlock()
	for _, process := range adopted {
		process.fileOut.startTailing()
		go m.waitForFileProcess(process, reapAdopted(process.cmd.Process))
		if flush {
			go m.flushOutput(process)
		}
		logging.Info("Adopted a background process the previous daemon image handed off",
			"id", process.ID, "pid", process.GetPID(), "command", process.Command)
	}
}

// stopOrphan stops a file-backed process a crashed (or force-stopped)
// daemon left running: nothing else would, and no daemon tracks it any
// more. Only while it is provably the journaled process — the same start
// time — and still the leader of its own group.
func stopOrphan(rec journalRecord) {
	if !rec.FileOutput || rec.PID <= 0 {
		return
	}
	id, ok := osutil.IdentifyProcess(rec.PID)
	if !ok || id.Exited || !id.Same(rec.StartTicks) || id.PGID != rec.PID {
		return
	}
	logging.Info("Stopping a background process a previous daemon left running untracked", "id", rec.ID, "pid", rec.PID)
	// Recorded now, while its leader is alive and proven: the group is its.
	w := watchGroup(rec.PID)
	// A handed-off process this image could not adopt (its output files
	// gone) is still this process's child: reap it once it is stopped.
	var reap *os.Process
	if id.PPID == os.Getpid() {
		reap, _ = os.FindProcess(rec.PID)
	}
	go func() {
		stopProcessGroup(w, nil, nil)
		if reap != nil {
			_, _ = reap.Wait()
		}
	}()
}
