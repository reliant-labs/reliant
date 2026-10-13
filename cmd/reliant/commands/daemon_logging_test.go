// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runenv"
)

// A quiet foreground daemon must still write EVERYTHING to the log file.
//
// Non-verbose only changes who stdout is for: a person gets short status lines
// instead of the structured stream. If the file lost lines too, `reliant daemon
// logs` and every post-mortem would silently degrade, and the quiet default
// would be a loss of evidence rather than a UX improvement.
func TestSetupToolsDaemonLoggingPersistsWhenQuiet(t *testing.T) {
	dataDir := t.TempDir()

	setupToolsDaemonLogging(dataDir, false, logging.Settings{})
	logging.Info("quiet-mode persistence test", "source", "file-only")
	if err := logging.Close(); err != nil {
		t.Fatalf("close tools-daemon log: %v", err)
	}
	logging.Setup(logging.Settings{}, slog.LevelInfo)

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

	setupToolsDaemonLogging(dataDir, true, logging.Settings{})
	logging.Info("tools-daemon persistence test", "source", "stdout-and-file")
	if err := logging.Close(); err != nil {
		t.Fatalf("close tools-daemon log: %v", err)
	}
	// Restore the package-global logger for subsequent tests in this process.
	logging.Setup(logging.Settings{}, slog.LevelInfo)

	logPath := toolsDaemonLogPath(dataDir)
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read tools-daemon log %s: %v", logPath, err)
	}
	if !strings.Contains(string(contents), "tools-daemon persistence test") {
		t.Fatalf("tools-daemon log did not contain emitted message: %q", contents)
	}
}

// A hosted daemon (RELIANT_DAEMON_TYPE=managed) is read through its stdout by
// the node log agent, so it must emit JSON there as well as to the file, with
// no --verbose, and must not leave its logging variables in the environment
// its children inherit.
func TestSetupToolsDaemonLoggingManagedWritesJSONToStdout(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("RELIANT_DAEMON_TYPE", "managed")
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("RELIANT_LOG_LEVEL", "info")
	t.Cleanup(func() { logging.Setup(logging.Settings{}, slog.LevelInfo) })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	setupToolsDaemonLogging(dataDir, false, loadedLogSettings(t))
	logging.Info("managed stdout test")
	os.Stdout = prev
	_ = w.Close()
	if err := logging.Close(); err != nil {
		t.Fatalf("close tools-daemon log: %v", err)
	}
	stdout, _ := io.ReadAll(r)

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &rec); err != nil {
		t.Fatalf("managed stdout is not one JSON record: %q: %v", stdout, err)
	}
	if rec["msg"] != "managed stdout test" || rec["service"] != "reliant-daemon" {
		t.Fatalf("record is missing required fields: %v", rec)
	}
	if _, ok := rec["workspace_id"]; ok {
		t.Fatalf("a daemon line must not claim a workspace: %v", rec)
	}
	onDisk, err := os.ReadFile(toolsDaemonLogPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, stdout) {
		t.Fatalf("file and stdout diverged:\nfile:   %q\nstdout: %q", onDisk, stdout)
	}
	if os.Getenv("LOG_FORMAT") != "" {
		t.Fatal("LOG_FORMAT must not leak into the daemon's children")
	}
}

// loadedLogSettings is what `reliant daemon start` hands setupToolsDaemonLogging:
// the root config's log settings, loaded from this process's environment.
func loadedLogSettings(t *testing.T) logging.Settings {
	t.Helper()
	runenv.ConfigureForTest(t, "") // loadRootConfig sets the tier; undo it after t
	cmd, _, err := NewRootCmd().Find([]string{"daemon", "start"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadRootConfig(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return logSettings(cfg)
}
