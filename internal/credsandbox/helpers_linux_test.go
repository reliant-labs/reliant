// Copyright (c) 2025 Reliant Labs
//go:build linux

package credsandbox

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func syscallMkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }

func waitFor(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
