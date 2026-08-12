// Copyright (c) 2025 Reliant Labs
package shell

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/cgroupmem"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/osutil"
)

// ErrPersistenceSkipped is returned by OnCreate when the process was intentionally
// not persisted (e.g., no chat or worktree association). This is not a real error;
// it signals that output flushing should be skipped.
var ErrPersistenceSkipped = errors.New("persistence skipped")

// bgMemReader reads the workspace cgroup's memory accounting for OOM-kill
// attribution. Inert (all checks answer false) on hosts without cgroup v2
// memory files — macOS and uncontained local daemons.
var bgMemReader = cgroupmem.NewReader(cgroupmem.DefaultRoot)

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
	done           chan struct{}
	// oomSnap captures the workspace cgroup's oom_kill counter at spawn so a
	// SIGKILLed process can be attributed to the kernel OOM killer on exit.
	oomSnap cgroupmem.OOMSnapshot

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
	repo db.Repository
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
func (m *BackgroundManager) SetRepository(repo db.Repository) {
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

	logging.Info("Process port change detected",
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
	// Argv, when set, runs the command with no shell (see
	// daemon.RunCommandRequest.Argv). Command is used for display only.
	Argv []string
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

	// Set up process group so we can kill all child processes
	// This is critical for commands that spawn subprocesses (e.g., npm run dev)
	setProcessGroup(cmd)

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
	cmd.Env = daemonpolicy.ChildEnv(ctx, env)

	// Create buffers for output
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	// Create pipes for real-time output capture
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

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

	// Start the command
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Steer the kernel OOM killer toward the workload (and its descendants,
	// which inherit the score) rather than the daemon. No-op outside Linux;
	// best-effort everywhere.
	if cmd.Process != nil {
		if err := osutil.AdjustChildOOMScore(cmd.Process.Pid); err != nil {
			logging.Debug("Failed to adjust background process oom_score_adj", "pid", cmd.Process.Pid, "error", err)
		}
	}

	// Start goroutines to capture output
	go m.captureOutput(stdoutPipe, stdout, "stdout", process)
	go m.captureOutput(stderrPipe, stderr, "stderr", process)

	// Start goroutine to wait for process completion
	go m.waitForProcess(process)

	// Store process in map
	m.mu.Lock()
	m.processes[processID] = process
	m.mu.Unlock()

	logging.Info("Started background process",
		"id", processID,
		"command", opts.Command,
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

// captureOutput reads from a pipe and writes to a buffer, also appending to combined output
// and broadcasting to any subscribers
func (m *BackgroundManager) captureOutput(pipe io.Reader, buffer *bytes.Buffer, outputType string, process *BackgroundProcess) {
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		line := scanner.Text()

		// Update output buffers
		process.outputMu.Lock()
		buffer.WriteString(line)
		buffer.WriteByte('\n')
		process.outputSeq++
		outputLine := OutputLineWithSeq{
			Type:     outputType,
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
}

// waitForProcess waits for a process to complete and updates its status
func (m *BackgroundManager) waitForProcess(process *BackgroundProcess) {
	err := process.cmd.Wait()
	m.handleProcessCompletion(process, err)
}

// waitForProcessWithChannel waits for process completion via an external channel.
// Used by AdoptRunningProcess to avoid calling cmd.Wait() twice.
func (m *BackgroundManager) waitForProcessWithChannel(process *BackgroundProcess, waitErrCh <-chan error) {
	err := <-waitErrCh
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
				logging.Warn("Background process OOM-killed", "id", process.ID, "command", process.Command)
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
		logging.Error("Background process failed", "id", process.ID, "error", err)
		eventType = "failed"
	} else {
		exitCode := 0
		process.ExitCode = &exitCode
		process.Status = "completed"
		logging.Info("Background process completed", "id", process.ID)
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

	// Persist status change to database
	m.persistStatusChange(process.ID, process.Status, process.ExitCode, process.EndTime)

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

func (m *BackgroundManager) GetProcess(processID string) (*BackgroundProcess, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	process, exists := m.processes[processID]
	if !exists {
		return nil, fmt.Errorf("process %s not found", processID)
	}

	// Update port information if process is running
	if process.Status == "running" && process.cmd != nil && process.cmd.Process != nil {
		pid := process.cmd.Process.Pid
		ports, err := getProcessPorts(pid)
		if err == nil {
			process.Ports = ports
		}
	}

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

	// Update port information for running processes
	for _, process := range processes {
		if process.Status == "running" && process.cmd != nil && process.cmd.Process != nil {
			pid := process.cmd.Process.Pid
			ports, err := getProcessPorts(pid)
			if err == nil {
				process.Ports = ports
			}
		}
	}

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

	// Update port information for running processes
	for _, process := range processes {
		if process.Status == "running" && process.cmd != nil && process.cmd.Process != nil {
			pid := process.cmd.Process.Pid
			ports, err := getProcessPorts(pid)
			if err == nil {
				process.Ports = ports
			}
		}
	}

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

	// Update port information for running processes
	for _, process := range processes {
		if process.Status == "running" && process.cmd != nil && process.cmd.Process != nil {
			pid := process.cmd.Process.Pid
			ports, err := getProcessPorts(pid)
			if err == nil {
				process.Ports = ports
			}
		}
	}

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

	// Update port information for running processes
	for _, process := range processes {
		if process.Status == "running" && process.cmd != nil && process.cmd.Process != nil {
			pid := process.cmd.Process.Pid
			ports, err := getProcessPorts(pid)
			if err == nil {
				process.Ports = ports
			}
		}
	}

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
// SIGTERM before escalating to SIGKILL.
const KillGraceTimeout = 3 * time.Second

// KillProcess terminates a background process and all its child processes
func (m *BackgroundManager) KillProcess(processID string) error {
	process, err := m.GetProcess(processID)
	if err != nil {
		return err
	}

	if process.Status != "running" {
		return fmt.Errorf("process %s is not running (status: %s)", processID, process.Status)
	}

	// If this is a recovered/ghost process (no exec.Cmd / cancelFunc), fall back to
	// DB+PID based killing. This allows stopping long-running servers (e.g., npm dev)
	// after Reliant restarts.
	if process.cmd == nil || process.cancelFunc == nil {
		return m.killRecoveredProcessByPID(processID)
	}

	// Get the PID before we do anything
	var pid int
	if process.cmd != nil && process.cmd.Process != nil {
		pid = process.cmd.Process.Pid
	}

	logging.Info("Killing background process and process group",
		"id", processID,
		"pid", pid)

	// First, try to gracefully terminate the process group
	// This sends SIGTERM to all processes in the group
	if pid > 0 {
		if err := terminateProcessGroup(pid); err != nil {
			logging.Warn("Failed to terminate process group gracefully",
				"pid", pid,
				"error", err)
		}
	}

	// Cancel the context as well (this triggers CommandContext cancellation)
	process.cancelFunc()

	// Wait for process to finish with timeout
	select {
	case <-process.done:
		// Process finished
		logging.Debug("Process group terminated gracefully", "id", processID)
	case <-time.After(KillGraceTimeout):
		// Force kill the entire process group if it doesn't stop gracefully
		logging.Warn("Process group did not terminate gracefully, force killing",
			"id", processID,
			"pid", pid)
		if pid > 0 {
			if err := forceKillProcessGroup(pid); err != nil {
				logging.Error("Failed to force kill process group",
					"pid", pid,
					"error", err)
			}
		}
		// Also try direct process kill as last resort
		if process.cmd != nil && process.cmd.Process != nil {
			process.cmd.Process.Kill()
		}
	}

	process.outputMu.Lock()
	process.Status = "killed"
	endTime := time.Now()
	process.EndTime = &endTime
	process.outputMu.Unlock()

	logging.Info("Killed background process", "id", processID, "pid", pid)

	// Persist status change to database
	m.persistStatusChange(processID, "killed", nil, process.EndTime)

	// Emit killed event
	m.emitEvent(ProcessEvent{
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
	})

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

	// Try graceful termination first.
	if err := terminateProcessGroup(pid); err != nil {
		logging.Warn("Failed to terminate recovered process group gracefully",
			"pid", pid,
			"error", err)
	}

	// Give it a brief moment then force kill.
	time.Sleep(3 * time.Second)
	if osutil.IsProcessRunning(pid) {
		if err := forceKillProcessGroup(pid); err != nil {
			logging.Error("Failed to force kill recovered process group",
				"pid", pid,
				"error", err)
			return err
		}
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

	logging.Info("Adopted running process to background",
		"id", processID,
		"command", opts.Command,
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

	logging.Info("Loaded process from database",
		"id", id,
		"command", command,
		"status", status,
		"pid", pid)
}

// CleanupOldProcesses removes completed/failed processes older than the specified duration
func (m *BackgroundManager) CleanupOldProcesses(maxAge time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, process := range m.processes {
		if process.Status != "running" && process.EndTime != nil {
			if now.Sub(*process.EndTime) > maxAge {
				delete(m.processes, id)
				logging.Debug("Cleaned up old process", "id", id)
			}
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
	var runningIDs []string
	for id, process := range m.processes {
		if process.Status == "running" {
			runningIDs = append(runningIDs, id)
		}
	}
	m.mu.RUnlock()

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

// getProcessPorts gets the ports used by a process and its children
func getProcessPorts(pid int) ([]PortInfo, error) {
	// Get all child PIDs recursively
	allPids := getProcessTree(pid)

	var ports []PortInfo
	seenPorts := make(map[int]bool) // Deduplicate by port number

	for _, p := range allPids {
		pidPorts, err := getPortsForPid(p)
		if err != nil {
			continue // Ignore errors for individual PIDs
		}
		for _, portInfo := range pidPorts {
			if !seenPorts[portInfo.Port] {
				seenPorts[portInfo.Port] = true
				ports = append(ports, portInfo)
			}
		}
	}

	return ports, nil
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
