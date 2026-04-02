// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"

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

// Temp is a temporary logging function for debugging purposes.
// All calls to this function should be removed once the issue is resolved.
func Temp(msg string, args ...any) {
	slog.Warn(msg, args...)
}

// Message Logging for Debug
var MessageDir string

func GetSessionPrefix(sessionId string) string {
	return sessionId[:8]
}

// SetupWithTrace configures the logging system with optional trace logging
func SetupWithTrace(defaultLevel slog.Level, enableTrace bool) {
	TraceEnabled = enableTrace
	// Configure logging output
	// Configure logger with custom writer (in-memory)
	var handler slog.Handler = slog.NewTextHandler(DefaultOutput, &slog.HandlerOptions{
		Level: defaultLevel,
	})
	handler = newSentryHandler(handler)
	handler = newMetricsHandler(handler)
	slog.SetDefault(slog.New(handler))
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

	// Configure logger with the output, wrapped with Sentry bridge.
	// The Sentry handler lazily checks the global reporter, so it's safe to
	// create before Sentry is initialised — it will be a no-op until then.
	var handler slog.Handler = slog.NewTextHandler(output, &slog.HandlerOptions{
		Level: defaultLevel,
	})
	handler = newSentryHandler(handler)
	handler = newMetricsHandler(handler)
	slog.SetDefault(slog.New(handler))
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
