// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *fakeClock                   { return &fakeClock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)} }

// capturePolicyLogs installs a text logger at DEBUG and returns what it wrote,
// so a test asserts on the level and fields a line actually carries.
func capturePolicyLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func lineWith(t *testing.T, out *bytes.Buffer, substr string) string {
	t.Helper()
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, substr) {
			return l
		}
	}
	return ""
}

// A gateway rollout drops every daemon once. Routine failures are INFO with an
// attempt count; they escalate to WARN on a long streak or a long outage, to
// ERROR when an operator has to act, and are restated at most once per window.
func TestReconnectEscalation(t *testing.T) {
	clock := newClock()
	tr := newReconnectTracker(clock.now)

	// The first failures are routine and always logged.
	for attempt := 1; attempt < reconnectWarnAfterFailures; attempt++ {
		d := tr.failed(0)
		require.True(t, d.emit, "attempt %d", attempt)
		assert.Equal(t, slog.LevelInfo, d.level, "attempt %d", attempt)
		assert.Contains(t, d.attrs, attempt)
		clock.advance(2 * time.Second)
	}

	// The Nth consecutive failure escalates to WARN.
	d := tr.failed(0)
	assert.Equal(t, slog.LevelWarn, d.level)
	assert.True(t, d.emit, "reaching a new level is always logged")
	clock.advance(15 * time.Second)

	// Further failures inside the window are suppressed, but counted.
	var suppressed int
	for i := 0; i < 5; i++ {
		d = tr.failed(0)
		assert.Equal(t, slog.LevelWarn, d.level)
		assert.False(t, d.emit, "a repeat inside the window must not be logged at WARN")
		suppressed++
		clock.advance(15 * time.Second)
	}

	// A long outage reaches ERROR even though the window has not elapsed.
	clock.advance(reconnectErrorAfterOutage)
	d = tr.failed(0)
	assert.Equal(t, slog.LevelError, d.level)
	require.True(t, d.emit)
	assert.Contains(t, d.attrs, "suppressed")
	assert.Contains(t, d.attrs, suppressed, "the suppressed count rides on the next line")

	// Then once per window.
	clock.advance(30 * time.Second)
	assert.False(t, tr.failed(0).emit)
	clock.advance(reconnectRepeatEvery)
	assert.True(t, tr.failed(0).emit)
}

// Two minutes without a stream is a WARN even when the attempts were few
// because each backoff was long.
func TestReconnectWarnsOnLongOutageWithFewAttempts(t *testing.T) {
	clock := newClock()
	tr := newReconnectTracker(clock.now)
	assert.Equal(t, slog.LevelInfo, tr.failed(0).level)
	clock.advance(reconnectWarnAfterOutage + time.Second)
	assert.Equal(t, slog.LevelWarn, tr.failed(0).level)
}

// A session that stayed up ends the streak: the next drop is routine again.
func TestReconnectStreakResetsAfterAHealthySession(t *testing.T) {
	clock := newClock()
	tr := newReconnectTracker(clock.now)
	for i := 0; i < reconnectWarnAfterFailures+2; i++ {
		tr.failed(0)
		clock.advance(time.Second)
	}
	d := tr.failed(10 * time.Minute) // this one was up for ten minutes before dropping
	assert.Equal(t, slog.LevelInfo, d.level)
	assert.Contains(t, d.attrs, 1, "attempt restarts at 1")
}

// A session that connected only briefly (under the backoff-reset threshold)
// does not count as recovery.
func TestReconnectFlappingSessionDoesNotResetStreak(t *testing.T) {
	clock := newClock()
	tr := newReconnectTracker(clock.now)
	for i := 0; i < reconnectWarnAfterFailures; i++ {
		tr.failed(2 * time.Second)
		clock.advance(3 * time.Second)
	}
	assert.Equal(t, slog.LevelWarn, tr.failed(2*time.Second).level)
}

