// Copyright (c) 2025 Reliant Labs
//
// Process-global instrumentation. The exported vars are the collectors and
// registry the process registers once at init and updates from everywhere; a
// getter returns the same pointer and hides nothing. Behind an interface these
// would still be the single global sink they are today.
//
//forge:exclude-contract: slog setup, handlers and rotation; process-global logging by design
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/runenv"
)

var (
	logOutput      io.Writer = os.Stdout
	logOutputMutex sync.RWMutex
)

// GetOutput returns the current log output writer
func GetOutput() io.Writer {
	logOutputMutex.RLock()
	defer logOutputMutex.RUnlock()
	return logOutput
}

// SetOutput sets the log output writer (thread-safe)
func SetOutput(w io.Writer) {
	logOutputMutex.Lock()
	defer logOutputMutex.Unlock()
	logOutput = w
}

// GetLogLevel returns the current log level from environment or default.
//
// The servers never log DEBUG in prod, and nothing overrides that:
// RELIANT_LOG_LEVEL=debug, DEBUG=true and RELIANT_DEV_DEBUG=true are all held at
// INFO there, because DEBUG lines are where request bodies and tool payloads
// end up. The tools daemon is the one exception (see DaemonLogLevel).
// RELIANT_LOG_LEVEL=INFO/WARN/ERROR is honoured in every environment, since it
// can only make prod quieter. The refusal is reported once, as a WARN, when
// the logger is installed (see install) — this function runs before that
// logger exists, so it has nowhere to say it.
//
// "Prod" is runenv's resolution, the one telemetry and auth use. It fails
// closed, so a pod with neither RELIANT_ENV nor NODE_ENV set — which is how
// prod runs — is prod. This used to test RELIANT_ENV for "prod"/"production"
// itself; prod never sets it, so the guard never fired where it mattered.
func GetLogLevel() slog.Level {
	return capLevel(requestedLevel())
}

// requestedLevel is the level the environment variables ask for, before any
// environment cap. RELIANT_LOG_LEVEL (debug|info|warn|error, any case) wins;
// otherwise DEBUG=true or RELIANT_DEV_DEBUG=true selects DEBUG; otherwise INFO.
func requestedLevel() slog.Level {
	if level, ok := explicitLevel(); ok {
		return level
	}
	if os.Getenv("DEBUG") == "true" || os.Getenv("RELIANT_DEV_DEBUG") == "true" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// logLevelEnv is the one variable that names a log level. The daemon reads it
// too, so a pod and a developer's shell spell the request the same way.
const logLevelEnv = "RELIANT_LOG_LEVEL"

// parseEnvLevel parses a level name, case-insensitively. ok is false for a
// name that is not one of the four, so a typo is reported rather than quietly
// becoming INFO.
func parseEnvLevel(raw string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return slog.LevelInfo, false
}

// explicitLevel is RELIANT_LOG_LEVEL when it is set to a valid name.
func explicitLevel() (slog.Level, bool) {
	return parseEnvLevel(os.Getenv(logLevelEnv))
}

// unrecognisedLogLevel returns RELIANT_LOG_LEVEL when it is set but names no
// level, so the logger can say so once it exists.
func unrecognisedLogLevel() string {
	raw := os.Getenv(logLevelEnv)
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	if _, ok := parseEnvLevel(raw); ok {
		return ""
	}
	return raw
}

// capLevel holds prod at INFO or quieter. Every other environment gets the
// level it asked for.
func capLevel(level slog.Level) slog.Level {
	if level < slog.LevelInfo && runenv.IsProd() {
		return slog.LevelInfo
	}
	return level
}

// debugRequest names the setting asking for DEBUG ("DEBUG=true"), or "" when
// none is. It follows requestedLevel's precedence: an explicit
// RELIANT_LOG_LEVEL outranks the DEBUG flags, so with a quieter explicit level
// they are not a request at all. A request prod refuses must be reported
// however it was spelled.
func debugRequest() string {
	if level, ok := explicitLevel(); ok {
		if level < slog.LevelInfo {
			return logLevelEnv + "=" + os.Getenv(logLevelEnv)
		}
		return ""
	}
	for _, flag := range []string{"DEBUG", "RELIANT_DEV_DEBUG"} {
		if os.Getenv(flag) == "true" {
			return flag + "=true"
		}
	}
	return ""
}

// ParseLogLevel parses a string log level to slog.Level
func ParseLogLevel(level string) slog.Level {
	switch level {
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "info", "INFO":
		return slog.LevelInfo
	case "warn", "WARN", "warning", "WARNING":
		return slog.LevelWarn
	case "error", "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
