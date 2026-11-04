// Copyright (c) 2025 Reliant Labs
package tools

import (
	"time"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/shellaudit"
)

// shellOutcome collects, as a shell call runs, what its audit-log entry needs
// that only the call's execution knows.
type shellOutcome struct {
	outcome    shellaudit.Outcome
	refusal    string
	exitCode   *int
	durationMs int64
	timedOut   bool
	background bool
	processID  string
}

func (o *shellOutcome) refused(guard string) {
	o.outcome, o.refusal = shellaudit.OutcomeRefused, guard
}

func (o *shellOutcome) backgrounded(processID string) {
	o.outcome, o.background, o.processID = shellaudit.OutcomeBackgrounded, true, processID
}

func (o *shellOutcome) completed(exitCode int, durationMs int64, timedOut bool) {
	o.outcome, o.exitCode, o.durationMs, o.timedOut = shellaudit.OutcomeCompleted, &exitCode, durationMs, timedOut
}

// record writes the entry. It runs for every call, refused ones included: a
// refusal is the evidence that someone tried.
func (o *shellOutcome) record(tc *rctx.ToolContext, params ShellParams, start, end time.Time, err error) {
	log := shellaudit.Default()
	if !log.Enabled() {
		return
	}
	e := shellaudit.Entry{
		Time:       start,
		Command:    params.Command,
		Outcome:    o.outcome,
		Refusal:    o.refusal,
		ExitCode:   o.exitCode,
		DurationMs: o.durationMs,
		TimedOut:   o.timedOut,
		Background: o.background,
		ProcessID:  o.processID,
	}
	if e.Outcome == "" || err != nil {
		e.Outcome = shellaudit.OutcomeError // returned before reaching the machine
	}
	if e.DurationMs == 0 {
		e.DurationMs = end.Sub(start).Milliseconds()
	}
	if tc != nil {
		e.ChatID, e.Thread = tc.ChatID, tc.Thread
		if cwd, cwdErr := ResolveRepoPath(tc, params.Repo); cwdErr == nil {
			e.Cwd = cwd
		}
	}
	log.Record(e)
}