func TestReconnectRecoveredReportsTheOutage(t *testing.T) {
	clock := newClock()
	tr := newReconnectTracker(clock.now)
	failures, _ := tr.recovered()
	assert.Zero(t, failures, "no outage, nothing to report")

	tr.failed(0)
	clock.advance(10 * time.Second)
	tr.failed(0)
	clock.advance(20 * time.Second)
	failures, outage := tr.recovered()
	assert.Equal(t, 2, failures)
	assert.Equal(t, 30*time.Second, outage)

	failures, _ = tr.recovered()
	assert.Zero(t, failures, "reported once")
}

// Credentials the gateway will not accept are retried every backoff tick, up
// to every 15s. The first is the ERROR with the explanation; the rest are one
// line per window with a counter, and the retries between them are silent.
func TestCredentialRejectionIsDeduplicated(t *testing.T) {
	clock := newClock()
	tr := newRejectionTracker(clock.now)

	first := tr.rejected()
	require.True(t, first.first)
	require.True(t, first.emit)

	emitted, suppressedOnEmit := 0, 0
	for i := 0; i < 20; i++ { // 20 retries at 15s = exactly one window
		clock.advance(15 * time.Second)
		d := tr.rejected()
		assert.False(t, d.first)
		if d.emit {
			emitted++
			suppressedOnEmit = d.suppressed
		}
	}
	assert.Equal(t, 1, emitted, "one restatement per window, not one per retry")
	assert.Equal(t, 19, suppressedOnEmit, "the restatement carries the count of retries it stands for")
	assert.Equal(t, 21, tr.total)
}

func TestCredentialRejectionStreakResets(t *testing.T) {
	clock := newClock()
	tr := newRejectionTracker(clock.now)
	tr.rejected()
	tr.rejected()
	tr.reset()
	d := tr.rejected()
	assert.True(t, d.first, "after credentials were accepted, a new rejection is a fresh ERROR")
	assert.True(t, d.emit)
}

func TestDaemonCommandLogLevel(t *testing.T) {
	cases := []struct {
		name    string
		elapsed time.Duration
		failed  bool
		want    slog.Level
	}{
		{"fast success is DEBUG", 3 * time.Millisecond, false, slog.LevelDebug},
		{"just under notable is DEBUG", notableDaemonCommandThreshold - time.Millisecond, false, slog.LevelDebug},
		{"notable success is INFO", notableDaemonCommandThreshold, false, slog.LevelInfo},
		{"fast failure is INFO", 3 * time.Millisecond, true, slog.LevelInfo},
		{"slow success is WARN", slowDaemonCommandThreshold, false, slog.LevelWarn},
		{"slow failure is WARN", slowDaemonCommandThreshold + time.Second, true, slog.LevelWarn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, daemonCommandLogLevel(tc.elapsed, tc.failed))
		})
	}
}

// The line's keys are snake_case and carry no payload.
func TestLogDaemonCommandHandledKeys(t *testing.T) {
	out := capturePolicyLogs(t)
	logDaemonCommandHandled(context.Background(), nil, 5*time.Millisecond, nil, 3)
	line := lineWith(t, out, "daemon command handled")
	require.NotEmpty(t, line)
	assert.Contains(t, line, "level=DEBUG")
	assert.Contains(t, line, "outcome=ok")
	assert.Contains(t, line, "send_backlog=3")
	assert.Contains(t, line, "elapsed_ms=5")
	assert.NotContains(t, line, "requestID")

	out.Reset()
	logDaemonCommandHandled(context.Background(), nil, 5*time.Millisecond, errors.New("boom"), 0)
	assert.Contains(t, lineWith(t, out, "daemon command handled"), "level=INFO")
}

