// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/runenv"
	"gopkg.in/natefinch/lumberjack.v2"
)

var DefaultOutput io.Writer = os.Stdout

// TraceEnabled controls whether trace logs are emitted
var TraceEnabled bool = false

// activeLogger holds a reference to the lumberjack logger for cleanup
var activeLogger *lumberjack.Logger

// RotationConfig holds log rotation settings
type RotationConfig struct {
	Filename   string // Path to log file
	MaxSizeMB  int    // Max size in MB before rotation
	MaxBackups int    // Max number of old log files to keep
	MaxAgeDays int    // Max days to retain old log files
	Compress   bool   // Whether to compress rotated files
}

func Info(msg string, args ...any) {
	slog.Info(msg, args...)
}

func Debug(msg string, args ...any) {
	// slog.Debug(msg, args...)
	// source := getCaller()
	slog.Debug(msg, args...)
}

func Trace(msg string, args ...any) {
	// Only emit trace logs if explicitly enabled
	if !TraceEnabled {
		return
	}
	// Use a level lower than debug for trace logs
	traceLevel := slog.LevelDebug - 4
	slog.Log(context.Background(), traceLevel, msg, args...)
}

func Warn(msg string, args ...any) {
	slog.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	slog.Error(msg, args...)
}

// logFormatEnv selects the line format: "json" writes one JSON object per line,
// which is what a log pipeline indexes; "text" writes key=value, which is what
// a person greps. It is the variable forge's config block already sets, with
// the same two values.
const logFormatEnv = "LOG_FORMAT"

// resolveLogFormat reads LOG_FORMAT. Text is the default, so dev logs — and
// every grep written against them — only change when a deploy asks for JSON.
// recognised is false for a value that is set but is neither, so the caller can
// say so once a logger exists to say it with.
func resolveLogFormat() (asJSON, recognised bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(logFormatEnv))) {
	case "", "text":
		return false, true
	case "json":
		return true, true
	default:
		return false, false
	}
}

// install is the one place the process logger is built. Every Setup* variant
// differs only in where lines are written, so they all come here: the format
// is chosen once, and the error-class policy, the Sentry and the metrics
// bridges wrap it the same way whichever format it is (withReporting).
//
// It is also where prod's INFO floor is enforced, so no caller can build a
// DEBUG logger in prod: GetLogLevel already caps what it returns, and install
// caps whatever it is handed as well. When either cap refused a DEBUG request
// it logs one WARN saying so, through the handler it just built.
//
// The Sentry handler lazily checks the global reporter, so it's safe to create
// before Sentry is initialised — it will be a no-op until then.
func install(output io.Writer, level slog.Level) {
	requested := level
	level = capLevel(level)

	opts := &slog.HandlerOptions{Level: level}
	asJSON, recognised := resolveLogFormat()

	var handler slog.Handler
	if asJSON {
		handler = slog.NewJSONHandler(output, opts)
	} else {
		handler = slog.NewTextHandler(output, opts)
	}
	slog.SetDefault(slog.New(withReporting(handler)))

	if !recognised {
		slog.Warn("Unrecognised LOG_FORMAT, logging as text", "log_format", os.Getenv(logFormatEnv), "allowed", "json,text")
	}
	warnRefusedDebug(requested, level)
}

// withReporting wraps the line-writing handler with everything that routes on a
// record's level: the Sentry bridge (ERROR and above) and the dead-end error
// counter. The error-class policy goes OUTSIDE both, so they see the level the
// policy decided rather than the one the call site wrote: a logging.Error
// carrying a user error — the user's machine is offline, their provider
// subscription is spent — is written at INFO with error_class=user, counted as
// a user error, and never reaches Sentry. See internal/errclass.
func withReporting(lines slog.Handler) slog.Handler {
	var handler slog.Handler = newSentryHandler(lines)
	handler = newMetricsHandler(handler)
	return observe.NewErrorClassHandler(handler, observe.WithErrorClassifier(errclass.Library))
}

