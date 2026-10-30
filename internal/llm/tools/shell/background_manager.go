// Copyright (c) 2025 Reliant Labs
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:exclude-contract: background process manager for the shell tool; OS process lifecycle owned by the daemon
package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/cgroupmem"
	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/heavyjob"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/osutil"
	"github.com/reliant-labs/reliant/internal/proctree"
)

// ErrPersistenceSkipped is returned by OnCreate when the process was intentionally
// not persisted (e.g., no chat or worktree association). This is not a real error;
// it signals that output flushing should be skipped.
var ErrPersistenceSkipped = errors.New("persistence skipped")

// bgMemReader reads the workspace cgroup's memory accounting for OOM-kill
// attribution. Inert (all checks answer false) on hosts without cgroup v2
// memory files — macOS and uncontained local daemons.
var bgMemReader = cgroupmem.NewReader(cgroupmem.DefaultRoot)

// BackgroundWaitDelay bounds how long a background process's output keeps
// draining AFTER the process itself has exited — the cmd.WaitDelay the
// background path shares with exec.run (daemon.ExecWaitDelay, whose comment
// has the full reasoning).
//
// Normally the drain ends at once: the last writer of the output pipe is the
// process, and its exit is EOF. A command that leaves a child running with the
// inherited stdout (`server & exit 0`) holds the pipe open past its own exit;
// without a bound the process would read as running for the child's whole
// lifetime. A variable so tests can shrink it.
var BackgroundWaitDelay = 10 * time.Second

// maxOutputLineBytes caps one line of captured output. A longer run without a
// newline is split into lines of this size rather than buffered without limit.
const maxOutputLineBytes = 64 << 10

// OutputLine represents a single line of output with its source
type OutputLine struct {
	Type string `json:"type"` // "stdout" or "stderr"
	Text string `json:"text"`
}

// OutputLineWithSeq extends OutputLine with a sequence number for streaming
type OutputLineWithSeq struct {
	Type     string `json:"type"`     // "stdout" or "stderr"
	Text     string `json:"text"`     // The line content
	Sequence int64  `json:"sequence"` // Monotonically increasing sequence number
}

// OutputSubscription represents a subscription to process output
type OutputSubscription struct {
	ProcessID    string
	SubscriberID string
	Ch           chan OutputLineWithSeq
	Done         chan struct{} // Closed when process completes
}

// BackgroundProcess represents a background process
type BackgroundProcess struct {
	ID         string     `json:"id"`
	Command    string     `json:"command"`
	Status     string     `json:"status"` // running, completed, failed, killed, killed_externally
	StartTime  time.Time  `json:"start_time"`
	EndTime    *time.Time `json:"end_time,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	WorkingDir string     `json:"working_dir"`
	WorktreeID string     `json:"worktree_id,omitempty"` // Worktree this process belongs to
	SessionID  string     `json:"session_id"`
	ChatID     string     `json:"chat_id,omitempty"`
	// GrantID records the connector grant that started this process, empty for
	// first-party ones. The registry is process-global and keyed by id alone,
	// so without this a connector holding a guessed id could read the output
	// of — or kill — the user's own build, test run, or dev server.
	GrantID string     `json:"grant_id,omitempty"`
	Ports   []PortInfo `json:"ports,omitempty"` // Currently used ports

	// Internal fields
	cmd            *exec.Cmd
	stdout         *bytes.Buffer
	stderr         *bytes.Buffer
	combinedOutput []OutputLineWithSeq // Interleaved output with sequence numbers
	outputSeq      int64               // Current sequence number for output lines
	outputMu       sync.RWMutex
	cancelFunc     context.CancelFunc
	// survivors are the live members of the command's process group when its
	// leader was reaped: what it left running. Recorded so a later kill can
	// prove the group id is still the command's before signalling it
	// (osutil.GroupMember). Guarded by outputMu.
	survivors []osutil.GroupMember
	done      chan struct{}
	// oomSnap captures the workspace cgroup's oom_kill counter at spawn so a
	// SIGKILLed process can be attributed to the kernel OOM killer on exit.
	oomSnap cgroupmem.OOMSnapshot
	// tree bounds the command's processes and reaches all of them on a kill
	// (internal/proctree). Nil for a process recovered from the database.
	tree *proctree.Tree

	// Output subscribers (for real-time streaming)
	subscribers   map[string]chan OutputLineWithSeq // subscriber ID -> channel
	subscribersMu sync.RWMutex
}

// GetPID returns the process ID (PID) of the running command, or 0 if not available
func (p *BackgroundProcess) GetPID() int {
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

// ProcessEvent represents a background process event
type ProcessEvent struct {
	Type       string     `json:"type"` // "started", "completed", "failed", "killed", "port_changed"
	ProcessID  string     `json:"process_id"`
	ChatID     string     `json:"chat_id,omitempty"`
	SessionID  string     `json:"session_id"`
	WorktreeID string     `json:"worktree_id,omitempty"`
	Command    string     `json:"command"`
	WorkingDir string     `json:"working_dir"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	StartTime  time.Time  `json:"start_time"`
	EndTime    *time.Time `json:"end_time,omitempty"`
	Ports      []PortInfo `json:"ports,omitempty"` // Currently listening ports
}

// ProcessEventCallback is a callback function for process events
type ProcessEventCallback func(event ProcessEvent)

// PersistenceCallback is a callback for persisting process state to the database
type PersistenceCallback struct {
	// OnCreate is called when a new process is started
	OnCreate func(process *BackgroundProcess) error
	// OnStatusChange is called when a process status changes (completed, failed, killed)
	OnStatusChange func(processID string, status string, exitCode *int, endTime *time.Time) error
	// OnOutput is called periodically to persist batches of output lines
	OnOutput func(processID string, lines []OutputLineWithSeq) error
}

// BackgroundManager manages background processes
type BackgroundManager struct {
	processes           map[string]*BackgroundProcess
	mu                  sync.RWMutex
	eventCallback       ProcessEventCallback
	callbackMu          sync.RWMutex
	persistenceCallback *PersistenceCallback
	persistenceMu       sync.RWMutex

	// Optional repo for DB-backed operations (e.g., killing recovered processes by PID)
	repo ProcessStore
}

var (
	bgManager     *BackgroundManager
	bgManagerOnce sync.Once
)

// GetBackgroundManager returns the singleton background manager instance
func GetBackgroundManager() *BackgroundManager {
	bgManagerOnce.Do(func() {
		bgManager = &BackgroundManager{
			processes: make(map[string]*BackgroundProcess),
		}
		// Note: Process monitor is initialized separately to avoid circular dependencies
	})
	return bgManager
}

// SetEventCallback sets the callback function for process events
// This should be called during app initialization to wire up event publishing
func (m *BackgroundManager) SetEventCallback(callback ProcessEventCallback) {
	m.callbackMu.Lock()
	defer m.callbackMu.Unlock()
	m.eventCallback = callback
}

// SetPersistenceCallback sets the callback for persisting process state
// This should be called during app initialization to wire up database persistence
func (m *BackgroundManager) SetPersistenceCallback(callback *PersistenceCallback) {
	m.persistenceMu.Lock()
	defer m.persistenceMu.Unlock()
	m.persistenceCallback = callback
}

// SetRepository sets the DB repository for operations that need database access.
// This is optional; when unset, DB-backed operations will not be available.
func (m *BackgroundManager) SetRepository(repo ProcessStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repo = repo
}

// persistCreate calls the persistence callback to save a new process.
// Returns true if the row was actually created in the database.
func (m *BackgroundManager) persistCreate(process *BackgroundProcess) bool {
	m.persistenceMu.RLock()
	callback := m.persistenceCallback
	m.persistenceMu.RUnlock()

	if callback != nil && callback.OnCreate != nil {
		if err := callback.OnCreate(process); err != nil {
			if errors.Is(err, ErrPersistenceSkipped) {
				logging.Debug("Process persistence skipped",
					"id", process.ID)
			} else {
				logging.Error("Failed to persist process creation",
					"id", process.ID,
					"error", err)
			}
			return false
		}
		return true
	}
	return false
}

