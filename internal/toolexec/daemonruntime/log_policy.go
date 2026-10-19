// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// The daemon's log levels, as policy. Hosted daemons are read by us, in bulk,
// so a level must mean the same thing on every line:
//
//	ERROR  an operator must act: the daemon cannot reach the gateway for
//	       minutes, credentials are rejected, a workload was OOM-killed.
//	WARN   degraded but self-healing, or worth trending: a sustained
//	       reconnect streak, a slow or timed-out command, a failed tool.
//	INFO   lifecycle and state transitions, one line per meaningful event:
//	       a connection that came up or went down, a notable command.
//	DEBUG  per-message detail and every success of a hot loop.
//
// Expected cancellation never logs above DEBUG, and a condition that repeats
// on a timer is logged once per window with a count of what was skipped.

const (
	// reconnectWarnAfterFailures and reconnectWarnAfterOutage are when a run of
	// failed sessions stops being routine. A gateway rollout drops every
	// daemon once and they are back within a backoff or two; five failures in
	// a row, or two minutes without a stream, is not a rollout.
	reconnectWarnAfterFailures = 5
	reconnectWarnAfterOutage   = 2 * time.Minute
	// reconnectErrorAfterOutage is "an operator must act".
	reconnectErrorAfterOutage = 5 * time.Minute
	// reconnectRepeatEvery is how often a sustained outage is restated. The
	// backoff retries every 15s, which at WARN or ERROR is 5,760 lines a day
	// from one daemon saying one thing.
	reconnectRepeatEvery = 5 * time.Minute
	// streakResetAfter mirrors the backoff reset in run(): a session shorter
	// than this did not really recover, so it does not end the streak.
	streakResetAfter = 30 * time.Second

	// A command is worth a line at INFO once it takes this long, whatever its
	// outcome. Below it, a successful command is DEBUG.
	notableDaemonCommandThreshold = 2 * time.Second
)

// reconnectDecision is what the session loop should log for one failed
// session.
type reconnectDecision struct {
	level slog.Level
	// emit is false when the line is a repeat inside the restate window. The
	// caller logs it at DEBUG so a DEBUG override still sees every retry.
	emit  bool
	attrs []any
}

// reconnectTracker turns a stream of failed sessions into one decision per
// failure: routine (INFO) until the streak is long or old enough to matter
// (WARN, then ERROR), restated at most once per reconnectRepeatEvery.
type reconnectTracker struct {
	mu          sync.Mutex
	now         func() time.Time // nil means time.Now
	failures    int
	outageStart time.Time
	lastLevel   slog.Level
	lastEmit    time.Time
	suppressed  int
}

func newReconnectTracker(now func() time.Time) *reconnectTracker {
	return &reconnectTracker{now: now}
}

// clockOrNow reads a tracker clock; nil means time.Now, so a zero tracker works.
func clockOrNow(now func() time.Time) time.Time {
	if now == nil {
		return time.Now()
	}
	return now()
}

// failed records one failed session. sessionLasted is how long it was up.
func (t *reconnectTracker) failed(sessionLasted time.Duration) reconnectDecision {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := clockOrNow(t.now)

	if sessionLasted >= streakResetAfter {
		t.failures = 0
		t.lastEmit = time.Time{}
		t.suppressed = 0
	}
	if t.failures == 0 {
		t.outageStart = now
	}
	t.failures++
	disconnectedFor := now.Sub(t.outageStart)

	level := slog.LevelInfo
	switch {
	case disconnectedFor >= reconnectErrorAfterOutage:
		level = slog.LevelError
	case t.failures >= reconnectWarnAfterFailures || disconnectedFor >= reconnectWarnAfterOutage:
		level = slog.LevelWarn
	}

	attrs := []any{"attempt", t.failures, "disconnected_for_ms", disconnectedFor.Milliseconds()}

	// Routine failures always log: there are few, and the first is the one
	// that says what happened. Escalated ones log on reaching a new level and
	// then once per window.
	emit := level == slog.LevelInfo ||
		level > t.lastLevel ||
		t.lastEmit.IsZero() ||
		now.Sub(t.lastEmit) >= reconnectRepeatEvery
	if emit {
		if t.suppressed > 0 {
			attrs = append(attrs, "suppressed", t.suppressed)
		}
		t.suppressed = 0
		t.lastEmit = now
		t.lastLevel = level
	} else {
		t.suppressed++
	}
	return reconnectDecision{level: level, emit: emit, attrs: attrs}
}