// warnRefusedDebug reports, once per logger install, that prod refused a DEBUG
// request. An environment variable is named when one asked; otherwise the
// request was a caller passing a DEBUG level to a Setup function.
func warnRefusedDebug(requested, effective slog.Level) {
	if !runenv.IsProd() {
		return
	}
	source := debugRequest()
	if source == "" && requested >= slog.LevelInfo {
		return
	}
	if source == "" {
		source = "caller level " + requested.String()
	}
	slog.Warn("DEBUG logging requested but ignored: environment is prod",
		"requested_by", source,
		"environment", string(runenv.Prod),
		"effective_level", effective.String(),
	)
}

// SetupWithTrace configures the logging system with optional trace logging
func SetupWithTrace(defaultLevel slog.Level, enableTrace bool) {
	TraceEnabled = enableTrace
	install(DefaultOutput, defaultLevel)
}

// Setup configures the logging system (trace disabled by default)
func Setup(defaultLevel slog.Level) {
	SetupWithTrace(defaultLevel, false)
}

// SetupWithRotation configures the logging system with file rotation
// If config is nil or Filename is empty, logs go to stdout only
func SetupWithRotation(defaultLevel slog.Level, enableTrace bool, config *RotationConfig) {
	TraceEnabled = enableTrace

	var output io.Writer = os.Stdout

	// If we have a valid rotation config with a filename, set up lumberjack
	if config != nil && config.Filename != "" {
		// Ensure the log directory exists
		logDir := filepath.Dir(config.Filename)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			// Fall back to stdout if we can't create the directory
			slog.Warn("Failed to create log directory, using stdout", "dir", logDir, "error", err)
		} else {
			// Apply defaults for zero values
			maxSize := config.MaxSizeMB
			if maxSize <= 0 {
				maxSize = 50 // 50MB default
			}
			maxBackups := config.MaxBackups
			if maxBackups <= 0 {
				maxBackups = 3
			}
			maxAge := config.MaxAgeDays
			if maxAge <= 0 {
				maxAge = 30
			}

			// Create lumberjack logger
			activeLogger = &lumberjack.Logger{
				Filename:   config.Filename,
				MaxSize:    maxSize,
				MaxBackups: maxBackups,
				MaxAge:     maxAge,
				Compress:   config.Compress,
				LocalTime:  true, // Use local time for backup file names
			}

			// Write to both file and stdout for visibility
			output = io.MultiWriter(os.Stdout, activeLogger)
		}
	}

	// Update the default output
	DefaultOutput = output

	install(output, defaultLevel)
}

// SetupFileOnly configures logging to write to the rotating file ONLY, leaving
// stdout free for a process to print human-readable output of its own.
//
// This is SetupWithRotation minus the os.Stdout leg of the MultiWriter. The
// tools daemon uses it for a non-verbose foreground run: a person watching that
// terminal wants "connected" or an error, not the structured stream, while the
// file must keep every line so `reliant daemon logs` and post-mortems are
// unaffected.
//
// Falls back to the normal stdout logger when no file can be opened. Silently
// discarding logs because a directory was unwritable would turn a broken log
// path into a daemon that appears to run with no output at all.
func SetupFileOnly(defaultLevel slog.Level, config *RotationConfig) {
	if config == nil || config.Filename == "" {
		Setup(defaultLevel)
		return
	}

	logDir := filepath.Dir(config.Filename)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		slog.Warn("Failed to create log directory, logging to stdout", "dir", logDir, "error", err)
		Setup(defaultLevel)
		return
	}

	maxSize := config.MaxSizeMB
	if maxSize <= 0 {
		maxSize = 50
	}
	maxBackups := config.MaxBackups
	if maxBackups <= 0 {
		maxBackups = 3
	}
	maxAge := config.MaxAgeDays
	if maxAge <= 0 {
		maxAge = 30
	}

	activeLogger = &lumberjack.Logger{
		Filename:   config.Filename,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		MaxAge:     maxAge,
		Compress:   config.Compress,
		LocalTime:  true,
	}

	DefaultOutput = activeLogger

	install(activeLogger, defaultLevel)
}

// Close closes the active log file (should be called on shutdown)
func Close() error {
	if activeLogger != nil {
		return activeLogger.Close()
	}
	return nil
}

// Rotate manually triggers a log rotation
func Rotate() error {
	if activeLogger != nil {
		return activeLogger.Rotate()
	}
	return nil
}