// persistStatusChange calls the persistence callback to update process status
func (m *BackgroundManager) persistStatusChange(processID string, status string, exitCode *int, endTime *time.Time) {
	m.persistenceMu.RLock()
	callback := m.persistenceCallback
	m.persistenceMu.RUnlock()

	if callback != nil && callback.OnStatusChange != nil {
		if err := callback.OnStatusChange(processID, status, exitCode, endTime); err != nil {
			logging.Error("Failed to persist process status change",
				"id", processID,
				"status", status,
				"error", err)
		}
	}
}

// flushOutput periodically persists new output lines to the database.
// It runs in a goroutine for the lifetime of the process.
func (m *BackgroundManager) flushOutput(process *BackgroundProcess) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastFlushedSeq int64

	for {
		select {
		case <-ticker.C:
			lastFlushedSeq = m.flushNewOutputLines(process, lastFlushedSeq)
		case <-process.done:
			// Final flush after process completes
			m.flushNewOutputLines(process, lastFlushedSeq)
			return
		}
	}
}

// flushNewOutputLines persists any output lines with sequence > lastFlushedSeq.
// Returns the new lastFlushedSeq value.
func (m *BackgroundManager) flushNewOutputLines(process *BackgroundProcess, lastFlushedSeq int64) int64 {
	m.persistenceMu.RLock()
	callback := m.persistenceCallback
	m.persistenceMu.RUnlock()

	if callback == nil || callback.OnOutput == nil {
		return lastFlushedSeq
	}

	process.outputMu.RLock()
	var newLines []OutputLineWithSeq
	for _, line := range process.combinedOutput {
		if line.Sequence > lastFlushedSeq {
			newLines = append(newLines, line)
		}
	}
	currentSeq := process.outputSeq
	process.outputMu.RUnlock()

	if len(newLines) > 0 {
		if err := callback.OnOutput(process.ID, newLines); err != nil {
			logging.Warn("[BackgroundManager] Failed to persist output", "processID", process.ID, "error", err)
			return lastFlushedSeq // Don't advance on error — retry next tick
		}
		return currentSeq
	}
	return lastFlushedSeq
}

// emitEvent sends a process event to the registered callback
func (m *BackgroundManager) emitEvent(event ProcessEvent) {
	m.callbackMu.RLock()
	callback := m.eventCallback
	m.callbackMu.RUnlock()

	if callback != nil {
		// Run callback in goroutine to avoid blocking
		go callback(event)
	}
}

// EmitPortChangeEvent emits a port_changed event for a process.
// This is called by ProcessMonitor when it detects port changes.
func (m *BackgroundManager) EmitPortChangeEvent(processID string, ports []PortInfo) {
	m.mu.RLock()
	process, exists := m.processes[processID]
	m.mu.RUnlock()

	if !exists {
		return
	}

	// Update the process's port information
	process.outputMu.Lock()
	process.Ports = ports
	process.outputMu.Unlock()

	logging.Debug("Process port change detected",
		"id", processID,
		"ports", len(ports))

	m.emitEvent(ProcessEvent{
		Type:       "port_changed",
		ProcessID:  processID,
		ChatID:     process.ChatID,
		SessionID:  process.SessionID,
		WorktreeID: process.WorktreeID,
		Command:    process.Command,
		WorkingDir: process.WorkingDir,
		Status:     process.Status,
		StartTime:  process.StartTime,
		Ports:      ports,
	})
}

// StartProcessOptions contains options for starting a background process
type StartProcessOptions struct {
	Command    string
	WorkingDir string
	WorktreeID string
	SessionID  string
	ChatID     string
	// GrantID scopes the process to a connector grant. Empty for first-party
	// callers; see BackgroundProcess.GrantID.
	GrantID string
	Env     map[string]string
	// Credentials is daemonpolicy.CredentialsNone to start the process with no
	// GitHub, git, ssh or cloud credentials.
	Credentials string
	// Argv, when set, runs the command with no shell (see
	// daemon.RunCommandRequest.Argv). Command is used for display only.
	Argv []string
	// HeavyGate queues a heavy command behind the machine's heavy slots:
	// the process starts at once, prints that it is waiting, and runs its
	// command when a slot frees. On a nearly full disk it refuses the
	// command instead, and StartProcess returns the reason. Nil does neither.
	HeavyGate *heavyjob.Gate
	// Heavy declares the command heavy whatever its words are (see
	// daemon.RunCommandRequest.Heavy).
	Heavy bool
}

// StartProcessWithEnv starts a new background process with custom environment variables
// Deprecated: Use StartProcess instead which accepts StartProcessOptions
func (m *BackgroundManager) StartProcessWithEnv(ctx context.Context, command string, workingDir string, sessionID string, chatID string, env map[string]string) (*BackgroundProcess, error) {
	return m.StartProcess(ctx, StartProcessOptions{
		Command:    command,
		WorkingDir: workingDir,
		SessionID:  sessionID,
		ChatID:     chatID,
		Env:        env,
	})
}

