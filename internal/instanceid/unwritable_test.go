// Copyright (c) 2025 Reliant Labs
package instanceid

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"
)

// The hosted server containers run with HOME=/ on a read-only root, so
// ~/.reliant is /.reliant and cannot be created. A process-lifetime id is the
// correct reading there (see the package's Scope section); it is not a fault
// and must not warn on every boot.

func TestStateDirUnwritable_ReadOnlyFilesystemAndPermission(t *testing.T) {
	readOnly := fmt.Errorf("creating reliant state dir: %w",
		&fs.PathError{Op: "mkdir", Path: "/.reliant", Err: syscall.EROFS})
	if !stateDirUnwritable(readOnly) {
		t.Fatalf("a read-only root is the deployment, not a fault: %v", readOnly)
	}
	denied := fmt.Errorf("creating reliant state dir: %w",
		&fs.PathError{Op: "mkdir", Path: "/home/x/.reliant", Err: syscall.EACCES})
	if !stateDirUnwritable(denied) {
		t.Fatalf("permission denied is the deployment, not a fault: %v", denied)
	}
	if stateDirUnwritable(fmt.Errorf("publishing instance record: %w",
		&fs.PathError{Op: "rename", Path: "/x", Err: syscall.EIO})) {
		t.Fatal("an I/O error is a fault and must still warn")
	}
}

func TestResolve_UnwritableHomeDoesNotWarn(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	if f, err := os.CreateTemp(home, "probe"); err == nil {
		_ = f.Close()
		t.Skip("running with privileges that ignore directory permissions; cannot make HOME unwritable")
	}
	t.Setenv(EnvOverride, "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	id := resolve()
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("resolve() = %q, want a process-lifetime UUID: %v", id, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".reliant")); err == nil {
		t.Fatal("nothing should have been created under an unwritable HOME")
	}

	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["level"] == "WARN" || rec["level"] == "ERROR" {
			t.Fatalf("an unwritable state dir logged at %v: %s", rec["level"], line)
		}
	}
}
