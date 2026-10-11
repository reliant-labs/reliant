// Copyright (c) 2025 Reliant Labs
package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// isolateDaemonLogging is isolateLogging plus the daemon-only globals, and a
// clean slate for every variable SetupDaemon reads.
func isolateDaemonLogging(t *testing.T) {
	t.Helper()
	isolateLogging(t)
	for _, name := range []string{"LOG_FORMAT", workspaceIDEnv, orgIDEnv, "RELIANT_ENV", "NODE_ENV"} {
		t.Setenv(name, "")
	}
	prevFields := daemonFields.Load()
	prevNotices := notices.Load()
	prevLevel := levelVar.Level()
	t.Cleanup(func() {
		daemonFields.Store(prevFields)
		notices.Store(prevNotices)
		levelVar.Set(prevLevel)
	})
}

// captureStdout swaps os.Stdout for a pipe for the duration of fn and returns
// what was written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() { os.Stdout = prev }()
		fn()
	}()
	require.NoError(t, w.Close())
	return <-done
}

func jsonLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec), "not JSON: %q", line)
		recs = append(recs, rec)
	}
	return recs
}

func TestDaemonLogLevel(t *testing.T) {
	cases := []struct {
		name         string
		env          map[string]string
		want         slog.Level
		wantExplicit bool
	}{
		{"default is INFO", nil, slog.LevelInfo, false},
		{"debug", map[string]string{"RELIANT_LOG_LEVEL": "debug"}, slog.LevelDebug, true},
		{"upper case", map[string]string{"RELIANT_LOG_LEVEL": "WARN"}, slog.LevelWarn, true},
		{"mixed case with spaces", map[string]string{"RELIANT_LOG_LEVEL": " Error "}, slog.LevelError, true},
		{"warning spelling", map[string]string{"RELIANT_LOG_LEVEL": "warning"}, slog.LevelWarn, true},
		{"info", map[string]string{"RELIANT_LOG_LEVEL": "info"}, slog.LevelInfo, true},
		{"typo falls back to INFO, not explicit", map[string]string{"RELIANT_LOG_LEVEL": "verbose"}, slog.LevelInfo, false},
		{"ambient DEBUG flag in prod stays INFO", map[string]string{"DEBUG": "true"}, slog.LevelInfo, false},
		{"explicit info outranks DEBUG flag in dev", map[string]string{"RELIANT_ENV": "dev", "RELIANT_LOG_LEVEL": "info", "DEBUG": "true"}, slog.LevelInfo, true},
		{"DEBUG flag in dev", map[string]string{"RELIANT_ENV": "dev", "DEBUG": "true"}, slog.LevelDebug, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateDaemonLogging(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, explicit := DaemonLogLevel()
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantExplicit, explicit)
		})
	}
}

// The prod DEBUG block is lifted for the daemon only when it is asked for by
// name; the servers keep it.
func TestDaemonAllowsExplicitDebugInProd(t *testing.T) {
	isolateDaemonLogging(t)
	t.Setenv("RELIANT_LOG_LEVEL", "debug")
	var buf bytes.Buffer

	SetupDaemon(DaemonLogConfig{Output: &buf, Fields: DaemonFields{Version: "v1"}})
	Debug("debug-line-marker")

	assert.Contains(t, buf.String(), "debug-line-marker")
	assert.NotContains(t, buf.String(), refusedDebugMessage)
	assert.Equal(t, slog.LevelDebug, CurrentLevel())

	// ... and the servers' path is unchanged.
	buf.Reset()
	Setup(GetLogLevel())
	Debug("server-debug-marker")
	assert.NotContains(t, buf.String(), "server-debug-marker")
	assert.Contains(t, buf.String(), refusedDebugMessage)
}

func TestDaemonDefaultsToInfoInProd(t *testing.T) {
	isolateDaemonLogging(t)
	var buf bytes.Buffer
	SetupDaemon(DaemonLogConfig{Output: &buf})
	Debug("debug-line-marker")
	Info("info-line-marker")
	assert.NotContains(t, buf.String(), "debug-line-marker")
	assert.Contains(t, buf.String(), "info-line-marker")
}