func TestStreamTransitionsAreLoggedOnceWithDurations(t *testing.T) {
	clock := newClock()
	st := &streamTransitions{now: clock.now}

	// Connecting is DEBUG: it is only the start of a dial.
	level, _, ok := st.transition(daemonstate.StreamConnecting, "")
	require.True(t, ok)
	assert.Equal(t, slog.LevelDebug, level)

	clock.advance(400 * time.Millisecond)
	level, attrs, ok := st.transition(daemonstate.StreamConnected, "")
	require.True(t, ok)
	assert.Equal(t, slog.LevelInfo, level)
	assert.Contains(t, attrs, "connected")
	assert.Contains(t, attrs, int64(400), "how long the previous state lasted")

	_, _, ok = st.transition(daemonstate.StreamConnected, "")
	assert.False(t, ok, "no change, no line")

	clock.advance(2 * time.Hour)
	level, attrs, ok = st.transition(daemonstate.StreamDisconnected, "EOF")
	require.True(t, ok)
	assert.Equal(t, slog.LevelInfo, level, "losing an established stream is lifecycle news")
	assert.Contains(t, attrs, "connected_for_ms")
	assert.Contains(t, attrs, (2 * time.Hour).Milliseconds())
	assert.Contains(t, attrs, "EOF")

	// The retry loop connecting/disconnecting again is DEBUG...
	clock.advance(time.Second)
	level, _, _ = st.transition(daemonstate.StreamConnecting, "")
	assert.Equal(t, slog.LevelDebug, level)
	clock.advance(time.Second)
	level, _, _ = st.transition(daemonstate.StreamDisconnected, "EOF")
	assert.Equal(t, slog.LevelDebug, level, "a failed dial in a streak is not a second outage")

	// ... and the recovery says how long the outage was.
	clock.advance(30 * time.Second)
	st.transition(daemonstate.StreamConnecting, "")
	clock.advance(time.Second)
	level, attrs, _ = st.transition(daemonstate.StreamConnected, "")
	assert.Equal(t, slog.LevelInfo, level)
	assert.Contains(t, attrs, "disconnected_for_ms")
	assert.Contains(t, attrs, (33 * time.Second).Milliseconds(), "1s+1s+30s+1s from the moment the stream was lost")
}

func TestLogSendFailureLevel(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"session ended is a symptom":   {errSessionEnded, "level=DEBUG"},
		"wrapped session end":          {fmt.Errorf("sending: %w", errSessionEnded), "level=DEBUG"},
		"cancellation":                 {context.Canceled, "level=DEBUG"},
		"a genuine send failure warns": {errors.New("marshal: invalid utf-8"), "level=WARN"},
	} {
		t.Run(name, func(t *testing.T) {
			out := capturePolicyLogs(t)
			logSendFailure("Failed to send x", tc.err, "request_id", "r1")
			assert.Contains(t, lineWith(t, out, "Failed to send x"), tc.want)
		})
	}
}

func TestTerminalFailureLevel(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"closed session":  {errors.New("session not found: abc"), "level=DEBUG"},
		"eof":             {io.EOF, "level=DEBUG"},
		"live pty broken": {errors.New("write /dev/ptmx: input/output error"), "level=WARN"},
	} {
		t.Run(name, func(t *testing.T) {
			out := capturePolicyLogs(t)
			terminalFailureLog(tc.err, "Failed to write terminal input", "session_id", "s1")
			assert.Contains(t, lineWith(t, out, "Failed to write terminal input"), tc.want)
		})
	}
}

