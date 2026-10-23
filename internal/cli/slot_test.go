package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// slotEnv points the slot commands at a private slot directory with n slots.
// TestMain switches the limit off for every other test; these turn it on.
func slotEnv(t *testing.T, n string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(hostslot.EnvDir, dir)
	t.Setenv(hostslot.SlotsEnv(hostslot.Heavy), n)
	t.Setenv(hostslot.EnvHeld, "")
	return dir
}

func TestSlotRun_PassesTheSlotAndTheExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	slotEnv(t, "1")
	var out, errb bytes.Buffer
	err := runInSlot(context.Background(), hostslot.Heavy, 0,
		[]string{"sh", "-c", `printf %s "$FORGE_SLOT_HELD"; exit 3`}, nil, &out, &errb)
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != 3 {
		t.Fatalf("runInSlot = %v, want the command's exit status 3", err)
	}
	if !strings.HasPrefix(out.String(), "heavy:0:") {
		t.Fatalf("the command saw %s=%q, want its slot so its own children take none", hostslot.EnvHeld, out.String())
	}
}

func TestSlotRun_CommandNotFoundIs127(t *testing.T) {
	slotEnv(t, "1")
	err := runInSlot(context.Background(), hostslot.Heavy, 0, []string{"forge-no-such-command-xyz"}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != 127 {
		t.Fatalf("runInSlot = %v, want exit 127 for a missing command", err)
	}
}

func TestSlotRun_WaitsForTheHolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	slotEnv(t, "1")
	held, err := hostslot.Acquire(context.Background(), hostslot.Heavy, hostslot.Options{Label: "the other job", Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var errb bytes.Buffer
	err = runInSlot(ctx, hostslot.Heavy, 0, []string{"sh", "-c", "exit 0"}, nil, &bytes.Buffer{}, &errb)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runInSlot with the only slot held = %v, want it to wait until the context ended", err)
	}
	if !strings.Contains(errb.String(), "the other job") {
		t.Fatalf("the waiting line does not name the holder:\n%s", errb.String())
	}
	_ = held.Release()
	if err := runInSlot(context.Background(), hostslot.Heavy, 0, []string{"sh", "-c", "exit 0"}, nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("after the holder released: %v", err)
	}
}

func TestSlotStatus_Renders(t *testing.T) {
	slotEnv(t, "2")
	held, err := hostslot.Acquire(context.Background(), hostslot.Heavy, hostslot.Options{Label: "go test ./...", Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()
	rep, err := hostslot.Status(hostslot.Heavy, hostslot.Options{Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writeSlotStatus(&out, rep)
	for _, want := range []string{"heavy slots: 2 (FORGE_HEAVY_SLOTS=2)", "held (1 of 2)", "go test ./...", "waiting (0)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := writeSlotStatusJSON(&out, rep); err != nil || !strings.Contains(out.String(), `"command": "go test ./..."`) {
		t.Fatalf("json status: %v\n%s", err, out.String())
	}
}
