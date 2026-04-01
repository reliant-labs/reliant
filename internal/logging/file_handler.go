// Copyright (c) 2025 Reliant Labs
package logging

import (
	"io"
	"log/slog"
	"os"
	"sync"
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

// GetLogLevel returns the current log level from environment or default
func GetLogLevel() slog.Level {
	// Check if we're in production - never allow debug in prod
	env := os.Getenv("RELIANT_ENV")
	isProd := env == "production" || env == "prod"

	level := os.Getenv("RELIANT_LOG_LEVEL")
	switch level {
	case "DEBUG":
		if isProd {
			return slog.LevelInfo // Downgrade debug to info in production
		}
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		// In production, default to INFO regardless of DEBUG flags
		if isProd {
			return slog.LevelInfo
		}
		if os.Getenv("DEBUG") == "true" || os.Getenv("RELIANT_DEV_DEBUG") == "true" {
			return slog.LevelDebug
		}
		return slog.LevelInfo
	}
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
