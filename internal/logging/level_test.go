// Copyright (c) 2025 Reliant Labs
package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// GetLogLevel must decide "is this prod" exactly as runenv does. Prod pods set
// neither RELIANT_ENV nor NODE_ENV, so the cases that matter most are the ones
// with both unset: before the fix the logger kept its own check for
// RELIANT_ENV=prod, which nothing sets, and a stray DEBUG=true turned DEBUG on
// in prod while telemetry, auth and analytics all knew they were in prod.
func TestGetLogLevel(t *testing.T) {
	cases := []struct {
		name            string
		reliantEnv      string
		reliantLogLevel string
		debug           string
		devDebug        string
		want            slog.Level
	}{
		// Prod, as it actually runs: no environment variable at all.
		{name: "prod pod with DEBUG=true stays at INFO", debug: "true", want: slog.LevelInfo},
		{name: "prod pod with RELIANT_DEV_DEBUG=true stays at INFO", devDebug: "true", want: slog.LevelInfo},
		{name: "prod pod with RELIANT_LOG_LEVEL=DEBUG stays at INFO", reliantLogLevel: "DEBUG", want: slog.LevelInfo},
		{name: "prod pod with RELIANT_LOG_LEVEL=debug stays at INFO", reliantLogLevel: "debug", want: slog.LevelInfo},
		{name: "prod pod with every DEBUG request stays at INFO", reliantLogLevel: "DEBUG", debug: "true", devDebug: "true", want: slog.LevelInfo},
		{name: "prod pod honours RELIANT_LOG_LEVEL=WARN", reliantLogLevel: "WARN", want: slog.LevelWarn},
		{name: "prod pod honours RELIANT_LOG_LEVEL=ERROR", reliantLogLevel: "ERROR", want: slog.LevelError},
		{name: "prod pod defaults to INFO", want: slog.LevelInfo},

		// Prod named explicitly, and a name runenv does not recognise.
		{name: "RELIANT_ENV=production with DEBUG=true stays at INFO", reliantEnv: "production", debug: "true", want: slog.LevelInfo},
		{name: "unrecognised RELIANT_ENV fails closed to prod", reliantEnv: "staging", debug: "true", want: slog.LevelInfo},

		// Every non-prod tier may log DEBUG, exactly as before.
		{name: "dev with DEBUG=true logs DEBUG", reliantEnv: "dev", debug: "true", want: slog.LevelDebug},
		{name: "dev with RELIANT_DEV_DEBUG=true logs DEBUG", reliantEnv: "dev", devDebug: "true", want: slog.LevelDebug},
		{name: "dev with RELIANT_LOG_LEVEL=DEBUG logs DEBUG", reliantEnv: "dev", reliantLogLevel: "DEBUG", want: slog.LevelDebug},
		{name: "e2e with DEBUG=true logs DEBUG", reliantEnv: "e2e", debug: "true", want: slog.LevelDebug},
		{name: "test with DEBUG=true logs DEBUG", reliantEnv: "test", debug: "true", want: slog.LevelDebug},
		{name: "dev defaults to INFO", reliantEnv: "dev", want: slog.LevelInfo},
		{name: "RELIANT_LOG_LEVEL wins over DEBUG=true in dev", reliantEnv: "dev", reliantLogLevel: "WARN", debug: "true", want: slog.LevelWarn},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RELIANT_ENV", tc.reliantEnv)
			t.Setenv("NODE_ENV", "")
			t.Setenv("RELIANT_LOG_LEVEL", tc.reliantLogLevel)
			t.Setenv("DEBUG", tc.debug)
			t.Setenv("RELIANT_DEV_DEBUG", tc.devDebug)

			if got := GetLogLevel(); got != tc.want {
				t.Errorf("GetLogLevel() = %v, want %v", got, tc.want)
			}
		})
	}
}

const refusedDebugMessage = "DEBUG logging requested but ignored: environment is prod"

// setupLikeAServer installs the logger the way serverapi, serverworker and
// servergateway do — Setup with GetLogLevel's answer — and returns what it
// wrote after one DEBUG and one INFO line.
func setupLikeAServer(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	DefaultOutput = &buf
	Setup(GetLogLevel())
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
		{"DEBUG", "true", "DEBUG=true"},
		{"RELIANT_DEV_DEBUG", "true", "RELIANT_DEV_DEBUG=true"},
		{"RELIANT_LOG_LEVEL", "debug", "RELIANT_LOG_LEVEL=debug"},
		{"RELIANT_LOG_LEVEL", "DEBUG", "RELIANT_LOG_LEVEL=DEBUG"},
	}
	for _, prodEnv := range []string{"", "prod"} {
		for _, req := range requests {
			t.Run("RELIANT_ENV="+prodEnv+"/"+req.name+"="+req.value, func(t *testing.T) {
				isolateLogging(t)
				t.Setenv("RELIANT_ENV", prodEnv)
				t.Setenv("NODE_ENV", "")
				t.Setenv(req.name, req.value)

				out := setupLikeAServer(t)

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
	t.Setenv("RELIANT_ENV", "")
	t.Setenv("NODE_ENV", "")

	var buf bytes.Buffer
	DefaultOutput = &buf
	Setup(slog.LevelDebug)
	Debug("debug-line-marker")

	out := buf.String()
	assert.NotContains(t, out, "debug-line-marker")
	assert.Equal(t, 1, strings.Count(out, refusedDebugMessage))
	assert.Contains(t, out, `requested_by="caller level DEBUG"`)
}

// Prod with nothing asking for DEBUG says nothing, and an explicit quieter
// RELIANT_LOG_LEVEL outranks a stray DEBUG flag, so that is not a request.
func TestProdWithoutDebugRequestDoesNotWarn(t *testing.T) {
	for _, tc := range []struct{ name, logLevel, debug string }{
		{name: "nothing set"},
		{name: "RELIANT_LOG_LEVEL=WARN outranks DEBUG=true", logLevel: "WARN", debug: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateLogging(t)
			t.Setenv("RELIANT_ENV", "")
			t.Setenv("NODE_ENV", "")
			t.Setenv("RELIANT_LOG_LEVEL", tc.logLevel)
			t.Setenv("DEBUG", tc.debug)

			out := setupLikeAServer(t)

			assert.NotContains(t, out, refusedDebugMessage)
			assert.NotContains(t, out, "debug-line-marker")
		})
	}
}

// Dev and every other non-prod tier are unchanged: DEBUG is honoured and
// nothing warns.
func TestNonProdHonoursDebug(t *testing.T) {
	for _, env := range []string{"dev", "e2e", "test"} {
		for _, flag := range []string{"DEBUG", "RELIANT_DEV_DEBUG"} {
			t.Run(env+"/"+flag, func(t *testing.T) {
				isolateLogging(t)
				t.Setenv("RELIANT_ENV", env)
				t.Setenv("NODE_ENV", "")
				t.Setenv(flag, "true")

				out := setupLikeAServer(t)

				assert.Contains(t, out, "debug-line-marker")
				assert.NotContains(t, out, refusedDebugMessage)
			})
		}
	}
}