// recovered ends the streak and returns how long the outage lasted and how
// many sessions failed in it (zero when there was no outage).
func (t *reconnectTracker) recovered() (failures int, outage time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failures == 0 {
		return 0, 0
	}
	failures, outage = t.failures, clockOrNow(t.now).Sub(t.outageStart)
	t.failures = 0
	t.lastEmit = time.Time{}
	t.lastLevel = slog.LevelInfo
	t.suppressed = 0
	return failures, outage
}

// rejectionTracker deduplicates the "credentials not accepted" line. The
// first rejection of a streak is an ERROR with the likely causes; repeats are
// one line per window carrying a count, and the rest are DEBUG.
type rejectionTracker struct {
	mu    sync.Mutex
	now   func() time.Time // nil means time.Now
	total int
	// windowStart/suppressed implement one line per window with a count.
	windowStart time.Time
	suppressed  int
}

func newRejectionTracker(now func() time.Time) *rejectionTracker {
	return &rejectionTracker{now: now}
}

type rejectionDecision struct {
	// first is the first rejection of a streak: log the full explanation.
	first bool
	// emit is false for a repeat inside the window.
	emit       bool
	suppressed int
	total      int
}

func (t *rejectionTracker) rejected() rejectionDecision {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := clockOrNow(t.now)
	t.total++
	d := rejectionDecision{first: t.total == 1, total: t.total}
	if t.total == 1 || now.Sub(t.windowStart) >= reconnectRepeatEvery {
		d.emit, d.suppressed = true, t.suppressed
		t.windowStart, t.suppressed = now, 0
		return d
	}
	t.suppressed++
	return d
}

// reset ends the streak; the next rejection is a first again.
func (t *rejectionTracker) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.total, t.suppressed, t.windowStart = 0, 0, time.Time{}
}

// streamTransitions logs the gateway stream's state changes with how long the
// previous state lasted. Connecting and repeat-disconnects are DEBUG: during
// an outage the reconnect tracker owns the story, and a retry every 15s would
// otherwise be three INFO lines each.
type streamTransitions struct {
	mu    sync.Mutex
	now   func() time.Time // nil means time.Now
	state daemonstate.Stream
	since time.Time
	// lastConnected is when the stream last came up, for connected_for.
	lastConnected time.Time
	// outageStart is when the stream was lost and not yet restored.
	outageStart time.Time
}

