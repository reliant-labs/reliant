// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
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

func handleExecRun(ctx context.Context, payload []byte) ([]byte, error) {
	var req daemon.RunCommandRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = defaultExecTimeout
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(execCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", req.Command)
	} else {
		cmd = exec.CommandContext(execCtx, "bash", "-c", req.Command)
	}

	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}

	// Put child in its own process group
	setExecProcessGroup(cmd)

	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// Snapshot the cgroup's oom_kill counter so a SIGKILL during the
	// command's lifetime can be attributed to the kernel OOM killer.
	// Invalid (and therefore inert) on hosts without cgroup v2 accounting.
	oomSnap := memReader.SnapshotOOMKills()

	start := time.Now()
	err := cmd.Start()
	if err == nil {
		// Steer the OOM killer toward the workload, away from the daemon.
		steerOOMKiller(cmd)
		err = cmd.Wait()
	}
	duration := time.Since(start)

	resp := daemon.CommandResult{
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		DurationMs: duration.Milliseconds(),
	}

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			resp.TimedOut = true
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			resp.ExitCode = exitErr.ExitCode()
		} else {
			resp.ExitCode = 1
			resp.Stderr = fmt.Sprintf("%s\n%s", resp.Stderr, err.Error())
		}
		// A SIGKILL-shaped failure (exit -1: shell itself killed; exit 137:
		// shell reports a killed child) that coincides with an oom_kill in the
		// container cgroup gets the structured out-of-memory explanation. The
		// message rides Stderr so the LLM tool result and user RPC consumers
		// both see actionable text, plus a flag for programmatic handling.
		// Timeouts also SIGKILL — TimedOut keeps precedence in consumers.
		if !resp.TimedOut {
			if oom, msg := memReader.CheckOOMKill(resp.ExitCode, oomSnap); oom {
				resp.OOMKilled = true
				if resp.Stderr != "" {
					resp.Stderr += "\n"
				}
				resp.Stderr += msg
			}
		}
	}

	// Build combined output
	if resp.Stderr != "" {
		resp.Combined = resp.Stdout + resp.Stderr
	} else {
		resp.Combined = resp.Stdout
	}

	return json.Marshal(resp)
}

// =============================================================================
// exec.bg_start — start a background process
// =============================================================================

func handleExecBGStart(ctx context.Context, payload []byte) ([]byte, error) {
	var req daemon.RunCommandRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	mgr := shell.GetBackgroundManager()
	process, err := mgr.StartProcess(ctx, shell.StartProcessOptions{
		Command:    req.Command,
		WorkingDir: req.WorkingDir,
		Env:        req.Env,
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

func handleExecBGOutput(_ context.Context, payload []byte) ([]byte, error) {
	var req execBGOutputRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	mgr := shell.GetBackgroundManager()
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

func handleExecBGKill(_ context.Context, payload []byte) ([]byte, error) {
	var req execBGKillRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	mgr := shell.GetBackgroundManager()
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
