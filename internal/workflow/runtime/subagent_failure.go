// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// subAgentFailsAloneChangeID gates ending a sub-agent whose step exhausted its
// retries, instead of self-pausing the whole run (workflow.GetVersion).
//
// It is consulted only on a sub-agent's thread, and only at the exhaustion
// point, which is exactly where the two shapes first differ: the old one
// schedules WorkflowError, writes "paused" and parks every thread in the run on
// the shared pause coordinator; the new one schedules WorkflowError and ends
// the agent. A run that parked behind a sub-agent before this shipped has no
// marker, so it replays the pause it recorded — and a resume re-dispatches the
// step exactly as before (frozen/2026-10-10-subagent-exhaustion-pauses-run).
const subAgentFailsAloneChangeID = "subagent-exhaustion-fails-agent"

// failsAlone reports whether a step that exhausted its retries on this
// controller's thread ends that thread, rather than pausing the run.
//
// Only a sub-agent fails alone. A sub-agent shares its parent's Temporal
// execution and pause coordinator with every other thread in the chat, so a
// pause it arms parks all of them and cancels their in-flight activities: on
// 2026-10-10 one implementer's provider stalls held an orchestrator and its
// other sub-agents for 70 minutes, and earlier the same night a sub-agent's
// permanent 400 cancelled the main thread's healthy call mid-stream. A
// sub-agent has someone better placed to decide than a paused chat — its
// parent — so it ends and reports, the way it reports a completion.
//
// The root thread has no parent to report to; its exhausted step keeps
// pausing the run until the user resumes it.
func (pc *PauseController) failsAlone(ctx workflow.Context) bool {
	if pc == nil || !pc.SubAgent {
		return false
	}
	return workflow.GetVersion(ctx, subAgentFailsAloneChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion
}

// subAgentFailedError ends a sub-agent whose step exhausted its retries. It is
// terminal for that agent: runSpawnInlineChild must not retry it behind the
// parent's back, because deciding whether to retry is the parent's job.
type subAgentFailedError struct {
	StepID string
	// Reason is the cause as one line, for the parent's report.
	Reason string
	Err    error
}

func (e *subAgentFailedError) Error() string {
	return fmt.Sprintf("step %s exhausted its retries: %s", e.StepID, e.Reason)
}

func (e *subAgentFailedError) Unwrap() error { return e.Err }

// endSubAgent writes the exhausted step's error to the sub-agent's OWN thread
// and returns the error that ends the agent. The executor that saw the
// exhaustion returns it, the stack unwinds to runSpawnInlineChild, and that
// reports the failure to the parent's mailbox.
//
// The error row is thread-scoped (exhaustion.Thread is the agent's thread), so
// it renders in that agent's transcript and nowhere else, and its summary says
// what actually happened: the agent stopped, nothing paused.
func endSubAgent(ctx workflow.Context, exhaustion retryExhaustionError, stepID string) error {
	reason := oneLineError(exhaustion.Err)
	exhaustion.Summary = reason + ". This agent stopped; its parent was told and can resume it."

	errorCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})
	_ = workflow.ExecuteActivity(errorCtx, "WorkflowError", exhaustion.payload()).Get(ctx, nil)

	return &subAgentFailedError{StepID: stepID, Reason: reason, Err: exhaustion.Err}
}

// maxReportedErrorRunes bounds the one-line cause handed to a parent. An error
// chain can embed a whole provider response body; the parent needs the cause,
// not the payload.
const maxReportedErrorRunes = 300

// oneLineError renders err as one readable line: the summary a user would be
// shown when the error is a recognised one, otherwise the cause with
// Temporal's bookkeeping removed, cut at its first line.
func oneLineError(err error) string {
	if err == nil {
		return "unknown error"
	}
	raw := err.Error()
	line := extractLLMErrorSummary(raw)
	if line == "" {
		line = cleanTemporalError(raw)
	}
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > maxReportedErrorRunes {
		line = string([]rune(line)[:maxReportedErrorRunes]) + "…"
	}
	if line == "" {
		return "unknown error"
	}
	return line
}

// spawnFailureReport is the agent_result body a parent receives when its
// sub-agent fails: the cause in one line and the handle that resumes the agent
// where it stopped. The parent decides what happens next — resume it, retry
// the work with a fresh agent, or carry on without it — so the report carries
// everything that decision needs and nothing it has to dig for.
//
// The <system> line mirrors fetchSpawnResult's, which is where a parent
// already learns an agent's resumption handle on success.
func spawnFailureReport(title, agentID string, err error) string {
	reason := oneLineError(err)
	how := ""
	var exhausted *subAgentFailedError
	if errors.As(err, &exhausted) {
		reason = exhausted.Reason
		how = fmt.Sprintf(" It stopped at its %s step.", exhausted.StepID)
	}
	if title == "" {
		title = agentID
	}
	return fmt.Sprintf(
		"<system>Use agent_id: %s to resume this agent where it stopped.</system>\n\n"+
			"Agent %q failed: %s.%s\n\n"+
			"Only this agent stopped; nothing else in this chat was paused. Decide what happens next: "+
			"resume it with spawn(agent_id=%q, prompt=\"<what to do now>\"), retry the work with a fresh agent, "+
			"or continue without it.",
		agentID, title, strings.TrimSuffix(reason, "."), how, agentID)
}
