// Copyright (c) 2025 Reliant Labs
package commands

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/logging"
)

func TestSetupToolsDaemonLoggingPersistsOutput(t *testing.T) {
	dataDir := t.TempDir()

	setupToolsDaemonLogging(dataDir)
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
