// Copyright (c) 2025 Reliant Labs
package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/runenv"

	"github.com/stretchr/testify/assert"
)

// GetLogLevel must decide "is this prod" exactly as runenv does. Prod pods set
// neither RELIANT_ENV nor NODE_ENV, so the cases that matter most are the ones
// with both unset: before the fix the logger kept its own check for
// RELIANT_ENV=prod, which nothing sets, and a stray DEBUG request turned DEBUG
// on in prod while telemetry, auth and analytics all knew they were in prod.
func TestGetLogLevel(t *testing.T) {
	cases := []struct {
		name            string
		reliantEnv      string
		reliantLogLevel string
		want            slog.Level
	}{
		// Prod, as it actually runs: no environment variable at all.
		{name: "prod pod with RELIANT_LOG_LEVEL=DEBUG stays at INFO", reliantLogLevel: "DEBUG", want: slog.LevelInfo},
		{name: "prod pod with RELIANT_LOG_LEVEL=debug stays at INFO", reliantLogLevel: "debug", want: slog.LevelInfo},
		{name: "prod pod honours RELIANT_LOG_LEVEL=WARN", reliantLogLevel: "WARN", want: slog.LevelWarn},
		{name: "prod pod honours RELIANT_LOG_LEVEL=ERROR", reliantLogLevel: "ERROR", want: slog.LevelError},
		{name: "prod pod defaults to INFO", want: slog.LevelInfo},

		// Prod named explicitly, and a name runenv does not recognise.
		{name: "RELIANT_ENV=production with RELIANT_LOG_LEVEL=debug stays at INFO", reliantEnv: "production", reliantLogLevel: "debug", want: slog.LevelInfo},
		{name: "unrecognised RELIANT_ENV fails closed to prod", reliantEnv: "staging", reliantLogLevel: "debug", want: slog.LevelInfo},

		// Every non-prod tier may log DEBUG.
		{name: "dev with RELIANT_LOG_LEVEL=DEBUG logs DEBUG", reliantEnv: "dev", reliantLogLevel: "DEBUG", want: slog.LevelDebug},
		{name: "e2e with RELIANT_LOG_LEVEL=debug logs DEBUG", reliantEnv: "e2e", reliantLogLevel: "debug", want: slog.LevelDebug},
		{name: "test with RELIANT_LOG_LEVEL=debug logs DEBUG", reliantEnv: "test", reliantLogLevel: "debug", want: slog.LevelDebug},
		{name: "dev defaults to INFO", reliantEnv: "dev", want: slog.LevelInfo},
		{name: "dev honours RELIANT_LOG_LEVEL=WARN", reliantEnv: "dev", reliantLogLevel: "WARN", want: slog.LevelWarn},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runenv.ConfigureForTest(t, tc.reliantEnv)

			if got := GetLogLevel(Settings{Level: tc.reliantLogLevel}); got != tc.want {
				t.Errorf("GetLogLevel() = %v, want %v", got, tc.want)
			}
		})
	}
}

// RELIANT_LOG_LEVEL is the one way to ask for DEBUG. DEBUG=true belongs to
// other tools as often as to us, and RELIANT_DEV_DEBUG is retired: neither
// moves the level, even in dev.
func TestRetiredDebugFlagsDoNotRaiseTheLevel(t *testing.T) {
	runenv.ConfigureForTest(t, "dev")
	for _, name := range []string{"DEBUG", "RELIANT_DEV_DEBUG"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "true")
			assert.Equal(t, slog.LevelInfo, GetLogLevel(Settings{}))
			assert.Empty(t, debugRequest(Settings{}))
		})
	}
}

const refusedDebugMessage = "DEBUG logging requested but ignored: environment is prod"

// setupLikeAServer installs the logger the way serverapi, serverworker and
// servergateway do — Setup with GetLogLevel's answer for the same settings —
// and returns what it wrote after one DEBUG and one INFO line.
func setupLikeAServer(t *testing.T, s Settings) string {
	t.Helper()
	var buf bytes.Buffer
	DefaultOutput = &buf
	Setup(s, GetLogLevel(s))
	Debug("debug-line-marker")
	Info("info-line-marker")
	return buf.String()
}

// In prod a DEBUG request is refused with no override, and the refusal is
// said exactly once, naming the variable that asked, so whoever set it learns
// why it had no effect instead of concluding the logger is broken.
func TestProdRefusesDebugAndWarnsOnce(t *testing.T) {
	requests := []struct {
		name, value string
		wantNamed   string
	}{
		{"RELIANT_LOG_LEVEL", "debug", "RELIANT_LOG_LEVEL=debug"},
		{"RELIANT_LOG_LEVEL", "DEBUG", "RELIANT_LOG_LEVEL=DEBUG"},
	}
	for _, prodEnv := range []string{"", "prod"} {
		for _, req := range requests {
			t.Run("RELIANT_ENV="+prodEnv+"/"+req.name+"="+req.value, func(t *testing.T) {
				isolateLogging(t)
				runenv.ConfigureForTest(t, prodEnv)
				out := setupLikeAServer(t, Settings{Level: req.value})

				assert.NotContains(t, out, "debug-line-marker", "prod must not emit DEBUG lines")
				assert.Contains(t, out, "info-line-marker")
				assert.Equal(t, 1, strings.Count(out, refusedDebugMessage), "want exactly one refusal WARN:\n%s", out)
				assert.Contains(t, out, "level=WARN")
				assert.Contains(t, out, `requested_by="`+req.wantNamed+`"`)
				assert.Contains(t, out, "effective_level=INFO")
			})
		}
	}
}

// A caller handing install a DEBUG level directly is capped too: no Setup
// entry point is a way around the prod floor.
func TestProdCapsDebugPassedByCaller(t *testing.T) {
	isolateLogging(t)
	runenv.ConfigureForTest(t, "")

	var buf bytes.Buffer
	DefaultOutput = &buf
	Setup(Settings{}, slog.LevelDebug)
	Debug("debug-line-marker")

	out := buf.String()
	assert.NotContains(t, out, "debug-line-marker")
	assert.Equal(t, 1, strings.Count(out, refusedDebugMessage))
	assert.Contains(t, out, `requested_by="caller level DEBUG"`)
}

// Prod with nothing asking for DEBUG says nothing, and a quieter
// RELIANT_LOG_LEVEL is not a request.
func TestProdWithoutDebugRequestDoesNotWarn(t *testing.T) {
	for _, tc := range []struct{ name, logLevel string }{
		{name: "nothing set"},
		{name: "RELIANT_LOG_LEVEL=WARN", logLevel: "WARN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateLogging(t)
			runenv.ConfigureForTest(t, "")

			out := setupLikeAServer(t, Settings{Level: tc.logLevel})

			assert.NotContains(t, out, refusedDebugMessage)
			assert.NotContains(t, out, "debug-line-marker")
		})
	}
}

// Dev and every other non-prod tier honour RELIANT_LOG_LEVEL=debug and
// nothing warns.
func TestNonProdHonoursDebug(t *testing.T) {
	for _, env := range []string{"dev", "e2e", "test"} {
		t.Run(env, func(t *testing.T) {
			isolateLogging(t)
			runenv.ConfigureForTest(t, env)

			out := setupLikeAServer(t, Settings{Level: "debug"})

			assert.Contains(t, out, "debug-line-marker")
			assert.NotContains(t, out, refusedDebugMessage)
		})
	}
}
