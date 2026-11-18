//go:build windows

package cli

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestReadProcEnvironSelfHasPath(t *testing.T) {
	env, ok := readProcEnviron(os.Getpid())
	if !ok {
		t.Fatal("readProcEnviron(self) unreadable")
	}
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			return
		}
	}
	t.Fatalf("PATH not found in %d entries", len(env))
}

func TestReadProcEnvironChildMarker(t *testing.T) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	want := "FORGE_TEST_MARKER=" + hex.EncodeToString(b)
	cmd := exec.Command("cmd", "/c", "ping -n 30 127.0.0.1")
	cmd.Env = append(os.Environ(), want)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if env, ok := readProcEnviron(cmd.Process.Pid); ok {
			for _, kv := range env {
				if kv == want {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child env never showed %s", want)
}

func TestReadProcEnvironInvalidPid(t *testing.T) {
	for _, pid := range []int{0, -1, 0x7ffffff0} {
		if _, ok := readProcEnviron(pid); ok {
			t.Fatalf("pid %d should be unreadable", pid)
		}
	}
}