// StartProcess starts a new background process with the given options
func (m *BackgroundManager) StartProcess(ctx context.Context, opts StartProcessOptions) (*BackgroundProcess, error) {
	// Name a bad working directory before spawning. Go does the chdir in the
	// forked child, so the kernel's ENOENT comes back as
	// "fork/exec /bin/bash: no such file or directory" — blaming the shell for
	// a directory that is gone.
	if err := osutil.ValidateWorkingDir(opts.WorkingDir); err != nil {
		return nil, err
	}

	processID := uuid.New().String()

	// Create command with context for cancellation
	cmdCtx, cancel := context.WithCancel(context.Background())
	// Argv runs the program directly with no shell, so a caller confined to an
	// allowlist gets the same guarantee here as for a synchronous command.
	// Without it, use the platform shell (bash on Unix, PowerShell on Windows).
	var cmd *exec.Cmd
	if len(opts.Argv) > 0 {
		cmd = exec.CommandContext(cmdCtx, opts.Argv[0], opts.Argv[1:]...)
	} else {
		cmd = createShellCommand(cmdCtx, opts.Command)
	}
	cmd.Dir = opts.WorkingDir

	// Set up environment variables.
	// RELIANT_SPAWNED=1 allows scripts to detect they're running from Reliant.
	//
	// ChildEnv returns the daemon's full environment for a first-party caller
	// and an allowlisted subset for a confined one, so a connector's
	// background job cannot read the user's git token out of its own
	// environment. See internal/daemonpolicy/env.go.
	env := map[string]string{
		"GIT_EDITOR":      "true",
		"RELIANT_SPAWNED": "1",
	}
	for key, value := range opts.Env {
		env[key] = value
	}
	cmd.Env = daemonpolicy.ChildEnv(ctx, env, opts.Credentials)
	if _, err := opts.HeavyGate.Prepare(cmd, heavyjob.Job{Command: opts.Command, Argv: opts.Argv, Declared: opts.Heavy}); err != nil {
		// Refused (a nearly full disk): it never starts, and says why.
		cancel()
		return nil, err
	}
	// credentials: none runs where the daemon's credential files are absent; see
	// credsandbox. Outside the heavy-slot rewrite above, so the slot wrapper
	// runs inside it.
	if refusal := credsandbox.Wrap(cmd, opts.Credentials); refusal != "" {
		cancel()
		return nil, errors.New(refusal)
	}

	// Create buffers for output
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	process := &BackgroundProcess{
		ID:             processID,
		Command:        opts.Command,
		Status:         "running",
		StartTime:      time.Now(),
		WorkingDir:     opts.WorkingDir,
		WorktreeID:     opts.WorktreeID,
		SessionID:      opts.SessionID,
		ChatID:         opts.ChatID,
		GrantID:        opts.GrantID,
		cmd:            cmd,
		stdout:         stdout,
		stderr:         stderr,
		combinedOutput: make([]OutputLineWithSeq, 0),
		outputSeq:      0,
		cancelFunc:     cancel,
		done:           make(chan struct{}),
		subscribers:    make(map[string]chan OutputLineWithSeq),
	}

	process.oomSnap = bgMemReader.SnapshotOOMKills()

	// Output is captured by writers that os/exec feeds from its own copying
	// goroutines, NOT by reading cmd.StdoutPipe(). Wait closes a StdoutPipe as
	// soon as the process exits, so a reader still behind lost the rest of the
	// output — all of it, for a command as quick as `env`. With writers, Wait
	// itself returns only once the output has drained (to EOF, or for
	// BackgroundWaitDelay after the exit), so completion is never reported
	// ahead of the output it completes.
	stdoutLines := &outputLineWriter{process: process, buffer: stdout, outputType: "stdout"}
	stderrLines := &outputLineWriter{process: process, buffer: stderr, outputType: "stderr"}
	cmd.Stdout = stdoutLines
	cmd.Stderr = stderrLines
	cmd.WaitDelay = BackgroundWaitDelay

	// Its own process group, so a kill reaches every child (e.g. npm run dev),
	// and — where the machine allows — its own cgroup with a process limit,
	// so a runaway command fails alone (internal/proctree). Last before
	// Start: everything above may still rewrite what cmd execs.
	process.tree = proctree.Default().Prepare(cmd)

	// Start the command
	if err := cmd.Start(); err != nil {
		process.tree.Started(0)
		cancel()
		return nil, fmt.Errorf("failed to start command: %w", err)
	}
	process.tree.Started(cmd.Process.Pid)

	// Steer the kernel OOM killer toward the workload (and its descendants,
	// which inherit the score) rather than the daemon. No-op outside Linux;
	// best-effort everywhere.
	if cmd.Process != nil {
		if err := osutil.AdjustChildOOMScore(cmd.Process.Pid); err != nil {
			logging.DebugContext(ctx, "Failed to adjust background process oom_score_adj", "pid", cmd.Process.Pid, "error", err)
		}
		// Yield to the daemon, the UI and Docker when the machine is saturated.
		if err := osutil.LowerChildPriority(cmd.Process.Pid); err != nil {
			logging.DebugContext(ctx, "Failed to lower background process priority", "pid", cmd.Process.Pid, "error", err)
		}
		if err := osutil.LowerChildIOPriority(cmd.Process.Pid); err != nil {
			logging.DebugContext(ctx, "Failed to lower background process I/O priority", "pid", cmd.Process.Pid, "error", err)
		}
	}

	// Start goroutine to wait for process completion
	go m.waitForProcess(process, stdoutLines, stderrLines)

	// Store process in map
	m.mu.Lock()
	m.processes[processID] = process
	m.mu.Unlock()

	logging.DebugContext(ctx, "Started background process",
		"id", processID,
		"workingDir", opts.WorkingDir,
		"worktreeID", opts.WorktreeID,
		"sessionID", opts.SessionID)

	// Persist to database — only flush output if the row was actually created,
	// otherwise the FK constraint on background_process_output will fail.
	persisted := m.persistCreate(process)
	if persisted {
		m.persistenceMu.RLock()
		hasOutputCallback := m.persistenceCallback != nil && m.persistenceCallback.OnOutput != nil
		m.persistenceMu.RUnlock()
		if hasOutputCallback {
			go m.flushOutput(process)
		}
	}

	// Emit process started event
	// Note: Ports may not be available immediately after process start
	m.emitEvent(ProcessEvent{
		Type:       "started",
		ProcessID:  processID,
		ChatID:     opts.ChatID,
		SessionID:  opts.SessionID,
		WorktreeID: opts.WorktreeID,
		Command:    opts.Command,
		WorkingDir: opts.WorkingDir,
		Status:     "running",
		StartTime:  process.StartTime,
		Ports:      process.Ports,
	})

	return process, nil
}

// outputLineWriter splits one output stream of a background process into
// lines, appending each to the process's buffer and combined output and
// broadcasting it to subscribers. os/exec calls Write from a single copying
// goroutine per stream, so partial needs no lock of its own.
type outputLineWriter struct {
	process    *BackgroundProcess
	buffer     *bytes.Buffer
	outputType string
	partial    []byte // the current line, not yet terminated by a newline
}

func (w *outputLineWriter) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		newline := bytes.IndexByte(p, '\n')
		if newline < 0 {
			w.partial = append(w.partial, p...)
			for len(w.partial) >= maxOutputLineBytes {
				w.emit(w.partial[:maxOutputLineBytes])
				w.partial = append(w.partial[:0], w.partial[maxOutputLineBytes:]...)
			}
			break
		}
		w.partial = append(w.partial, p[:newline]...)
		w.emit(w.partial)
		w.partial = w.partial[:0]
		p = p[newline+1:]
	}
	return written, nil
}

// flush emits a final line the stream did not terminate with a newline. Call
// it only once os/exec has stopped writing, i.e. after cmd.Wait returns.
func (w *outputLineWriter) flush() {
	if len(w.partial) > 0 {
		w.emit(w.partial)
		w.partial = w.partial[:0]
	}
}

func (w *outputLineWriter) emit(raw []byte) {
	// Same line shape bufio.ScanLines produced: no trailing carriage return.
	line := string(bytes.TrimSuffix(raw, []byte{'\r'}))
	process := w.process

	process.outputMu.Lock()
	w.buffer.WriteString(line)
	w.buffer.WriteByte('\n')
	process.outputSeq++
	outputLine := OutputLineWithSeq{
		Type:     w.outputType,
		Text:     line,
		Sequence: process.outputSeq,
	}
	process.combinedOutput = append(process.combinedOutput, outputLine)
	process.outputMu.Unlock()

	// Broadcast to subscribers (non-blocking)
	process.subscribersMu.RLock()
	for _, ch := range process.subscribers {
		select {
		case ch <- outputLine:
			// Sent successfully
		default:
			// Channel full, subscriber too slow - skip this line for them
			// They can still see it via GetCombinedOutput
		}
	}
	process.subscribersMu.RUnlock()
}

// waitForProcess waits for a process to complete and updates its status.
// cmd.Wait returns only after the output writers have stopped being written,
// so flushing them first puts every line in place before the status changes.
func (m *BackgroundManager) waitForProcess(process *BackgroundProcess, outputs ...*outputLineWriter) {
	err := process.cmd.Wait()
	process.recordSurvivors()
	process.tree.Release()
	for _, output := range outputs {
		output.flush()
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The process exited 0 but something it left running still held its
		// output open when BackgroundWaitDelay ran out. Collection stopped
		// there; the command itself succeeded and is reported as it exited.
		logging.Debug("Background process output drain cut off by a lingering child", "id", process.ID)
		err = nil
	}
	m.handleProcessCompletion(process, err)
}

// waitForProcessWithChannel waits for process completion via an external channel.
// Used by AdoptRunningProcess to avoid calling cmd.Wait() twice.
func (m *BackgroundManager) waitForProcessWithChannel(process *BackgroundProcess, waitErrCh <-chan error) {
	err := <-waitErrCh
	process.recordSurvivors()
	process.tree.Release()
	m.handleProcessCompletion(process, err)
}

