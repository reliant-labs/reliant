// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"math"
	"time"
)

// ReissueAfterInterruption reports whether a daemon-placed tool call whose
// daemon connection dropped while it was in flight may be sent again once the
// daemon reconnects, and the input to send it with.
//
// Asking again is safe only for a call that observes state, and only state the
// daemon keeps across a reconnect. The daemon is one process that outlives its
// stream: a gateway rollout ends the stream, not the daemon, so a background
// process it started is still running and still buffering output. Reading that
// again changes nothing, whether or not the first request ran.
//
// Every tool that starts, writes, edits, kills or calls out (shell, prove_test,
// metadata_writer, any MCP tool) is excluded: its first request may have run,
// or may still be running, and nothing reports which. The caller keeps
// reporting those as outcome unknown.
//
// elapsed is how long the original call has already been out. shell_wait's
// budget is cut by it: re-sent with its full budget, a wait interrupted minutes
// in would outlast the executor's ceiling (toolexec.DefaultToolTimeout) and
// come back as a timeout error instead of shell_wait's own "still running, call
// again".
func ReissueAfterInterruption(toolName, input string, elapsed time.Duration) (string, bool) {
	switch toolName {
	case ToolShellList, ToolShellOutput, ToolCodeContext:
		return input, true
	case ToolShellWait:
		return shellWaitInputWithin(input, elapsed)
	default:
		return "", false
	}
}

// shellWaitInputWithin rewrites a shell_wait input so the call waits only for
// what remains of the budget it was first given, at least one second: past
// the budget, the re-sent call reports the process's state at once.
func shellWaitInputWithin(input string, elapsed time.Duration) (string, bool) {
	if input == "" {
		input = "{}"
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(input), &fields); err != nil {
		return "", false
	}
	var params ShellWaitParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", false
	}
	budget := shellWaitDefaultTimeout
	if params.TimeoutSeconds > 0 {
		budget = min(time.Duration(params.TimeoutSeconds)*time.Second, shellWaitMaxTimeout)
	}
	remaining := int(math.Ceil((budget - elapsed).Seconds()))
	encoded, err := json.Marshal(max(remaining, 1))
	if err != nil {
		return "", false
	}
	fields["timeout_seconds"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return "", false
	}
	return string(out), true
}
