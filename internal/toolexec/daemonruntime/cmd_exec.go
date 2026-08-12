// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/osutil"
)

func init() {
	RegisterCommand("exec.run", handleExecRun)
	RegisterCommand("exec.bg_start", handleExecBGStart)
	RegisterCommand("exec.bg_output", handleExecBGOutput)
	RegisterCommand("exec.bg_kill", handleExecBGKill)
	RegisterCommand("exec.bg_list", handleExecBGList)
}

// =============================================================================
// exec.run — synchronous command execution
// =============================================================================

const defaultExecTimeout = 60_000 // 60 seconds

// buildExecCommand constructs the child process for a run request.
//
// Two shapes, and the difference is a security boundary rather than a
// convenience: Argv execs the binary directly with no interpreter, so a
// caller's command allowlist actually binds. Command goes through the
// platform shell, which is what first-party callers want (pipelines,
// redirection, expansion) and what a confined caller must not get.
func buildExecCommand(ctx context.Context, req daemon.RunCommandRequest) *exec.Cmd {
	if len(req.Argv) > 0 {
		return exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	}
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", req.Command)
	}
	return exec.CommandContext(ctx, "bash", "-c", req.Command)
}

func handleExecRun(ctx context.Context, payload []byte) ([]byte, error) {
	var req daemon.RunCommandRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// An absent working directory means the process inherits the DAEMON's cwd,
	// which for a confined caller is outside the grant. Resolving maps empty
	// to the allowed root; a no-op for unconfined callers, which keep
	// inheriting the daemon's cwd as before.
	resolvedDir, err := daemonpolicy.ResolveDir(ctx, req.WorkingDir)
	if err != nil {
		return nil, err
	}
	req.WorkingDir = resolvedDir

	// Name a bad working directory before spawning, or the kernel's ENOENT
	// comes back attributed to the shell binary instead of the directory.
	if wdErr := osutil.ValidateWorkingDir(req.WorkingDir); wdErr != nil {
		msg := wdErr.Error()
		return json.Marshal(daemon.CommandResult{ExitCode: 1, Stderr: msg, Combined: msg})
	}

	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = defaultExecTimeout
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := buildExecCommand(execCtx, req)

	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}

	// Put child in its own process group
	setExecProcessGroup(cmd)

	// A confined caller gets a constructed environment rather than the
	// daemon's. The daemon's environment holds the user's git token and the
	// deployment's internal URLs, and any allowlisted program can print its
	// own environment straight back to the model. Unconfined callers inherit
	// everything, as before.
	cmd.Env = daemonpolicy.ChildEnv(ctx, req.Env)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// A bytes.Buffer stdout means os/exec reads through an OS pipe, and
	// cmd.Wait() blocks until the LAST holder of that pipe exits — which is not
	// the shell if the shell spawned something that outlives it. Bound that
	// wait, or a finished command hangs for a grandchild's lifetime and
	// TimeoutMs stops bounding anything. Shared with LocalClient.RunCommand so
	// the two paths cannot bound it differently.
	cmd.WaitDelay = daemon.ExecWaitDelay

	// Snapshot the cgroup's oom_kill counter so a SIGKILL during the
	// command's lifetime can be attributed to the kernel OOM killer.
	// Invalid (and therefore inert) on hosts without cgroup v2 accounting.
	oomSnap := memReader.SnapshotOOMKills()

	start := time.Now()
	err = cmd.Start()
	if err == nil {
		// Steer the OOM killer toward the workload, away from the daemon.
		steerOOMKiller(cmd)

		// Wait in a goroutine so the command can be detached into a background
		// process while it is still running. cmd.Wait() may be called exactly
		// once, so its result travels on this channel and is handed to the
		// background manager on adoption — never waited on twice.
		waitCh := make(chan error, 1)
		go func() { waitCh <- cmd.Wait() }()

		if bgResp, backgrounded := pollForBackgroundDetach(ctx, cmd, req, &stdoutBuf, &stderrBuf, waitCh, start); backgrounded {
			return json.Marshal(bgResp)
		}
		err = <-waitCh
	}
	duration := time.Since(start)

	// Shared with LocalClient.RunCommand so the two exec paths cannot drift on
	// what a failure looks like — see daemon.ClassifyExecOutcome.
	outcome := daemon.ClassifyExecOutcome(err, execCtx.Err(), stderrBuf.String(), memReader, oomSnap)

	resp := daemon.CommandResult{
		Stdout:           stdoutBuf.String(),
		Stderr:           outcome.Stderr,
		DurationMs:       duration.Milliseconds(),
		ExitCode:         outcome.ExitCode,
		TimedOut:         outcome.TimedOut,
		OOMKilled:        outcome.OOMKilled,
		OutputIncomplete: outcome.OutputIncomplete,
	}
	resp.Combined = daemon.CombineOutput(resp.Stdout, resp.Stderr)

	return json.Marshal(resp)
}

