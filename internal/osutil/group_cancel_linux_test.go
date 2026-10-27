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

func TestUnavailableLeaderCannotYieldSuccessfulProof(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := captureGroupKiller(cmd.Process)(); err == nil {
		t.Fatal("missing pidfd/leader treated as tree exit")
	}
}

func TestForkDuringCancellationCannotProduceSuccessfulTreeProof(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	forked := filepath.Join(dir, "forked")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The TERM handler forks only AFTER captureGroupKiller's snapshot. The
	// late child deliberately exits itself: the test does not signal a numeric
	// process group after its leader was reaped.
	script := fmt.Sprintf(`trap '(echo late > %s; sleep 0.08) >/dev/null 2>&1 & exit 0' TERM; echo ready > %s; while :; do sleep 0.01; done`, forked, ready)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	finish := ApplyGracefulCancelChecked(cmd, 150*time.Millisecond)
	defer finish()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			_ = cmd.Wait()
			t.Fatal("not ready")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	_ = cmd.Wait()
	if err := finish(); err == nil {
		t.Fatal("fork after snapshot was falsely acknowledged")
	}
	if _, err := os.Stat(forked); err != nil {
		t.Fatal("fixture did not fork during cancellation")
	}
}

func TestLeaderExitPreservesDescendantCleanupGrace(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	clean := filepath.Join(dir, "clean")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	script := fmt.Sprintf(`bash -c 'trap "sleep 0.08; echo cleaned > %s; exit 0" TERM; echo ready > %s; while :; do sleep 0.01; done' >/dev/null 2>&1 & wait`, clean, ready)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Leave scheduling headroom for the explicit 80ms cleanup subprocess on
	// a loaded integration host. A leader-triggered immediate escalation still
	// fails this assertion; the grace policy itself is unchanged.
	finish := ApplyGracefulCancelChecked(cmd, time.Second)
	defer finish()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			_ = cmd.Wait()
			t.Fatal("not ready")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	_ = cmd.Wait()
	err := finish()
	if err == nil {
		t.Fatal("snapshot falsely confirmed all descendants")
	}
	data, readErr := os.ReadFile(clean)
	if readErr != nil || strings.TrimSpace(string(data)) != "cleaned" {
		t.Fatalf("leader exit shortened cleanup grace: %s %v", data, readErr)
	}
}

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
	stop := ApplyGracefulCancelChecked(cmd, 40*time.Millisecond)
	defer stop()
	sibling := exec.Command("sleep", "30")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sibling.Process.Kill(); _ = sibling.Wait() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
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
	if err := stop(); err == nil {
		t.Fatal("snapshot must not claim complete tree proof after leader exit")
	}
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
