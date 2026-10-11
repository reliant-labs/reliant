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

// levelVar is the level every installed handler filters on. It is a LevelVar
// rather than a literal so a later runtime override (a TTL-bound level change
// pushed to one daemon) can move it without rebuilding the handler chain.
var levelVar slog.LevelVar

// CurrentLevel is the level the process logger is filtering at.
func CurrentLevel() slog.Level { return levelVar.Level() }

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

// Log writes a record at a level chosen at the call site, for callers whose
// level depends on the cause. Not for hot paths: a fixed level is cheaper.
func Log(level slog.Level, msg string, args ...any) {
	slog.Log(context.Background(), level, msg, args...)
}

// logFormatEnv selects the line format: "json" writes one JSON object per line,
// which is what a log pipeline indexes; "text" writes key=value, which is what
// a person greps. It is the variable forge's config block already sets, with
// the same two values.
const logFormatEnv = "LOG_FORMAT"

// resolveLogFormat reads LOG_FORMAT. Text is the default unless the caller
// passes defaultJSON, so dev logs — and every grep written against them — only
// change when a deploy asks for JSON.
// recognised is false for a value that is set but is neither, so the caller can
// say so once a logger exists to say it with.
func resolveLogFormat(defaultJSON bool) (asJSON, recognised bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(logFormatEnv))) {
	case "":
		return defaultJSON, true
	case "text":
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
func install(output io.Writer, level slog.Level, options ...installOption) {
	var cfg installConfig
	for _, apply := range options {
		apply(&cfg)
	}

	requested := level
	if !cfg.allowProdDebug {
		level = capLevel(level)
	}
	levelVar.Set(level)

	opts := &slog.HandlerOptions{Level: &levelVar}
	if cfg.redact {
		opts.ReplaceAttr = newRedactor().replaceAttr
	}
	asJSON, recognised := resolveLogFormat(cfg.defaultJSON)

	var handler slog.Handler
	if asJSON {
		handler = slog.NewJSONHandler(output, opts)
	} else {
		handler = slog.NewTextHandler(output, opts)
	}
	handler = withReporting(handler)
	if cfg.fields != nil {
		handler = newIdentityHandler(handler, cfg.fields)
		handler = observe.NewTraceHandler(handler)
	}
	slog.SetDefault(slog.New(handler))

	if !recognised {
		slog.Warn("Unrecognised LOG_FORMAT, logging as text", "log_format", os.Getenv(logFormatEnv), "allowed", "json,text")
	}
	if bad := unrecognisedLogLevel(); bad != "" {
		slog.Warn("Unrecognised RELIANT_LOG_LEVEL, ignoring it", "value", bad, "allowed", "debug,info,warn,error")
	}
	if !cfg.allowProdDebug {
		warnRefusedDebug(requested, level)
	}
}

// installConfig is what only some callers ask install for. Every field's zero
// value is what the API server, worker and gateway have always had, so a new
// option can never change them by being added.
type installConfig struct {
	// defaultJSON makes JSON the format when LOG_FORMAT is unset. An explicit
	// LOG_FORMAT=text still wins.
	defaultJSON bool
	// allowProdDebug lifts the prod INFO floor. Only the daemon uses it: its
	// logs are read by us, the redactor is in the chain, and the other
	// processes keep the floor because their DEBUG lines carry request bodies.
	allowProdDebug bool
	// redact masks secrets and caps attribute and message sizes.
	redact bool
	// fields, when set, stamps the daemon's identity on every record and adds
	// trace_id/span_id for records logged with a span in their context.
	fields *DaemonFields
}

type installOption func(*installConfig)

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

// newRotator builds the rotating file writer, applying the defaults for zero
// values in config.
func newRotator(config *RotationConfig) *lumberjack.Logger {
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
	return &lumberjack.Logger{
		Filename:   config.Filename,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		MaxAge:     maxAge,
		Compress:   config.Compress,
		LocalTime:  true, // Use local time for backup file names
	}
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
			activeLogger = newRotator(config)

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

	activeLogger = newRotator(config)

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
