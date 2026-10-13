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

// Settings is how the process logger was configured: the RELIANT_LOG_LEVEL
// and LOG_FORMAT values exactly as the operator spelled them, "" when unset.
// The entrypoint fills it from its loaded config (pkg/config's LogConfig);
// this package never reads those variables itself. Parsing stays here, so a
// value that names nothing is warned about once the logger exists rather than
// refusing to start.
type Settings struct {
	// Level is RELIANT_LOG_LEVEL: debug, info, warn or error, in any case.
	Level string
	// Format is LOG_FORMAT: text or json, in any case.
	Format string
}

// GetLogLevel returns the level s asks for, or the default.
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
func GetLogLevel(s Settings) slog.Level {
	return capLevel(requestedLevel(s))
}

// requestedLevel is the level the settings ask for, before any environment
// cap. RELIANT_LOG_LEVEL (debug|info|warn|error, any case) wins; otherwise
// DEBUG=true or RELIANT_DEV_DEBUG=true selects DEBUG; otherwise INFO.
//
// The DEBUG / RELIANT_DEV_DEBUG reads are a deliberate shim, not config still
// waiting to move: they are not alternative names for RELIANT_LOG_LEVEL (an
// alias would hand the level "true"), they are bools that MAP onto one of its
// values, only for the exact string "true". DEBUG is also a name other tools
// own (DEBUG=express:* is common in a shell), so it must not be parsed as a
// config bool that refuses to start on anything else.
func requestedLevel(s Settings) slog.Level {
	if level, ok := explicitLevel(s); ok {
		return level
	}
	if os.Getenv("DEBUG") == "true" || os.Getenv("RELIANT_DEV_DEBUG") == "true" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// logLevelEnv is the one variable that names a log level (Settings.Level).
// The daemon reads it too, so a pod and a developer's shell spell the request
// the same way.
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
func explicitLevel(s Settings) (slog.Level, bool) {
	return parseEnvLevel(s.Level)
}

// unrecognisedLogLevel returns RELIANT_LOG_LEVEL when it is set but names no
// level, so the logger can say so once it exists.
func unrecognisedLogLevel(s Settings) string {
	raw := s.Level
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
func debugRequest(s Settings) string {
	if level, ok := explicitLevel(s); ok {
		if level < slog.LevelInfo {
			return logLevelEnv + "=" + s.Level
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