// handleProcessCompletion updates process status and emits completion event.
// Shared by waitForProcess and waitForProcessWithChannel.
func (m *BackgroundManager) handleProcessCompletion(process *BackgroundProcess, err error) {
	defer func() {
		if r := recover(); r != nil {
			logging.Warn("Recovered from panic in handleProcessCompletion", "id", process.ID, "panic", r)
		}
	}()

	process.outputMu.Lock()

	endTime := time.Now()
	process.EndTime = &endTime

	var eventType string
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			process.ExitCode = &exitCode
			process.Status = "failed"
			// Attribute SIGKILL-shaped deaths to the kernel OOM killer when
			// the workspace cgroup recorded one during the process lifetime.
			// The message is appended as a synthetic stderr line so anyone
			// polling the process output (LLM via bg_output, user via RPC)
			// sees an actionable explanation instead of a silent kill.
			if oom, msg := bgMemReader.CheckOOMKill(exitCode, process.oomSnap); oom {
				process.outputSeq++
				process.combinedOutput = append(process.combinedOutput, OutputLineWithSeq{
					Type:     "stderr",
					Text:     msg,
					Sequence: process.outputSeq,
				})
				process.stderr.WriteString(msg + "\n")
				logging.Warn("Background process OOM-killed", "id", process.ID, "exit_code", exitCode)
			}
			// A process that failed for want of disk says which volume is full
			// and what is using it, the same way exec.run results do.
			if note := diskwatch.Default().ExplainNoSpace(bufferTail(process.stderr) + "\n" + bufferTail(process.stdout)); note != "" {
				process.outputSeq++
				process.combinedOutput = append(process.combinedOutput, OutputLineWithSeq{
					Type:     "stderr",
					Text:     note,
					Sequence: process.outputSeq,
				})
				process.stderr.WriteString(note + "\n")
			}
		} else {
			// No exit status means the process never ran to completion, so
			// nothing was ever written to its stderr pipe. Anyone polling the
			// output (LLM via bg_output, user via RPC) would see a process that
			// is "failed" with no exit code and no output at all — the daemon
			// log is not a channel they can read. Append the reason as a
			// synthetic stderr line, the same way an OOM kill is reported above.
			process.Status = "failed"
			process.outputSeq++
			process.combinedOutput = append(process.combinedOutput, OutputLineWithSeq{
				Type:     "stderr",
				Text:     err.Error(),
				Sequence: process.outputSeq,
			})
			process.stderr.WriteString(err.Error() + "\n")
		}
		// A non-zero exit is the command's own verdict (a failing test run),
		// not a daemon fault; only a process that never ran is worth a WARN.
		if exitErr, ok := err.(*exec.ExitError); ok {
			logging.Debug("Background process exited non-zero", "id", process.ID, "exit_code", exitErr.ExitCode())
		} else {
			logging.Warn("Background process failed", "id", process.ID, "error", err)
		}
		eventType = "failed"
	} else {
		exitCode := 0
		process.ExitCode = &exitCode
		process.Status = "completed"
		logging.Debug("Background process completed", "id", process.ID)
		eventType = "completed"
	}

	// Capture values before unlocking
	event := ProcessEvent{
		Type:       eventType,
		ProcessID:  process.ID,
		ChatID:     process.ChatID,
		SessionID:  process.SessionID,
		WorktreeID: process.WorktreeID,
		Command:    process.Command,
		WorkingDir: process.WorkingDir,
		Status:     process.Status,
		ExitCode:   process.ExitCode,
		StartTime:  process.StartTime,
		EndTime:    process.EndTime,
		Ports:      process.Ports,
	}

	process.outputMu.Unlock()

	// Persist from the event captured above, not by re-reading the process.
	// Re-reading after the unlock races a concurrent kill, and would persist a
	// status that disagrees with the event emitted a line later.
	m.persistStatusChange(event.ProcessID, event.Status, event.ExitCode, event.EndTime)

	// Emit event after unlocking
	m.emitEvent(event)

	close(process.done)
}

// GetProcess retrieves a process by ID
// ErrProcessNotOwned is returned when a caller asks about a process started
// under a different grant. It is deliberately indistinguishable from
// "not found" to the caller, so a connector cannot probe for the existence of
// the user's own processes.
var ErrProcessNotOwned = errors.New("process not found")

// GetProcessForGrant returns a process only if grantID matches the one that
// started it.
//
// The registry is process-global and keyed by id alone, so ownership has to be
// checked on every read: a connector holding a guessed or enumerated id would
// otherwise reach the output of the user's own build or test run, which
// routinely contains secrets.
//
// An empty grantID is a first-party caller and may see everything, which is
// the behavior that existed before connectors.
func (m *BackgroundManager) GetProcessForGrant(processID, grantID string) (*BackgroundProcess, error) {
	process, err := m.GetProcess(processID)
	if err != nil {
		return nil, err
	}
	if grantID == "" {
		return process, nil
	}
	if process.GrantID != grantID {
		return nil, ErrProcessNotOwned
	}
	return process, nil
}

// StatusSafe returns the process status under the lock that guards it.
//
// Status is written by the completion, kill and external-death paths while
// other goroutines are reading it, so a bare `process.Status` outside outputMu
// is a data race even though a string read looks harmless.
func (p *BackgroundProcess) StatusSafe() string {
	p.outputMu.RLock()
	defer p.outputMu.RUnlock()
	return p.Status
}

// refreshPorts re-reads the OS port list for a running process.
//
// Status, Ports and cmd are guarded by the process's own outputMu — NOT by the
// manager's map lock. m.mu protects the SHAPE of the map (which ids exist), and
// says nothing about the fields of a process already in it. Reading Status
// under m.mu.RLock alone therefore raced with handleProcessCompletion writing
// it under outputMu, and assigning Ports there was a write performed under a
// READ lock, which is unsynchronized against every other reader.
//
// getProcessPorts shells out to the OS, so it runs between the two critical
// sections rather than inside either. Holding a lock across it would serialize
// every caller of every getter behind a syscall per process.
func (p *BackgroundProcess) refreshPorts() {
	p.outputMu.RLock()
	pid := 0
	if p.Status == "running" && p.cmd != nil && p.cmd.Process != nil {
		pid = p.cmd.Process.Pid
	}
	p.outputMu.RUnlock()

	if pid == 0 {
		return
	}

	ports, err := getProcessPorts(pid)
	if err != nil {
		return
	}

	p.outputMu.Lock()
	p.Ports = ports
	p.outputMu.Unlock()
}

// refreshPortsBatch refreshes the port list of many processes using a fixed
// number of subprocesses, rather than one OS scan per process.
//
// Locking follows refreshPorts exactly: each process's pid is read under its own
// outputMu, the OS work happens holding no lock at all, and each result is
// written back under that process's write lock.
func refreshPortsBatch(processes []*BackgroundProcess) {
	byPid := make(map[int][]*BackgroundProcess, len(processes))
	roots := make([]int, 0, len(processes))
	for _, p := range processes {
		p.outputMu.RLock()
		pid := 0
		if p.Status == "running" && p.cmd != nil && p.cmd.Process != nil {
			pid = p.cmd.Process.Pid
		}
		p.outputMu.RUnlock()

		if pid == 0 {
			continue
		}
		if _, seen := byPid[pid]; !seen {
			roots = append(roots, pid)
		}
		byPid[pid] = append(byPid[pid], p)
	}

	if len(roots) == 0 {
		return
	}

	portsByRoot, err := getProcessPortsForRoots(roots)
	if err != nil {
		return
	}

	for pid, procs := range byPid {
		ports, ok := portsByRoot[pid]
		if !ok {
			continue
		}
		for _, p := range procs {
			p.outputMu.Lock()
			p.Ports = ports
			p.outputMu.Unlock()
		}
	}
}

func (m *BackgroundManager) GetProcess(processID string) (*BackgroundProcess, error) {
	m.mu.RLock()
	process, exists := m.processes[processID]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("process %s not found", processID)
	}

	process.refreshPorts()

	return process, nil
}