func TestUnrecognisedLogLevelIsReported(t *testing.T) {
	isolateDaemonLogging(t)
	t.Setenv("RELIANT_LOG_LEVEL", "verbose")
	var buf bytes.Buffer
	SetupDaemon(DaemonLogConfig{Output: &buf})
	assert.Contains(t, buf.String(), "Unrecognised RELIANT_LOG_LEVEL")
	assert.Contains(t, buf.String(), `value=verbose`)
}

// Managed daemons write JSON to stdout AND the file, every record carries the
// required fields, and the human banner becomes a record instead of a raw line
// between JSON lines.
func TestSetupDaemonManagedWritesJSONToStdoutAndFile(t *testing.T) {
	isolateDaemonLogging(t)
	t.Setenv(workspaceIDEnv, "ws_42")
	t.Setenv(orgIDEnv, "org_7")
	dir := t.TempDir()
	file := filepath.Join(dir, "logs", "tools-daemon.log")

	var notice io.Writer
	out := captureStdout(t, func() {
		SetupDaemon(DaemonLogConfig{
			Rotation: &RotationConfig{Filename: file},
			Stdout:   true,
			Managed:  true,
			Fields:   DaemonFields{DaemonID: "dmn_1", Version: "v9.9.9"},
		})
		Info("hello", "k", "v")
		notice = Notices()
		_, _ = io.WriteString(notice, "\n  ✓ Daemon connected — you can head back to Reliant.\n\n")
	})
	require.NoError(t, Close())

	recs := jsonLines(t, out)
	require.Len(t, recs, 2, "every stdout line must be one JSON record, banner included:\n%s", out)
	for _, rec := range recs {
		assert.Equal(t, "reliant-daemon", rec["service"])
		assert.Equal(t, "v9.9.9", rec["version"])
		assert.Equal(t, "dmn_1", rec["daemon_id"])
		assert.Equal(t, "ws_42", rec["workspace_id"])
		assert.Equal(t, "org_7", rec["org_id"])
		assert.Equal(t, "INFO", rec["level"])
	}
	assert.Equal(t, "v", recs[0]["k"])
	assert.Equal(t, "daemon notice", recs[1]["msg"])
	assert.Contains(t, recs[1]["notice"], "Daemon connected")

	onDisk, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, out, string(onDisk), "file and stdout must carry the same lines")
}

// The logging variables are read once and removed, so the daemon's children
// (a customer's dev server, their tests) do not inherit JSON mode.
func TestSetupDaemonManagedScrubsLoggingEnv(t *testing.T) {
	isolateDaemonLogging(t)
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("RELIANT_LOG_LEVEL", "info")
	t.Setenv(workspaceIDEnv, "ws_1")

	SetupDaemon(DaemonLogConfig{Managed: true, Output: io.Discard})

	for _, name := range []string{"LOG_FORMAT", "RELIANT_LOG_LEVEL", workspaceIDEnv, orgIDEnv} {
		_, set := os.LookupEnv(name)
		assert.False(t, set, "%s left in the process environment", name)
	}
	assert.Equal(t, "ws_1", daemonFields.Load().WorkspaceID, "the value was read before it was removed")
}

// A person's own daemon keeps its environment and its text output.
func TestSetupDaemonUnmanagedStaysText(t *testing.T) {
	isolateDaemonLogging(t)
	t.Setenv("LOG_FORMAT", "json")
	var buf bytes.Buffer
	SetupDaemon(DaemonLogConfig{Output: &buf})
	Info("hello")
	assert.True(t, json.Valid(bytes.TrimSpace(buf.Bytes())), "an explicit LOG_FORMAT=json is honoured: %s", buf.String())
	assert.Equal(t, "json", os.Getenv("LOG_FORMAT"), "an unmanaged daemon must not touch the environment")

	isolateDaemonLogging(t)
	buf.Reset()
	SetupDaemon(DaemonLogConfig{Output: &buf})
	Info("hello")
	assert.Contains(t, buf.String(), "level=INFO", "unmanaged defaults to text")
}

func TestSetupDaemonManagedHonoursExplicitTextFormat(t *testing.T) {
	isolateDaemonLogging(t)
	t.Setenv("LOG_FORMAT", "text")
	out := captureStdout(t, func() {
		SetupDaemon(DaemonLogConfig{Stdout: true, Managed: true})
		Info("hello")
	})
	assert.Contains(t, out, "level=INFO")
}