// transition returns the record to log, or ok=false when nothing changed.
func (s *streamTransitions) transition(next daemonstate.Stream, reason string) (level slog.Level, attrs []any, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := clockOrNow(s.now)
	prev := s.state
	if next == prev {
		return 0, nil, false
	}
	prevFor := time.Duration(0)
	if !s.since.IsZero() {
		prevFor = now.Sub(s.since)
	}
	s.state, s.since = next, now

	attrs = []any{
		"state", string(next),
		"prev_state", string(prev),
		"prev_state_ms", prevFor.Milliseconds(),
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	level = slog.LevelInfo
	switch next {
	case daemonstate.StreamConnected:
		if !s.outageStart.IsZero() {
			attrs = append(attrs, "disconnected_for_ms", now.Sub(s.outageStart).Milliseconds())
			s.outageStart = time.Time{}
		}
		s.lastConnected = now
	case daemonstate.StreamDisconnected:
		if prev == daemonstate.StreamConnected {
			s.outageStart = now
			attrs = append(attrs, "connected_for_ms", now.Sub(s.lastConnected).Milliseconds())
		} else {
			level = slog.LevelDebug // another failed dial in a streak already announced
		}
	case daemonstate.StreamConnecting:
		level = slog.LevelDebug
	}
	return level, attrs, true
}

// errSessionEnded is what send returns once the stream is gone. A send that
// fails with it is a symptom of a disconnect that was already logged.
var errSessionEnded = errors.New("daemon session ended")

// logSendFailure logs a message the daemon could not enqueue. When the stream
// has ended the message is a symptom of the disconnect, not news, so it is
// DEBUG; any other failure is a WARN.
func logSendFailure(msg string, err error, attrs ...any) {
	level := slog.LevelWarn
	if errors.Is(err, errSessionEnded) || errors.Is(err, context.Canceled) {
		level = slog.LevelDebug
	}
	logging.Log(level, msg, append(attrs, "error", err)...)
}

// daemonCommandLogLevel is the level of the one line a handled command gets:
// DEBUG when it was fast and fine, INFO when it failed or took a noticeable
// time, WARN when it took long enough to hold up the caller.
func daemonCommandLogLevel(elapsed time.Duration, failed bool) slog.Level {
	switch {
	case elapsed >= slowDaemonCommandThreshold:
		return slog.LevelWarn
	case failed || elapsed >= notableDaemonCommandThreshold:
		return slog.LevelInfo
	}
	return slog.LevelDebug
}

// summarizeReason bounds a disconnect reason. It is an error string from the
// transport, which is short; the bound keeps a wrapped chain from becoming the
// bulk of the line.
func summarizeReason(detail string) string {
	const max = 200
	if len(detail) <= max {
		return detail
	}
	return detail[:max] + "…"
}

// terminalFailureLog logs a failed terminal write or resize. These are mostly
// the browser closing a tab: the session is already gone and the client is
// told through the session event, so the daemon's line is DEBUG. Anything else
// (a PTY that errored while the session is live) stays a WARN.
func terminalFailureLog(err error, msg string, attrs ...any) {
	level := slog.LevelWarn
	if err != nil && (strings.Contains(err.Error(), "session not found") ||
		errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || errors.Is(err, context.Canceled)) {
		level = slog.LevelDebug
	}
	logging.Log(level, msg, attrs...)
}

// logExecFinished is the one lifecycle line for an exec command that did not
// simply succeed quickly: a non-zero exit, a timeout, a kill, truncated
// output, or just a long run. It names the class of outcome and the numbers,
// never the command line, its environment or any output. An OOM kill is the
// one ERROR, with the memory figures an operator needs.
func logExecFinished(outcome daemon.ExecOutcome, duration time.Duration) {
	class, level := "ok", slog.LevelDebug
	switch {
	case outcome.OOMKilled:
		class, level = "oom_killed", slog.LevelError
	case outcome.TimedOut:
		class, level = "timeout", slog.LevelWarn
	case outcome.OutputIncomplete:
		class, level = "output_incomplete", slog.LevelWarn
	case outcome.ExitCode != 0:
		// A command that exits non-zero is usually the model's own test or
		// build failing: expected, and the tool result already carries it.
		class, level = "nonzero_exit", slog.LevelInfo
	case duration >= slowDaemonCommandThreshold:
		level = slog.LevelInfo
	}
	attrs := []any{"outcome_class", class, "exit_code", outcome.ExitCode, "duration_ms", duration.Milliseconds()}
	if outcome.OOMKilled {
		if stats, ok := memReader.Stats(); ok {
			attrs = append(attrs, "memory_limit_bytes", stats.LimitBytes,
				"memory_unreclaimable_bytes", stats.UnreclaimableBytes,
				"memory_current_bytes", stats.CurrentBytes, "memory_peak_bytes", stats.PeakBytes)
		}
	}
	logging.Log(level, logPrefix+" exec finished", attrs...)
}

// logToolFinished is the one line a tool execution gets when it did not
// succeed. error_code is the closed set the executor assigns, so it can be
// counted; the message and content (which can hold file contents or command
// output) are deliberately not logged. A cancellation is the user's, not a
// fault, and stays DEBUG.
func logToolFinished(toolName string, result *toolexec.ExecutionResult, duration time.Duration) {
	if result == nil || result.Success && !result.IsError {
		if duration >= slowDaemonCommandThreshold {
			logging.Info(logPrefix+" tool finished", "tool", toolName, "outcome", "ok", "duration_ms", duration.Milliseconds())
		}
		return
	}
	level := slog.LevelWarn
	if result.ErrorCode == "CANCELLED" {
		level = slog.LevelDebug
	}
	logging.Log(level, logPrefix+" tool finished",
		"tool", toolName, "outcome", "failed", "error_code", result.ErrorCode,
		"backgrounded", result.Backgrounded, "duration_ms", duration.Milliseconds())
}
