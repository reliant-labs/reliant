// Copyright (c) 2025 Reliant Labs
package execfollow

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// DefaultHookTimeout bounds a single hook execution so a wedged hook can
// never stall the follow stream.
const DefaultHookTimeout = 60 * time.Second

// HookRunner executes hooks via `sh -c` with the event JSON on stdin and
// RELIANT_EVENT_* environment variables. Hook failures are logged to Stderr
// and never propagate — a broken hook must not kill the follow.
type HookRunner struct {
	// Timeout per hook execution; DefaultHookTimeout when zero.
	Timeout time.Duration
	// Stderr receives hook diagnostics (defaults to os.Stderr).
	Stderr io.Writer
	// Shell overrides the shell binary (defaults to "sh"); tests only.
	Shell string
}

func (r *HookRunner) stderr() io.Writer {
	if r.Stderr != nil {
		return r.Stderr
	}
	return os.Stderr
}

// Run executes every matching hook, in order, synchronously. evJSON is the
// exact NDJSON line already emitted for the event.
func (r *HookRunner) Run(ctx context.Context, hooks []Hook, ev Event, evJSON []byte) {
	for _, h := range hooks {
		if !h.Matches(ev.Event) {
			continue
		}
		r.runOne(ctx, h, ev, evJSON)
	}
}

func (r *HookRunner) runOne(ctx context.Context, h Hook, ev Event, evJSON []byte) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultHookTimeout
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := r.Shell
	if shell == "" {
		shell = "sh"
	}

	cmd := exec.CommandContext(hctx, shell, "-c", h.Cmd)
	cmd.Stdin = bytes.NewReader(evJSON)
	cmd.Stdout = r.stderr() // hook output is diagnostics; keep stdout NDJSON-clean
	cmd.Stderr = r.stderr()
	cmd.Env = append(os.Environ(), hookEnv(ev)...)

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(r.stderr(), "reliant: hook failed (on=%s cmd=%q): %v\n", h.On, h.Cmd, err)
	}
}

// hookEnv builds the RELIANT_EVENT_* variables passed to hook commands.
func hookEnv(ev Event) []string {
	env := []string{
		"RELIANT_EVENT=" + ev.Event,
		"RELIANT_EVENT_EXECUTION_ID=" + ev.ExecutionID,
		"RELIANT_EVENT_WORKFLOW_ID=" + ev.WorkflowID,
		"RELIANT_EVENT_WORKFLOW_NAME=" + ev.WorkflowName,
		"RELIANT_EVENT_NODE_ID=" + ev.NodeID,
		"RELIANT_EVENT_STATE=" + ev.NewState,
		"RELIANT_EVENT_OLD_STATE=" + ev.OldState,
		"RELIANT_EVENT_TIMESTAMP=" + ev.Timestamp,
		"RELIANT_EVENT_SEQUENCE=" + strconv.FormatInt(ev.Sequence, 10),
	}
	// Gate ids let an on=question / on=approval hook act without parsing JSON.
	if ev.Question != nil {
		env = append(env, "RELIANT_EVENT_QUESTION_ID="+ev.Question.QuestionID)
	}
	if ev.Approval != nil {
		env = append(env, "RELIANT_EVENT_APPROVAL_ID="+ev.Approval.ApprovalID)
	}
	return env
}