// GetProcessesBySession retrieves all processes for a session
func (m *BackgroundManager) GetProcessesBySession(sessionID string) []*BackgroundProcess {
	m.mu.RLock()
	var processes []*BackgroundProcess
	for _, process := range m.processes {
		if process.SessionID == sessionID {
			processes = append(processes, process)
		}
	}
	m.mu.RUnlock()

	// Update port information for running processes. Done outside the map
	// lock: refreshPortsBatch takes each process's own outputMu, which is what
	// actually guards Status/Ports/cmd. Batched because the OS scan is the
	// expensive part and it does not need to be repeated per process.
	refreshPortsBatch(processes)

	return processes
}

// GetProcessesByChat retrieves all processes for a chat
func (m *BackgroundManager) GetProcessesByChat(chatID string) []*BackgroundProcess {
	m.mu.RLock()
	var processes []*BackgroundProcess
	for _, process := range m.processes {
		if process.ChatID == chatID {
			processes = append(processes, process)
		}
	}
	m.mu.RUnlock()

	// Update port information for running processes. Done outside the map
	// lock: refreshPortsBatch takes each process's own outputMu, which is what
	// actually guards Status/Ports/cmd. Batched because the OS scan is the
	// expensive part and it does not need to be repeated per process.
	refreshPortsBatch(processes)

	return processes
}

// GetProcessesByWorktree retrieves all processes for a worktree
func (m *BackgroundManager) GetProcessesByWorktree(worktreeID string) []*BackgroundProcess {
	m.mu.RLock()
	var processes []*BackgroundProcess
	for _, process := range m.processes {
		if process.WorktreeID == worktreeID {
			processes = append(processes, process)
		}
	}
	m.mu.RUnlock()

	// Update port information for running processes. Done outside the map
	// lock: refreshPortsBatch takes each process's own outputMu, which is what
	// actually guards Status/Ports/cmd. Batched because the OS scan is the
	// expensive part and it does not need to be repeated per process.
	refreshPortsBatch(processes)

	return processes
}

// GetAllProcesses retrieves all processes
func (m *BackgroundManager) GetAllProcesses() []*BackgroundProcess {
	m.mu.RLock()
	var processes []*BackgroundProcess
	for _, process := range m.processes {
		processes = append(processes, process)
	}
	m.mu.RUnlock()

	// Update port information for running processes. Done outside the map
	// lock: refreshPortsBatch takes each process's own outputMu, which is what
	// actually guards Status/Ports/cmd. Batched because the OS scan is the
	// expensive part and it does not need to be repeated per process.
	refreshPortsBatch(processes)

	return processes
}

// GetOutput retrieves the current output of a process (separate stdout/stderr)
func (m *BackgroundManager) GetOutput(processID string) (string, string, error) {
	process, err := m.GetProcess(processID)
	if err != nil {
		return "", "", err
	}

	process.outputMu.RLock()
	defer process.outputMu.RUnlock()

	return process.stdout.String(), process.stderr.String(), nil
}

// GetCombinedOutput retrieves the interleaved output of a process (without sequence numbers)
func (m *BackgroundManager) GetCombinedOutput(processID string) ([]OutputLine, error) {
	process, err := m.GetProcess(processID)
	if err != nil {
		return nil, err
	}

	process.outputMu.RLock()
	defer process.outputMu.RUnlock()

	// Convert OutputLineWithSeq to OutputLine
	result := make([]OutputLine, len(process.combinedOutput))
	for i, line := range process.combinedOutput {
		result[i] = OutputLine{
			Type: line.Type,
			Text: line.Text,
		}
	}
	return result, nil
}

// GetCombinedOutputWithSeq retrieves the interleaved output with sequence numbers
// If afterSeq > 0, only returns lines with sequence > afterSeq
func (m *BackgroundManager) GetCombinedOutputWithSeq(processID string, afterSeq int64) ([]OutputLineWithSeq, int64, error) {
	process, err := m.GetProcess(processID)
	if err != nil {
		return nil, 0, err
	}

	process.outputMu.RLock()
	defer process.outputMu.RUnlock()

	latestSeq := process.outputSeq

	if afterSeq <= 0 {
		// Return all output
		result := make([]OutputLineWithSeq, len(process.combinedOutput))
		copy(result, process.combinedOutput)
		return result, latestSeq, nil
	}

	// Find lines after the given sequence
	var result []OutputLineWithSeq
	for _, line := range process.combinedOutput {
		if line.Sequence > afterSeq {
			result = append(result, line)
		}
	}
	return result, latestSeq, nil
}

// KillGraceTimeout is how long a kill waits for a process group to go down on
// SIGTERM before escalating to SIGKILL. A var only so tests can shorten it.
var KillGraceTimeout = 3 * time.Second

// killSettleTimeout is how long a SIGKILLed group gets to disappear: the
// kernel delivers SIGKILL at once, but each member must still be scheduled to
// exit and be reaped.
const killSettleTimeout = 2 * time.Second

// groupPollInterval is how often a kill looks whether the group is empty.
const groupPollInterval = 100 * time.Millisecond

// stopProcessGroup stops a command's whole process group: SIGTERM to every
// member, then onTerm (the context cancel, when there is one), then — if any
// member is still alive after KillGraceTimeout — SIGKILL to the group. It
// returns whether the group is gone. leaderDone closes once the leader has
// been reaped (until then it is a zombie member of its own group).
func stopProcessGroup(w *groupWatch, leaderDone <-chan struct{}, onTerm func()) bool {
	pgid := w.pgid
	if err := terminateProcessGroup(pgid); err != nil {
		logging.Warn("Failed to terminate process group gracefully", "pgid", pgid, "error", err)
	}
	if onTerm != nil {
		onTerm()
	}
	if waitProcessGroupGone(w, leaderDone, KillGraceTimeout) {
		logging.Debug("Process group terminated gracefully", "pgid", pgid)
		return true
	}
	// alive() just proved the id is still the command's.
	logging.Warn("Process group did not terminate gracefully, force killing", "pgid", pgid)
	if err := forceKillProcessGroup(pgid); err != nil {
		logging.Error("Failed to force kill process group", "pgid", pgid, "error", err)
	}
	return waitProcessGroupGone(w, leaderDone, killSettleTimeout)
}

// groupWatch follows a command's process group while it is being stopped.
//
// Liveness is the group's live members — zombies excluded, since a zombie
// runs nothing and one whose reaper never comes would otherwise read as a
// survivor of SIGKILL forever — and only members that are provably the
// command's: some member recorded while the id was known to be the command's
// must still be in the group (osutil.GroupOwned), and then everyone now in it
// is recorded in turn, so ownership carries forward as members come and go.
// Where the process table cannot be read (macOS) it falls back to "does the
// group id answer a signal".
type groupWatch struct {
	pgid     int
	members  []osutil.GroupMember
	listable bool
}

// watchGroup records the group's members now. Call it only while the group
// id is known to be the command's: its leader not yet reaped.
func watchGroup(pgid int) *groupWatch {
	members, ok := osutil.GroupMembers(pgid)
	return &groupWatch{pgid: pgid, members: members, listable: ok}
}

// alive reports whether the group still has live members that are the
// command's.
func (w *groupWatch) alive() bool {
	if !w.listable {
		return processGroupAlive(w.pgid)
	}
	if !osutil.GroupOwned(w.pgid, w.members) {
		w.members = nil
		return false
	}
	w.members, _ = osutil.GroupMembers(w.pgid)
	return len(w.members) > 0
}

// recordSurvivors records what the command left running in its group, right
// after its leader was reaped.
func (p *BackgroundProcess) recordSurvivors() {
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	members, _ := osutil.GroupMembers(p.cmd.Process.Pid)
	p.outputMu.Lock()
	p.survivors = members
	p.outputMu.Unlock()
}

