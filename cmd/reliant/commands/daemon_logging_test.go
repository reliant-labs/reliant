// Copyright (c) 2025 Reliant Labs
package commands

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/logging"
)

// A quiet foreground daemon must still write EVERYTHING to the log file.
//
// Non-verbose only changes who stdout is for: a person gets short status lines
// instead of the structured stream. If the file lost lines too, `reliant daemon
// logs` and every post-mortem would silently degrade, and the quiet default
// would be a loss of evidence rather than a UX improvement.
func TestSetupToolsDaemonLoggingPersistsWhenQuiet(t *testing.T) {
	dataDir := t.TempDir()

	setupToolsDaemonLogging(dataDir, false)
	logging.Info("quiet-mode persistence test", "source", "file-only")
	if err := logging.Close(); err != nil {
		t.Fatalf("close tools-daemon log: %v", err)
	}
	logging.Setup(slog.LevelInfo)

	contents, err := os.ReadFile(toolsDaemonLogPath(dataDir))
	if err != nil {
		t.Fatalf("read tools-daemon log: %v", err)
	}
	if !strings.Contains(string(contents), "quiet-mode persistence test") {
		t.Fatalf("quiet mode dropped a line from the log file: %q", contents)
	}
}

func TestSetupToolsDaemonLoggingPersistsOutput(t *testing.T) {
	dataDir := t.TempDir()

	setupToolsDaemonLogging(dataDir, true)
	logging.Info("tools-daemon persistence test", "source", "stdout-and-file")
	if err := logging.Close(); err != nil {
		t.Fatalf("close tools-daemon log: %v", err)
	}
	// Restore the package-global logger for subsequent tests in this process.
	logging.Setup(slog.LevelInfo)

	logPath := toolsDaemonLogPath(dataDir)
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read tools-daemon log %s: %v", logPath, err)
	}
	if !strings.Contains(string(contents), "tools-daemon persistence test") {
		t.Fatalf("tools-daemon log did not contain emitted message: %q", contents)
	}
}
