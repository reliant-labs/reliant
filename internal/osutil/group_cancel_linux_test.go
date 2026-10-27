//go:build linux

package osutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCancelKillsTERMResistantDescendantsAfterLeaderExits(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	effect := filepath.Join(dir, "effect")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Grandchild ignores TERM, redirects inherited pipes, and attempts a late
	// side effect. The leader dies on TERM; Wait alone cannot prove it stopped.
	script := fmt.Sprintf(`bash -c 'trap "" TERM; echo $$ > %s; sleep 0.6; echo bad > %s; sleep 30' >/dev/null 2>&1 & wait`, ready, effect)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stop := ApplyGracefulCancel(cmd, 40*time.Millisecond)
	defer stop()
	sibling := exec.Command("sleep", "30")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sibling.Process.Kill(); _ = sibling.Wait() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(ready)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child never ready")
	}
	cancel()
	_ = cmd.Wait()
	stop()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil || strings.Contains(string(b), ") Z") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil && !strings.Contains(string(b), ") Z") {
		t.Fatalf("descendant alive after cancel: %s", b)
	}
	if err := sibling.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated sibling killed: %v", err)
	}
	time.Sleep(650 * time.Millisecond)
	if _, err := os.Stat(effect); !os.IsNotExist(err) {
		t.Fatal("cancelled descendant produced a later side effect")
	}
}