// stopTree is stopProcessGroup, then — when the command has a cgroup of its
// own — a cgroup.kill for anything left in it: a member that left the process
// group with setsid is out of the group's reach but not the cgroup's.
func stopTree(w *groupWatch, tree *proctree.Tree, leaderDone <-chan struct{}, onTerm func()) bool {
	// The group is signalled only while it is provably the command's (a
	// running command's leader, or a member it left); an exited command whose
	// group is gone but whose cgroup is not is reached through the cgroup
	// alone.
	gone := true
	if onTerm != nil || w.alive() {
		gone = stopProcessGroup(w, leaderDone, onTerm)
	}
	if tree.Populated() && tree.Kill() {
		deadline := time.Now().Add(killSettleTimeout)
		for tree.Populated() && time.Now().Before(deadline) {
			time.Sleep(groupPollInterval)
		}
		return gone && !tree.Populated()
	}
	return gone
}

// waitProcessGroupGone waits up to d for the leader to be reaped and every
// other member of the group to exit.
func waitProcessGroupGone(w *groupWatch, leaderDone <-chan struct{}, d time.Duration) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	if leaderDone != nil {
		select {
		case <-leaderDone:
		case <-deadline.C:
			return false
		}
	}
	tick := time.NewTicker(groupPollInterval)
	defer tick.Stop()
	for w.alive() {
		select {
		case <-tick.C:
		case <-deadline.C:
			return !w.alive()
		}
	}
	return true
}

// KillProcess terminates a background process and all its child processes
func (m *BackgroundManager) KillProcess(processID string) error {
	process, err := m.GetProcess(processID)
	if err != nil {
		return err
	}

	// The leader's pid is the process group's id (proctree.Prepare).
	var pid int
	if process.cmd != nil && process.cmd.Process != nil {
		pid = process.cmd.Process.Pid
	}

	if status := process.StatusSafe(); status != "running" {
		// The command's shell exited, but what it started may not have: a
		// `task ci &`, a `nohup server &`, a child that trapped SIGTERM when
		// the shell was killed. They are still in its process group, still
		// this command's, and a kill reaches them — otherwise they run on,
		// orphaned, where nothing can see or stop them.
		// Only while a process it left is provably still in the group: once
		// they have all exited the group id may belong to someone else.
		process.outputMu.RLock()
		w := &groupWatch{pgid: pid, members: process.survivors, listable: true}
		process.outputMu.RUnlock()
		// Its own cgroup is unambiguous (named per daemon and command, never
		// reused), so what is still in it is the command's.
		if process.cmd != nil && (w.alive() || process.tree.Populated()) {
			logging.Info("Killing what an exited background command left running in its process group",
				"id", processID, "pgid", pid, "status", status)
			if !stopTree(w, process.tree, process.done, nil) {
				return fmt.Errorf("process %s (%s) left processes in its group %d that did not die on SIGKILL", processID, status, pid)
			}
			return nil
		}
		return fmt.Errorf("process %s is not running (status: %s)", processID, status)
	}

	// If this is a recovered/ghost process (no exec.Cmd / cancelFunc), fall back to
	// DB+PID based killing. This allows stopping long-running servers (e.g., npm dev)
	// after Reliant restarts.
	if process.cmd == nil || process.cancelFunc == nil {
		return m.killRecoveredProcessByPID(processID)
	}

	logging.Info("Killing background process and process group",
		"id", processID,
		"pid", pid)

	// SIGTERM to the whole group, cancel the context (CommandContext's own
	// kill of the leader), then wait until the GROUP is empty — not just the
	// leader. A leader that dies at once used to end the wait while children
	// that trapped or ignored SIGTERM ran on with nothing left to kill them.
	if pid > 0 {
		// Recorded now, while the leader is unreaped and the group id is
		// therefore certainly this command's.
		if !stopTree(watchGroup(pid), process.tree, process.done, process.cancelFunc) {
			logging.Error("Process group survived SIGKILL", "id", processID, "pgid", pid)
		}
	} else {
		process.cancelFunc()
		select {
		case <-process.done:
		case <-time.After(KillGraceTimeout):
		}
	}
	// The leader itself, if the group could not be signalled at all.
	select {
	case <-process.done:
	default:
		if process.cmd.Process != nil {
			_ = process.cmd.Process.Kill()
		}
	}

	// Capture the event under the lock that guards these fields, the same way
	// handleProcessCompletion and handleExternalKill do. Building it afterwards
	// read Status, ExitCode, EndTime and Ports unsynchronized.
	process.outputMu.Lock()
	process.Status = "killed"
	endTime := time.Now()
	process.EndTime = &endTime
	event := ProcessEvent{
		Type:       "killed",
		ProcessID:  process.ID,
		ChatID:     process.ChatID,
		SessionID:  process.SessionID,
		WorktreeID: process.WorktreeID,
		Command:    process.Command,
		WorkingDir: process.WorkingDir,
		Status:     process.Status,
		ExitCode:   process.ExitCode,
		StartTime:  process.StartTime,
		EndTime:    process.EndTime,
		Ports:      process.Ports,
	}
	process.outputMu.Unlock()

	logging.Info("Killed background process", "id", processID, "pid", pid)

	// Persist status change to database
	m.persistStatusChange(event.ProcessID, event.Status, event.ExitCode, event.EndTime)

	// Emit killed event
	m.emitEvent(event)

	return nil
}

func (m *BackgroundManager) killRecoveredProcessByPID(processID string) error {
	m.mu.RLock()
	repo := m.repo
	m.mu.RUnlock()
	if repo == nil {
		return fmt.Errorf("process %s is running but cannot be controlled (recovered) and no repository is configured", processID)
	}

	ctx := context.Background()
	dbProc, err := repo.GetBackgroundProcess(ctx, processID)
	if err != nil {
		return err
	}

	// If DB says it's not running, align with idempotent semantics.
	if dbProc.Status != db.BgProcessStatusRunning {
		return fmt.Errorf("process %s is not running (status: %s)", processID, dbProc.Status)
	}

	// If we don't have a PID, we cannot kill. Mark stale so it no longer appears running.
	if dbProc.PID == nil || *dbProc.PID <= 0 {
		now := time.Now()
		_ = repo.UpdateBackgroundProcessStatus(ctx, processID, db.BgProcessStatusStale, nil, &now)
		return nil
	}

	pid := *dbProc.PID
	if !osutil.IsProcessRunning(pid) {
		now := time.Now()
		_ = repo.UpdateBackgroundProcessStatus(ctx, processID, db.BgProcessStatusStale, nil, &now)
		return nil
	}

	// Validate signature if available to avoid killing a reused PID.
	if dbProc.Signature != nil && *dbProc.Signature != "" {
		if !osutil.ValidateProcessSignature(pid, *dbProc.Signature, dbProc.Command, dbProc.StartedAt) {
			now := time.Now()
			_ = repo.UpdateBackgroundProcessStatus(ctx, processID, db.BgProcessStatusStale, nil, &now)
			return nil
		}
	}

	logging.Info("Killing recovered background process by PID/process group",
		"id", processID,
		"pid", pid)

	// The whole group, SIGTERM then SIGKILL, until it is empty. The leader
	// is alive and its signature matched, so the group id is its: recorded
	// now. This daemon did not start it, so no reap is waited for.
	if !stopProcessGroup(watchGroup(pid), nil, nil) {
		return fmt.Errorf("recovered process %s: process group %d survived SIGKILL", processID, pid)
	}

	now := time.Now()
	if err := repo.UpdateBackgroundProcessStatus(ctx, processID, db.BgProcessStatusKilled, nil, &now); err != nil {
		return err
	}

	// Best-effort update in-memory representation.
	m.mu.Lock()
	if p, ok := m.processes[processID]; ok {
		p.outputMu.Lock()
		p.Status = "killed"
		p.EndTime = &now
		p.outputMu.Unlock()
	}
	m.mu.Unlock()

	// Emit killed event so UI updates immediately.
	m.emitEvent(ProcessEvent{
		Type:       "killed",
		ProcessID:  processID,
		ChatID:     dbProcChatID(dbProc),
		SessionID:  "",
		WorktreeID: dbProcWorktreeID(dbProc),
		Command:    dbProc.Command,
		WorkingDir: dbProc.WorkingDir,
		Status:     "killed",
		StartTime:  dbProc.StartedAt,
		EndTime:    &now,
	})

	return nil
}