// backgroundPollInterval is how often a running command checks whether the user
// asked to detach it. 100ms is imperceptible to a person clicking the button and
// negligible against commands that run long enough to be worth backgrounding.
const backgroundPollInterval = 100 * time.Millisecond

// pollForBackgroundDetach waits for the command to finish OR for the user to ask
// that it be detached into a background process, whichever comes first.
//
// This restores behaviour that existed and was deleted (see
// internal/llm/tools/shell_unix.go at 74e60c49^). Until now the API server set
// an in-memory "backgrounded" flag that NOTHING read: shell.BackgroundSignal
// lives in the API server's memory while the command runs in the DAEMON, a
// separate process that may not even be on the same machine. So clicking
// "background" marked the tool_calls row BACKGROUNDED, told the UI it worked,
// and left the command running in the foreground still blocking the workflow.
// Every backgrounded call in the database has a NULL background_process_id
// because no process was ever created.
//
// Returns (result, true) when the command was adopted into the background
// manager; the caller returns that result immediately and the process keeps
// running under BashOutput / BashKill. Returns (_, false) when the command
// finished on its own, and the caller proceeds with the normal result path.
func pollForBackgroundDetach(
	ctx context.Context,
	cmd *exec.Cmd,
	req daemon.RunCommandRequest,
	stdoutBuf, stderrBuf *bytes.Buffer,
	waitCh chan error,
	start time.Time,
) (daemon.CommandResult, bool) {
	ticker := time.NewTicker(backgroundPollInterval)
	defer ticker.Stop()

	for {
		select {
		case waitErr := <-waitCh:
			// Finished before anyone asked to detach it (or in the same
			// instant). The output is real and complete, so report it normally
			// rather than pretending it was backgrounded.
			//
			// waitCh is buffered with capacity 1 and has exactly one sender, so
			// putting the result back is guaranteed not to block and lets the
			// caller's own receive observe it. cmd.Wait() is still called only
			// once.
			waitCh <- waitErr
			return daemon.CommandResult{}, false

		case <-ctx.Done():
			return daemon.CommandResult{}, false

		case <-ticker.C:
			toolCallID, requested := backgroundRequested(ctx)
			if !requested {
				continue
			}

			process, adoptErr := shell.GetBackgroundManager().AdoptRunningProcess(shell.AdoptRunningProcessOptions{
				Cmd:        cmd,
				Command:    req.Command,
				WorkingDir: req.WorkingDir,
				StartTime:  start,
				StdoutBuf:  stdoutBuf,
				StderrBuf:  stderrBuf,
				WaitErrCh:  waitCh,
			})
			if adoptErr != nil {
				// Adoption failed: the command is still running and still owned
				// by this call, so fall back to waiting for it. Saying so beats
				// reporting a background process that does not exist.
				logging.Warn("[exec.run] Failed to adopt process into background; continuing in foreground",
					"error", adoptErr, "toolCallID", toolCallID)
				return daemon.CommandResult{}, false
			}

			logging.Info("[exec.run] Detached command into background process",
				"processID", process.ID, "toolCallID", toolCallID)

			out := fmt.Sprintf(
				"Command detached into a background process.\nProcess ID: %s\nCommand: %s\nUse BashOutput to read its output and BashKill to stop it.",
				process.ID, req.Command)
			resp := daemon.CommandResult{
				Stdout:       out,
				DurationMs:   time.Since(start).Milliseconds(),
				ExitCode:     0,
				Backgrounded: true,
				ProcessID:    process.ID,
			}
			resp.Combined = daemon.CombineOutput(resp.Stdout, resp.Stderr)
			return resp, true
		}
	}
}

// =============================================================================
// exec.bg_start — start a background process
// =============================================================================

func handleExecBGStart(ctx context.Context, payload []byte) ([]byte, error) {
	var req daemon.RunCommandRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	// Same reasoning as exec.run: an absent working directory would inherit
	// the daemon's cwd, outside a confined caller's grant.
	resolvedDir, err := daemonpolicy.ResolveDir(ctx, req.WorkingDir)
	if err != nil {
		return nil, err
	}
	req.WorkingDir = resolvedDir

	mgr := shell.GetBackgroundManager()
	process, err := mgr.StartProcess(ctx, shell.StartProcessOptions{
		Command:    req.Command,
		Argv:       req.Argv,
		WorkingDir: req.WorkingDir,
		Env:        req.Env,
		// Tag the process with the grant that started it, so a later
		// bg_output/bg_kill from a different connector cannot reach it.
		// Empty for first-party callers.
		GrantID: daemonpolicy.GrantIDFromContext(ctx),
	})
	if err != nil {
		return nil, fmt.Errorf("start background: %w", err)
	}

	resp := struct {
		ProcessID string `json:"process_id"`
	}{
		ProcessID: process.ID,
	}
	return json.Marshal(resp)
}

// =============================================================================
// exec.bg_output — retrieve output from a background process
// =============================================================================

type execBGOutputRequest struct {
	ProcessID string             `json:"process_id"`
	Opts      *daemon.OutputOpts `json:"opts,omitempty"`
}

