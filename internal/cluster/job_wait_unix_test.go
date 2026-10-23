//go:build unix

package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestWaitJobCompleteTimeout_ReapsTheLosingWatcher: the wait races two
// `kubectl wait` watchers (condition=complete, condition=failed) and returns
// the first verdict. The other watcher must be GONE when it returns — killed
// and reaped — not merely signalled.
//
// Returning while the loser was still alive is what made
// TestApply_FailedPreRolloutJobAppliesNoWorkload flake on a loaded machine
// ("TempDir RemoveAll cleanup: ... directory not empty"): the straggling fake
// kubectl appended to its call log inside the test's TempDir after the
// cleanup had already started removing it. In production it is the same
// defect with a smaller blast radius: a kubectl child that outlives the
// function that started it.
//
// Mutation that fails it: return the verdict without waiting for the other
// watcher (the pre-fix code).
func TestWaitJobCompleteTimeout_ReapsTheLosingWatcher(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a racing pair of fake kubectl watchers through shell subprocesses; runs in task test")
	}
	requirePOSIXFake(t, "kubectl")
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	// Each watcher records its pid. The `failed` watcher answers only once
	// BOTH have started, so the loser is provably running when the verdict
	// arrives; the `complete` watcher never answers on its own.
	script := `#!/bin/sh
echo $$ >> ` + pids + `
case " $* " in
  *'--for=condition=failed'*)
    i=0
    while [ "$(wc -l < ` + pids + `)" -lt 2 ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done
    exit 0 ;;
esac
exec sleep 30
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := WaitJobCompleteTimeout(context.Background(), "", "app-migrate-abc123", "app-prod", 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("a Job that reported condition=failed must read as failed, got: %v", err)
	}

	raw, rerr := os.ReadFile(pids)
	if rerr != nil {
		t.Fatalf("read pids: %v", rerr)
	}
	started := strings.Fields(string(raw))
	if len(started) != 2 {
		t.Fatalf("want both watchers started, got pids %v", started)
	}
	for _, s := range started {
		pid, _ := strconv.Atoi(s)
		// Signal 0 probes without signalling. It succeeds for a live process
		// AND for an exited-but-unreaped one, so it fails this test either
		// way the loser was left behind.
		if perr := syscall.Kill(pid, 0); !errors.Is(perr, syscall.ESRCH) {
			t.Errorf("kubectl %d was still running (or unreaped) after WaitJobCompleteTimeout returned (kill -0: %v)", pid, perr)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