func dbProcWorktreeID(p *db.BackgroundProcess) string {
	if p.WorktreeID == nil {
		return ""
	}
	return *p.WorktreeID
}

func dbProcChatID(p *db.BackgroundProcess) string {
	if p.ChatID == nil {
		return ""
	}
	return *p.ChatID
}

// AdoptRunningProcessOptions contains options for adopting a running process
type AdoptRunningProcessOptions struct {
	Cmd        *exec.Cmd
	Command    string
	WorkingDir string
	WorktreeID string
	SessionID  string
	ChatID     string
	StartTime  time.Time
	StdoutBuf  *bytes.Buffer // Existing stdout buffer
	StderrBuf  *bytes.Buffer // Existing stderr buffer
	WaitErrCh  <-chan error  // Channel that receives cmd.Wait() result from caller (REQUIRED to avoid double-wait)
	// Tree is the command's process tree (proctree), which the background
	// manager takes over: it releases it, and a kill reaches all of it.
	Tree *proctree.Tree
}

// AdoptRunningProcess adopts an already-running command into the background manager.
// This is used when converting a foreground command to background mode.
// Note: The caller must set up proper cancellation handling for the adopted process.
// Note: Adopted processes may or may not have been started with process groups.
// IMPORTANT: The caller MUST pass WaitErrCh - a channel that receives the result of cmd.Wait().
// This prevents calling cmd.Wait() twice which causes undefined behavior.
//
//	The cancelFunc will attempt to kill the process group, falling back to single process kill.
func (m *BackgroundManager) AdoptRunningProcess(opts AdoptRunningProcessOptions) (*BackgroundProcess, error) {
	if opts.Cmd == nil || opts.Cmd.Process == nil {
		return nil, fmt.Errorf("cannot adopt process: command not running")
	}
	if opts.WaitErrCh == nil {
		return nil, fmt.Errorf("cannot adopt process: WaitErrCh is required to avoid calling cmd.Wait() twice")
	}

	processID := uuid.New().String()
	pid := opts.Cmd.Process.Pid

	// Create a cancel function that will kill the process group if possible,
	// falling back to killing just the process
	cancelFunc := func() {
		if opts.Cmd.Process != nil {
			// Try to kill the process group first (works if process was started with Setpgid)
			if err := terminateProcessGroup(pid); err != nil {
				logging.Warn("Failed to terminate process group for adopted process, falling back to direct kill",
					"pid", pid,
					"error", err)
				opts.Cmd.Process.Kill()
			}
		}
	}

	// Use existing buffers or create new ones
	stdout := opts.StdoutBuf
	if stdout == nil {
		stdout = &bytes.Buffer{}
	}
	stderr := opts.StderrBuf
	if stderr == nil {
		stderr = &bytes.Buffer{}
	}

	process := &BackgroundProcess{
		ID:             processID,
		Command:        opts.Command,
		Status:         "running",
		StartTime:      opts.StartTime,
		WorkingDir:     opts.WorkingDir,
		WorktreeID:     opts.WorktreeID,
		SessionID:      opts.SessionID,
		ChatID:         opts.ChatID,
		cmd:            opts.Cmd,
		tree:           opts.Tree,
		stdout:         stdout,
		stderr:         stderr,
		combinedOutput: make([]OutputLineWithSeq, 0),
		outputSeq:      0,
		cancelFunc:     cancelFunc,
		done:           make(chan struct{}),
		subscribers:    make(map[string]chan OutputLineWithSeq),
	}

	// Start goroutine to wait for process completion using the caller's wait channel.
	// CRITICAL: We do NOT call cmd.Wait() here - the caller already has a goroutine waiting.
	// Using the passed WaitErrCh avoids calling cmd.Wait() twice which causes panics.
	go m.waitForProcessWithChannel(process, opts.WaitErrCh)

	// Store process in map
	m.mu.Lock()
	m.processes[processID] = process
	m.mu.Unlock()

	logging.Debug("Adopted running process to background",
		"id", processID,
		"workingDir", opts.WorkingDir,
		"pid", opts.Cmd.Process.Pid,
		"chatID", opts.ChatID)

	// Persist to database
	m.persistCreate(process)

	// Start output flusher goroutine for DB persistence
	m.persistenceMu.RLock()
	hasOutputCb := m.persistenceCallback != nil && m.persistenceCallback.OnOutput != nil
	m.persistenceMu.RUnlock()
	if hasOutputCb {
		go m.flushOutput(process)
	}

	// Emit process started event (as "adopted")
	m.emitEvent(ProcessEvent{
		Type:       "started",
		ProcessID:  processID,
		ChatID:     opts.ChatID,
		SessionID:  opts.SessionID,
		WorktreeID: opts.WorktreeID,
		Command:    opts.Command,
		WorkingDir: opts.WorkingDir,
		Status:     "running",
		StartTime:  process.StartTime,
		Ports:      process.Ports,
	})

	return process, nil
}

// LoadProcess loads a process record from the database into memory.
// This is used during startup recovery to populate the in-memory map with
// processes that are still running (validated by RecoveryService).
// Note: This creates a "ghost" process - we don't have the actual exec.Cmd,
// so we can only track its status and report it in lists. The process cannot
// be killed through us - it must be killed externally or terminate on its own.
func (m *BackgroundManager) LoadProcess(id, command, workingDir, worktreeID, sessionID, chatID, status string, startTime time.Time, pid int) {
	process := &BackgroundProcess{
		ID:             id,
		Command:        command,
		Status:         status,
		StartTime:      startTime,
		WorkingDir:     workingDir,
		WorktreeID:     worktreeID,
		SessionID:      sessionID,
		ChatID:         chatID,
		stdout:         &bytes.Buffer{},
		stderr:         &bytes.Buffer{},
		combinedOutput: make([]OutputLineWithSeq, 0),
		outputSeq:      0,
		done:           make(chan struct{}),
		subscribers:    make(map[string]chan OutputLineWithSeq),
		// Note: cmd is nil - this is a recovered process
		// cancelFunc is nil - we can't cancel a recovered process
	}

	// Try to get current port info for the running process
	if pid > 0 && status == "running" {
		ports, err := getProcessPorts(pid)
		if err == nil {
			process.Ports = ports
		}
	}

	m.mu.Lock()
	m.processes[id] = process
	m.mu.Unlock()

	logging.Debug("Loaded process from database",
		"id", id,
		"status", status,
		"pid", pid)
}

// CleanupOldProcesses removes completed/failed processes older than the specified duration
func (m *BackgroundManager) CleanupOldProcesses(maxAge time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Status and EndTime are read under the process's own outputMu. Taking it
	// while already holding m.mu follows the established order (map lock, then
	// process lock) that every write path here uses, so it cannot deadlock.
	now := time.Now()
	for id, process := range m.processes {
		process.outputMu.RLock()
		expired := process.Status != "running" &&
			process.EndTime != nil &&
			now.Sub(*process.EndTime) > maxAge
		process.outputMu.RUnlock()

		if expired {
			delete(m.processes, id)
			logging.Debug("Cleaned up old process", "id", id)
		}
	}
}