// With no file to write, a daemon still logs: silent loss is worse than a
// duplicate stream.
func TestSetupDaemonFallsBackToStdoutWithoutFile(t *testing.T) {
	isolateDaemonLogging(t)
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	out := captureStdout(t, func() {
		SetupDaemon(DaemonLogConfig{Rotation: &RotationConfig{Filename: filepath.Join(blocker, "sub", "x.log")}, Managed: true})
		Info("still-logging")
	})
	assert.Contains(t, out, "still-logging")
}

func TestSetDaemonIDRebindsFromThenOn(t *testing.T) {
	isolateDaemonLogging(t)
	var buf bytes.Buffer
	t.Setenv("LOG_FORMAT", "json")
	SetupDaemon(DaemonLogConfig{Output: &buf})

	Info("before")
	SetDaemonID("dmn_assigned")
	Info("after")
	SetDaemonID("")
	Info("cleared")

	recs := jsonLines(t, buf.String())
	require.Len(t, recs, 3)
	assert.NotContains(t, recs[0], "daemon_id")
	assert.Equal(t, "dmn_assigned", recs[1]["daemon_id"])
	assert.NotContains(t, recs[2], "daemon_id")
}

// A logger derived BEFORE the id was known (slog.With at package init) must
// still pick the id up, which is why identity is read per record.
func TestIdentityReachesLoggersDerivedEarly(t *testing.T) {
	isolateDaemonLogging(t)
	var buf bytes.Buffer
	t.Setenv("LOG_FORMAT", "json")
	SetupDaemon(DaemonLogConfig{Output: &buf})
	derived := slog.Default().With("component", "x").WithGroup("g")
	SetDaemonID("dmn_late")
	derived.Info("hi")
	recs := jsonLines(t, buf.String())
	require.Len(t, recs, 1)
	// Under an open group the stamped fields nest, as with any attribute.
	assert.Contains(t, buf.String(), "dmn_late")
}

func TestCallerSuppliedFieldIsNotOverwritten(t *testing.T) {
	isolateDaemonLogging(t)
	var buf bytes.Buffer
	t.Setenv("LOG_FORMAT", "json")
	SetupDaemon(DaemonLogConfig{Output: &buf, Fields: DaemonFields{DaemonID: "dmn_base"}})
	Info("explicit", "daemon_id", "dmn_override")
	recs := jsonLines(t, buf.String())
	assert.Equal(t, "dmn_override", recs[0]["daemon_id"])
}

func TestTraceIDsAppearWhenASpanIsActive(t *testing.T) {
	isolateDaemonLogging(t)
	var buf bytes.Buffer
	t.Setenv("LOG_FORMAT", "json")
	SetupDaemon(DaemonLogConfig{Output: &buf})

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	slog.InfoContext(ctx, "with span")
	slog.Info("without span")

	recs := jsonLines(t, buf.String())
	require.Len(t, recs, 2)
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", recs[0]["trace_id"])
	assert.Equal(t, "0102030405060708", recs[0]["span_id"])
	assert.NotContains(t, recs[1], "trace_id")
}

// Redaction is part of the daemon chain, not an opt-in.
func TestDaemonLoggerRedacts(t *testing.T) {
	isolateDaemonLogging(t)
	var buf bytes.Buffer
	t.Setenv("LOG_FORMAT", "json")
	SetupDaemon(DaemonLogConfig{Output: &buf})
	Info("cloning https://bot:hunter2hunter2@github.com/a/b", "token", "plainvalue", "content", strings.Repeat("c", 3000))
	out := buf.String()
	assert.NotContains(t, out, "hunter2hunter2")
	assert.NotContains(t, out, "plainvalue")
	assert.Contains(t, out, "truncated")
	assert.Less(t, len(out), 1500)
}

// The teeWriter keeps writing to the file when stdout is gone.
func TestTeeWriterSurvivesAFailingDestination(t *testing.T) {
	var good bytes.Buffer
	tee := &teeWriter{writers: []io.Writer{failWriter{}, &good}}
	n, err := tee.Write([]byte("line\n"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "line\n", good.String())
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