func TestLogExecFinishedClassifiesWithoutPayloads(t *testing.T) {
	cases := []struct {
		name    string
		outcome daemon.ExecOutcome
		dur     time.Duration
		level   string
		class   string
	}{
		{"fast success is quiet", daemon.ExecOutcome{}, 20 * time.Millisecond, "level=DEBUG", "outcome_class=ok"},
		{"slow success", daemon.ExecOutcome{}, 12 * time.Second, "level=INFO", "outcome_class=ok"},
		{"non-zero exit", daemon.ExecOutcome{ExitCode: 2, Stderr: "SECRET-IN-STDERR"}, time.Second, "level=INFO", "outcome_class=nonzero_exit"},
		{"timeout", daemon.ExecOutcome{ExitCode: 124, TimedOut: true}, time.Minute, "level=WARN", "outcome_class=timeout"},
		{"output cut off", daemon.ExecOutcome{OutputIncomplete: true}, time.Second, "level=WARN", "outcome_class=output_incomplete"},
		{"oom kill", daemon.ExecOutcome{ExitCode: 137, OOMKilled: true}, time.Second, "level=ERROR", "outcome_class=oom_killed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := capturePolicyLogs(t)
			logExecFinished(tc.outcome, tc.dur)
			line := lineWith(t, out, "exec finished")
			require.NotEmpty(t, line)
			assert.Contains(t, line, tc.level)
			assert.Contains(t, line, tc.class)
			assert.Contains(t, line, "duration_ms=")
			assert.NotContains(t, line, "SECRET-IN-STDERR", "output must never be logged")
		})
	}
}

func TestLogToolFinished(t *testing.T) {
	t.Run("failure is WARN with a class and no content", func(t *testing.T) {
		out := capturePolicyLogs(t)
		logToolFinished("read_file", &toolexec.ExecutionResult{
			IsError: true, ErrorCode: "EXECUTION_ERROR",
			Content: "FILE-CONTENTS", ErrorMessage: "open /home/u/secret.txt: permission denied",
		}, 40*time.Millisecond)
		line := lineWith(t, out, "tool finished")
		assert.Contains(t, line, "level=WARN")
		assert.Contains(t, line, "error_code=EXECUTION_ERROR")
		assert.Contains(t, line, "tool=read_file")
		assert.NotContains(t, line, "FILE-CONTENTS")
		assert.NotContains(t, line, "secret.txt")
	})
	t.Run("cancellation is not a fault", func(t *testing.T) {
		out := capturePolicyLogs(t)
		logToolFinished("bash", &toolexec.ExecutionResult{IsError: true, ErrorCode: "CANCELLED"}, time.Second)
		assert.Contains(t, lineWith(t, out, "tool finished"), "level=DEBUG")
	})
	t.Run("fast success logs nothing", func(t *testing.T) {
		out := capturePolicyLogs(t)
		logToolFinished("bash", &toolexec.ExecutionResult{Success: true}, 50*time.Millisecond)
		assert.Empty(t, lineWith(t, out, "tool finished"))
	})
	t.Run("slow success is INFO", func(t *testing.T) {
		out := capturePolicyLogs(t)
		logToolFinished("bash", &toolexec.ExecutionResult{Success: true}, 11*time.Second)
		assert.Contains(t, lineWith(t, out, "tool finished"), "level=INFO")
	})
}

// forge_memory used to hide a real failure at DEBUG. It is a WARN now, once
// per project per window.
func TestForgeMemoryRenderFailureWarnsOnce(t *testing.T) {
	out := capturePolicyLogs(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(": : not yaml : ["), 0o644))

	_, ok := forgeFrameworkMemory(dir)
	if ok {
		t.Skip("forge rendered this fixture; cannot provoke the failure path")
	}
	_, _ = forgeFrameworkMemory(dir)

	assert.Equal(t, 1, strings.Count(out.String(), "forge framework memory render failed"), "throttled to one per project")
	assert.Contains(t, lineWith(t, out, "forge framework memory render failed"), "level=WARN")
}

// A daemonClient built as a literal must be able to log through every policy
// tracker: tests construct clients that way, and so could any future caller.
func TestZeroValueTrackersAreUsable(t *testing.T) {
	var d daemonClient
	assert.NotPanics(t, func() {
		d.reconnects.failed(0)
		d.reconnects.recovered()
		d.rejections.rejected()
		d.rejections.reset()
		d.transitions.transition(daemonstate.StreamConnected, "")
	})
}
