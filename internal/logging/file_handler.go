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
// Prod never logs DEBUG, and nothing overrides that: RELIANT_LOG_LEVEL=debug,
// DEBUG=true and RELIANT_DEV_DEBUG=true are all held at INFO there, because
// DEBUG lines are where request bodies and tool payloads end up.
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
// environment cap. Precedence and spellings are the ones dev has always had:
// one of RELIANT_LOG_LEVEL's four upper-case values wins; otherwise
// DEBUG=true or RELIANT_DEV_DEBUG=true selects DEBUG; otherwise INFO.
func requestedLevel() slog.Level {
	switch os.Getenv("RELIANT_LOG_LEVEL") {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	}
	if os.Getenv("DEBUG") == "true" || os.Getenv("RELIANT_DEV_DEBUG") == "true" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
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
// none is. It follows requestedLevel's precedence — an explicit
// RELIANT_LOG_LEVEL=INFO/WARN/ERROR outranks the DEBUG flags, so they are not
// a request at all — except that any capitalisation of RELIANT_LOG_LEVEL=debug
// counts. A request prod refuses must be reported however it was spelled.
func debugRequest() string {
	switch level := os.Getenv("RELIANT_LOG_LEVEL"); {
	case strings.EqualFold(strings.TrimSpace(level), "debug"):
		return "RELIANT_LOG_LEVEL=" + level
	case level == "INFO" || level == "WARN" || level == "ERROR":
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
