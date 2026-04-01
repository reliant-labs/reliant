// Copyright (c) 2025 Reliant Labs
package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupWithRotation(t *testing.T) {
	// Create temp directory for test logs
	tempDir, err := os.MkdirTemp("", "logging-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logFile := filepath.Join(tempDir, "test.log")

	// Setup logging with rotation
	SetupWithRotation(slog.LevelInfo, false, &RotationConfig{
		Filename:   logFile,
		MaxSizeMB:  1,
		MaxBackups: 2,
		MaxAgeDays: 1,
		Compress:   false,
	})

	// Write some logs
	Info("Test message 1", "key", "value")
	Info("Test message 2", "number", 42)
	Error("Test error", "error", "something went wrong")

	// Close the logger
	if err := Close(); err != nil {
		t.Fatalf("Failed to close logger: %v", err)
	}

	// Verify log file was created
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Errorf("Log file was not created: %s", logFile)
	}

	// Read log file and verify content
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	logContent := string(content)
	if !strings.Contains(logContent, "Test message 1") {
		t.Errorf("Log file missing 'Test message 1'")
	}
	if !strings.Contains(logContent, "Test message 2") {
		t.Errorf("Log file missing 'Test message 2'")
	}
	if !strings.Contains(logContent, "Test error") {
		t.Errorf("Log file missing 'Test error'")
	}
}

func TestSetupWithRotation_NilConfig(t *testing.T) {
	// Setup with nil config should not panic and use stdout
	SetupWithRotation(slog.LevelInfo, false, nil)

	// Should be able to log without error
	Info("Test with nil config")
}

func TestSetupWithRotation_EmptyFilename(t *testing.T) {
	// Setup with empty filename should use stdout
	SetupWithRotation(slog.LevelInfo, false, &RotationConfig{
		Filename: "",
	})

	// Should be able to log without error
	Info("Test with empty filename")
}

func TestSetupWithRotation_DefaultValues(t *testing.T) {
	// Create temp directory for test logs
	tempDir, err := os.MkdirTemp("", "logging-test-defaults")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logFile := filepath.Join(tempDir, "defaults.log")

	// Setup with zero values - should apply defaults
	SetupWithRotation(slog.LevelDebug, true, &RotationConfig{
		Filename:   logFile,
		MaxSizeMB:  0, // Should default to 50
		MaxBackups: 0, // Should default to 3
		MaxAgeDays: 0, // Should default to 30
		Compress:   false,
	})
	defer Close()

	// Verify trace is enabled
	if !TraceEnabled {
		t.Errorf("TraceEnabled should be true")
	}

	// Write a trace log - this ensures activeLogger is used
	Trace("Trace message")
	// Also write a regular log to ensure the file is written
	Info("Info message to trigger file write")

	// Verify file exists
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Errorf("Log file was not created: %s", logFile)
	}
}

func TestManualRotate(t *testing.T) {
	// Create temp directory for test logs
	tempDir, err := os.MkdirTemp("", "logging-test-rotate")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logFile := filepath.Join(tempDir, "rotate.log")

	// Setup logging
	SetupWithRotation(slog.LevelInfo, false, &RotationConfig{
		Filename:   logFile,
		MaxSizeMB:  100,
		MaxBackups: 3,
		MaxAgeDays: 7,
		Compress:   false,
	})

	// Write some logs
	Info("Before rotation")

	// Manually trigger rotation
	if err := Rotate(); err != nil {
		t.Fatalf("Failed to rotate: %v", err)
	}

	// Write more logs
	Info("After rotation")

	// Close
	Close()

	// List files in directory
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("Failed to read temp dir: %v", err)
	}

	// Should have at least 2 files (current + rotated)
	if len(entries) < 2 {
		t.Errorf("Expected at least 2 log files after rotation, got %d", len(entries))
	}
}

func TestClose_NoActiveLogger(t *testing.T) {
	// Reset activeLogger
	activeLogger = nil

	// Close should not error when no logger is active
	if err := Close(); err != nil {
		t.Errorf("Close() with no active logger should not error: %v", err)
	}
}

func TestRotate_NoActiveLogger(t *testing.T) {
	// Reset activeLogger
	activeLogger = nil

	// Rotate should not error when no logger is active
	if err := Rotate(); err != nil {
		t.Errorf("Rotate() with no active logger should not error: %v", err)
	}
}