func handleExecBGOutput(ctx context.Context, payload []byte) ([]byte, error) {
	var req execBGOutputRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	mgr := shell.GetBackgroundManager()

	// Ownership first: process ids are not secrets, and a connector must not
	// be able to read the output of the user's own build or test run.
	if _, err := mgr.GetProcessForGrant(req.ProcessID, daemonpolicy.GrantIDFromContext(ctx)); err != nil {
		return nil, err
	}

	combined, err := mgr.GetCombinedOutput(req.ProcessID)
	if err != nil {
		return nil, fmt.Errorf("get output: %w", err)
	}

	// Build the full combined output string
	var sb strings.Builder
	for _, line := range combined {
		sb.WriteString(line.Text)
		sb.WriteByte('\n')
	}
	fullOutput := sb.String()
	totalBytes := len(fullOutput)

	// Apply opts
	var offset, limit int
	if req.Opts != nil {
		offset = req.Opts.Offset
		limit = req.Opts.Limit

		if req.Opts.TailLines > 0 {
			lines := strings.Split(strings.TrimRight(fullOutput, "\n"), "\n")
			start := len(lines) - req.Opts.TailLines
			if start < 0 {
				start = 0
			}
			fullOutput = strings.Join(lines[start:], "\n") + "\n"
			return json.Marshal(daemon.ProcessOutput{
				Output:     fullOutput,
				HasMore:    false,
				NextOffset: totalBytes,
				TotalBytes: totalBytes,
			})
		}

		if req.Opts.Regex != "" {
			// Filter by regex
			re, err := compileRegexForOutput(req.Opts.Regex)
			if err != nil {
				return nil, fmt.Errorf("invalid regex: %w", err)
			}
			lines := strings.Split(strings.TrimRight(fullOutput, "\n"), "\n")
			var filtered []string
			for _, l := range lines {
				if re.MatchString(l) {
					filtered = append(filtered, l)
				}
			}
			fullOutput = strings.Join(filtered, "\n") + "\n"
			totalBytes = len(fullOutput)
		}
	}

	// Apply offset/limit
	result := fullOutput
	if offset > 0 {
		if offset >= len(fullOutput) {
			result = ""
		} else {
			result = fullOutput[offset:]
		}
	}

	hasMore := false
	if limit > 0 && len(result) > limit {
		result = result[:limit]
		hasMore = true
	}

	nextOffset := offset + len(result)

	resp := daemon.ProcessOutput{
		Output:     result,
		HasMore:    hasMore,
		NextOffset: nextOffset,
		TotalBytes: totalBytes,
	}
	return json.Marshal(resp)
}

func compileRegexForOutput(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(pattern)
}

// =============================================================================
// exec.bg_kill — kill a background process
// =============================================================================

type execBGKillRequest struct {
	ProcessID string `json:"process_id"`
}

func handleExecBGKill(ctx context.Context, payload []byte) ([]byte, error) {
	var req execBGKillRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	mgr := shell.GetBackgroundManager()

	// Killing someone else's process is worse than reading its output, so the
	// same ownership check applies.
	if _, err := mgr.GetProcessForGrant(req.ProcessID, daemonpolicy.GrantIDFromContext(ctx)); err != nil {
		return nil, err
	}

	if err := mgr.KillProcess(req.ProcessID); err != nil {
		return nil, fmt.Errorf("kill process: %w", err)
	}
	return json.Marshal(struct{}{})
}

// =============================================================================
// exec.bg_list — list all background processes
// =============================================================================

func handleExecBGList(_ context.Context, _ []byte) ([]byte, error) {
	mgr := shell.GetBackgroundManager()
	processes := mgr.GetAllProcesses()

	// Stamp the daemon's control-plane identity so the orchestrator can build
	// the env-aware proxied preview URL for a detected dev-server port.
	daemonID := DaemonIdentity()

	result := make([]*daemon.ProcessInfo, 0, len(processes))
	for _, p := range processes {
		info := &daemon.ProcessInfo{
			ID:        p.ID,
			Command:   p.Command,
			Status:    p.Status,
			ExitCode:  p.ExitCode,
			StartTime: p.StartTime,
			EndTime:   p.EndTime,
			Ports:     shellPortsToDaemon(p.Ports),
			DaemonID:  daemonID,
		}
		result = append(result, info)
	}
	return json.Marshal(result)
}

// shellPortsToDaemon converts the shell package's PortInfo to the daemon wire
// type so listening-port info (with bind address) survives the RPC to the
// orchestrator, which decides which are publicly bound and previewable.
func shellPortsToDaemon(ports []shell.PortInfo) []daemon.PortInfo {
	if len(ports) == 0 {
		return nil
	}
	out := make([]daemon.PortInfo, len(ports))
	for i, p := range ports {
		out[i] = daemon.PortInfo{
			Port:     p.Port,
			Protocol: p.Protocol,
			State:    p.State,
			Address:  p.Address,
		}
	}
	return out
}
