// Copyright (c) 2025 Reliant Labs
//go:build !windows

package daemonruntime

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/hostslot"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/heavyjob"
)

// exec.run queues heavy commands behind the gate the runtime sets, exactly as
// the LocalClient path does (internal/daemon/local_exec_heavy_test.go).
func TestHandleExecRun_HeavyCommandQueuesForTheSlot(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	slots := t.TempDir()
	g, err := heavyjob.New(heavyjob.Config{
		Env:  []string{"FORGE_SLOTS_DIR=" + slots, "FORGE_HEAVY_SLOTS=1"},
		Self: []string{exe, heavyjob.WrapperArg},
	})
	if err != nil {
		t.Fatal(err)
	}
	SetHeavyGate(g)
	t.Cleanup(func() { SetHeavyGate(nil) })
	t.Setenv(hostslot.EnvHeld, "")

	// Another holder — a forge lane, say — has the only slot.
	held, err := hostslot.Acquire(context.Background(), hostslot.Heavy, hostslot.Options{Dir: slots, Slots: 1, Label: "forge generate"})
	if err != nil {
		t.Fatal(err)
	}
	res := runExec(t, daemon.RunCommandRequest{Command: "echo RAN-$((1+1))", Heavy: true, TimeoutMs: 1500})
	if !res.TimedOut || strings.Contains(res.Stdout, "RAN-2") || !strings.Contains(res.Stderr, "forge generate") {
		t.Fatalf("want a queued command naming its holder to time out unstarted, got %+v", res)
	}

	_ = held.Release()
	res = runExec(t, daemon.RunCommandRequest{Command: "echo RAN-$((1+1))", Heavy: true, TimeoutMs: 30_000})
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "RAN-2") || heavyjob.WaitedFor(res.Stderr) {
		t.Fatalf("with the slot free the command must run at once, got %+v", res)
	}
}