// KillAllRunning kills all running background processes.
// This should be called during server shutdown to prevent orphaned processes.
//
// The processes are killed concurrently. KillProcess spends up to three seconds
// waiting for each one to die, and this runs on the shutdown path between
// SIGTERM and process exit — serially that is three seconds PER background
// process, so a daemon holding a handful of dev servers stayed alive for tens of
// seconds after it was told to stop, looking indistinguishable from wedged. The
// processes are independent, so nothing is ordered here.
func (m *BackgroundManager) KillAllRunning() {
	m.mu.RLock()
	candidates := make(map[string]*BackgroundProcess, len(m.processes))
	for id, process := range m.processes {
		candidates[id] = process
	}
	m.mu.RUnlock()

	// Status is read under each process's own outputMu, not the map lock.
	var runningIDs []string
	for id, process := range candidates {
		if process.StatusSafe() == "running" {
			runningIDs = append(runningIDs, id)
		}
	}

	if len(runningIDs) == 0 {
		logging.Debug("No running background processes to kill during shutdown")
		return
	}

	logging.Info("Killing all running background processes during shutdown", "count", len(runningIDs))

	var wg sync.WaitGroup
	for _, id := range runningIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := m.KillProcess(id); err != nil {
				logging.Warn("Failed to kill background process during shutdown",
					"id", id,
					"error", err)
			}
		}(id)
	}
	wg.Wait()
}

// processTreeFromSnapshot walks a parent->children map to collect root and all
// its descendants. The snapshot is taken once by the caller, so this costs no
// subprocesses no matter how deep the tree is.
func processTreeFromSnapshot(root int, children map[int][]int) []int {
	pids := []int{root}
	// Guard against a cycle in a snapshot that was taken while the process
	// table was changing under us.
	seen := map[int]bool{root: true}
	for queue := []int{root}; len(queue) > 0; {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if seen[child] {
				continue
			}
			seen[child] = true
			pids = append(pids, child)
			queue = append(queue, child)
		}
	}
	return pids
}

// dedupePortsByNumber keeps the first PortInfo seen for each port number,
// matching the previous per-PID scan's behaviour where a port bound by both a
// parent and a child was reported once.
func dedupePortsByNumber(ports []PortInfo) []PortInfo {
	if len(ports) == 0 {
		return nil
	}
	var out []PortInfo
	seen := make(map[int]bool, len(ports))
	for _, portInfo := range ports {
		if seen[portInfo.Port] {
			continue
		}
		seen[portInfo.Port] = true
		out = append(out, portInfo)
	}
	return out
}

// getProcessPorts gets the ports used by a process and its children.
func getProcessPorts(pid int) ([]PortInfo, error) {
	byRoot, err := getProcessPortsForRoots([]int{pid})
	if err != nil {
		return nil, err
	}
	return byRoot[pid], nil
}

// getProcessPortsForRoots resolves the listening ports of several process trees
// in a fixed number of subprocesses: ONE process-table snapshot to expand every
// tree, and ONE port query covering every PID in all of them.
//
// This is the shape the cost lives in, not the parsing. The previous code forked
// `pgrep -P` once per node while descending each tree and then `lsof -p` once per
// resulting PID, so listing N background processes cost O(sum of tree sizes)
// subprocesses — a handful of dev servers with deep node trees is easily a
// hundred forks, which is what made exec.bg_list take seconds and, once, 15.7s.
func getProcessPortsForRoots(roots []int) (map[int][]PortInfo, error) {
	result := make(map[int][]PortInfo, len(roots))
	if len(roots) == 0 {
		return result, nil
	}

	children := childPIDSnapshot()

	treePids := make(map[int][]int, len(roots))
	allPids := make([]int, 0, len(roots))
	seenPid := make(map[int]bool)
	for _, root := range roots {
		pids := processTreeFromSnapshot(root, children)
		treePids[root] = pids
		for _, pid := range pids {
			if !seenPid[pid] {
				seenPid[pid] = true
				allPids = append(allPids, pid)
			}
		}
	}

	portsByPid, err := getPortsForPids(allPids)
	if err != nil {
		return nil, err
	}

	for root, pids := range treePids {
		var ports []PortInfo
		for _, pid := range pids {
			ports = append(ports, portsByPid[pid]...)
		}
		result[root] = dedupePortsByNumber(ports)
	}
	return result, nil
}

// SubscribeToOutput subscribes to real-time output from a process.
// Returns a subscription with:
// - Ch: channel receiving new output lines
// - Done: channel closed when process completes
// The caller must call UnsubscribeFromOutput when done to clean up resources.
func (m *BackgroundManager) SubscribeToOutput(processID string) (*OutputSubscription, error) {
	process, err := m.GetProcess(processID)
	if err != nil {
		return nil, err
	}

	subID := uuid.New().String()
	// Buffer allows some slack before blocking/dropping
	ch := make(chan OutputLineWithSeq, 100)

	process.subscribersMu.Lock()
	process.subscribers[subID] = ch
	process.subscribersMu.Unlock()

	logging.Debug("Subscribed to process output",
		"processID", processID,
		"subscriberID", subID)

	return &OutputSubscription{
		ProcessID:    processID,
		SubscriberID: subID,
		Ch:           ch,
		Done:         process.done,
	}, nil
}

// UnsubscribeFromOutput removes an output subscription.
// Safe to call multiple times or with invalid subscription.
func (m *BackgroundManager) UnsubscribeFromOutput(sub *OutputSubscription) {
	if sub == nil {
		return
	}

	m.mu.RLock()
	process, exists := m.processes[sub.ProcessID]
	m.mu.RUnlock()

	if !exists {
		return
	}

	process.subscribersMu.Lock()
	if ch, ok := process.subscribers[sub.SubscriberID]; ok {
		delete(process.subscribers, sub.SubscriberID)
		close(ch)
	}
	process.subscribersMu.Unlock()

	logging.Debug("Unsubscribed from process output",
		"processID", sub.ProcessID,
		"subscriberID", sub.SubscriberID)
}

// ProcessOutcome is a consistent snapshot of one process's lifecycle fields.
type ProcessOutcome struct {
	Status   string
	ExitCode *int
	EndTime  *time.Time
}

// ProcessOutcomes reports the lifecycle state of the requested processes, and
// which of them this manager has no record of.
//
// Unlike GetProcess / GetAllProcesses it never refreshes ports: that is an OS
// scan per process, and a caller asking only "has this ended?" for many ids
// must not pay for it. Each snapshot is read under the process's own outputMu,
// the lock the completion, kill and external-death paths write under.
func (m *BackgroundManager) ProcessOutcomes(processIDs []string) (map[string]ProcessOutcome, []string) {
	m.mu.RLock()
	tracked := make(map[string]*BackgroundProcess, len(processIDs))
	var unknown []string
	for _, id := range processIDs {
		if process, ok := m.processes[id]; ok {
			tracked[id] = process
		} else {
			unknown = append(unknown, id)
		}
	}
	m.mu.RUnlock()

	outcomes := make(map[string]ProcessOutcome, len(tracked))
	for id, process := range tracked {
		process.outputMu.RLock()
		outcomes[id] = ProcessOutcome{
			Status:   process.Status,
			ExitCode: process.ExitCode,
			EndTime:  process.EndTime,
		}
		process.outputMu.RUnlock()
	}
	return outcomes, unknown
}

// GetProcessStatus returns the current status and completion state of a process
func (m *BackgroundManager) GetProcessStatus(processID string) (status string, isComplete bool, exitCode *int, err error) {
	process, err := m.GetProcess(processID)
	if err != nil {
		return "", false, nil, err
	}

	process.outputMu.RLock()
	defer process.outputMu.RUnlock()

	isComplete = process.Status != "running"
	return process.Status, isComplete, process.ExitCode, nil
}

// noSpaceScanBytes bounds how much of a finished background process's output
// is searched for an ENOSPC: the failure is at the end, and a long-running dev
// server can hold megabytes.
const noSpaceScanBytes = 64 << 10

// bufferTail is the last noSpaceScanBytes of buf.
func bufferTail(buf *bytes.Buffer) string {
	if buf == nil {
		return ""
	}
	b := buf.Bytes()
	if len(b) > noSpaceScanBytes {
		b = b[len(b)-noSpaceScanBytes:]
	}
	return string(b)
}
