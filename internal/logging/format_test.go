// Copyright (c) 2025 Reliant Labs
package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateLogging restores the process logger and this package's globals after
// a test that calls a Setup function, so no test inherits another's handler.
// It also clears every variable that asks for DEBUG, so a developer's shell
// cannot make install emit the prod refusal WARN into a test's output.
func isolateLogging(t *testing.T) {
	t.Helper()
	for _, name := range []string{"RELIANT_LOG_LEVEL", "DEBUG", "RELIANT_DEV_DEBUG"} {
		t.Setenv(name, "")
	}
	previousDefault := slog.Default()
	previousOutput := DefaultOutput
	previousActive := activeLogger
	previousTrace := TraceEnabled
	t.Cleanup(func() {
		_ = Close()
		slog.SetDefault(previousDefault)
		DefaultOutput = previousOutput
		activeLogger = previousActive
		TraceEnabled = previousTrace
	})
}

// setupEntryPoint is one of the ways a process installs its logger. Each
// writes somewhere different; read returns what it wrote.
type setupEntryPoint struct {
	name  string
	setup func(t *testing.T) (read func() string)
}

var setupEntryPoints = []setupEntryPoint{
	{
		name: "Setup",
		setup: func(t *testing.T) func() string {
			var buf bytes.Buffer
			DefaultOutput = &buf
			Setup(slog.LevelInfo)
			return buf.String
		},
	},
	{
		name: "SetupWithRotation",
		setup: func(t *testing.T) func() string {
			file := filepath.Join(t.TempDir(), "api.log")
			SetupWithRotation(slog.LevelInfo, false, &RotationConfig{Filename: file})
			return readLogFile(t, file)
		},
	},
	{
		name: "SetupFileOnly",
		setup: func(t *testing.T) func() string {
			file := filepath.Join(t.TempDir(), "daemon.log")
			SetupFileOnly(slog.LevelInfo, &RotationConfig{Filename: file})
			return readLogFile(t, file)
		},
	},
}

func readLogFile(t *testing.T, file string) func() string {
	return func() string {
		content, err := os.ReadFile(file)
		require.NoError(t, err)
		return string(content)
	}
}

// LOG_FORMAT=json must produce one JSON object per line from every Setup
// entry point. The prod pipeline indexes JSON; a single entry point left on
// text would show up there as unparsed blobs.
func TestLogFormatJSON(t *testing.T) {
	for _, format := range []string{"json", "JSON", " json "} {
		for _, entry := range setupEntryPoints {
			t.Run(entry.name+"/LOG_FORMAT="+format, func(t *testing.T) {
				isolateLogging(t)
				t.Setenv("LOG_FORMAT", format)
				read := entry.setup(t)

				Info("daemon connected", "daemon_id", "dmn_123", "attempt", 2)

				lines := nonEmptyLines(read())
				require.Len(t, lines, 1, "want exactly the one line logged")
				var record map[string]any
				require.NoError(t, json.Unmarshal([]byte(lines[0]), &record), "line is not JSON: %s", lines[0])
				assert.Equal(t, "daemon connected", record["msg"])
				assert.Equal(t, "INFO", record["level"])
				assert.Equal(t, "dmn_123", record["daemon_id"])
				assert.EqualValues(t, 2, record["attempt"])
			})
		}
	}
}

// Text stays the default so dev logs, and the greps written against them, are
// unchanged when LOG_FORMAT is unset. An unrecognised value also falls back to
// text, and says so rather than failing silently.
func TestLogFormatText(t *testing.T) {
	for _, format := range []string{"", "text", "TEXT", "logfmt"} {
		for _, entry := range setupEntryPoints {
			t.Run(entry.name+"/LOG_FORMAT="+format, func(t *testing.T) {
				isolateLogging(t)
				t.Setenv("LOG_FORMAT", format)
				read := entry.setup(t)

				Info("daemon connected", "daemon_id", "dmn_123")

				out := read()
				assert.Contains(t, out, `level=INFO msg="daemon connected" daemon_id=dmn_123`)
				for _, line := range nonEmptyLines(out) {
					assert.False(t, json.Valid([]byte(line)), "text output must not be JSON: %s", line)
				}

				warned := strings.Contains(out, "Unrecognised LOG_FORMAT")
				assert.Equal(t, format == "logfmt", warned, "only an unrecognised LOG_FORMAT should warn")
			})
		}
	}
}

// The JSON handler must sit under the same wrappers as the text one: the
// metrics bridge outermost, then the Sentry bridge. Losing either when the
// format changes would silently drop dead-end metrics or Sentry reports in
// exactly the environment that turns JSON on.
func TestLogFormatKeepsWrappers(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			isolateLogging(t)
			t.Setenv("LOG_FORMAT", format)
			DefaultOutput = io.Discard
			Setup(slog.LevelInfo)

			metrics, ok := slog.Default().Handler().(*metricsHandler)
			require.True(t, ok, "outermost handler is %T, want *metricsHandler", slog.Default().Handler())
			sentry, ok := metrics.inner.(*sentryHandler)
			require.True(t, ok, "metrics wraps %T, want *sentryHandler", metrics.inner)

			switch format {
			case "json":
				assert.IsType(t, &slog.JSONHandler{}, sentry.inner)
			default:
				assert.IsType(t, &slog.TextHandler{}, sentry.inner)
			}
		})
	}
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
