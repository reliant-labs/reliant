package cmdutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// nestedAcquireEnv, set in a re-exec'd test binary, makes it play the child:
// a forge process `forge generate` started (buf's protoc-gen-forge plugin,
// whose pin hand-off builds forge in a heavy slot).
const nestedAcquireEnv = "FORGE_TEST_NESTED_SLOT_ACQUIRE"

// TestNestedAcquireHelper is not a test. Re-exec'd with nestedAcquireEnv set,
// it acquires a heavy slot from its inherited environment, as
// forgedelegate's build does, and exits 0 if Acquire returned at once inside
// its parent's slot, 10 if it took a slot of its own, 11 if it was still
// waiting at the deadline (the deadlock).
func TestNestedAcquireHelper(t *testing.T) {
	if os.Getenv(nestedAcquireEnv) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := hostslot.Acquire(ctx, hostslot.Heavy, hostslot.Options{Env: os.Environ(), Poll: 5 * time.Millisecond})
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		os.Exit(11)
	case err != nil:
		os.Exit(3)
	case s.Reentrant() == "":
		os.Exit(10)
	}
	os.Exit(0)
}

// forge#948: on a 4-CPU runner the heavy pool is 2 slots, 1 of them reserved
// for express, so `forge generate` held the only bulk slot while the forge
// plugin buf ran for it queued for a slot of its own to build forge — and
// waited for its own parent for 6 hours. A child of a HoldHeavySlot holder
// must run inside the holder's slot, so it returns at once.
func TestHoldHeavySlot_ChildrenRunInsideTheSlot(t *testing.T) {
	t.Setenv(hostslot.EnvDir, t.TempDir())
	t.Setenv(hostslot.EnvHeld, "")
	t.Setenv(hostslot.SlotsEnv(hostslot.Heavy), "2") // 1 bulk + 1 express, as on the runner
	t.Setenv(hostslot.EnvLane, "")

	release, err := HoldHeavySlot(context.Background(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !HoldsOwnHeavySlot() {
		release()
		t.Fatal("HoldHeavySlot took no slot of its own; the pool cannot prove anything")
	}
	if os.Getenv(hostslot.EnvHeld) == "" {
		t.Errorf("%s is not in the environment while the slot is held: a child would queue for its own", hostslot.EnvHeld)
	}
	defer release()

	// The child inherits the environment the way exec.Cmd does with a nil
	// Env (buf), and the plugin it runs passes it on.
	cmd := exec.Command(os.Args[0], "-test.run=^TestNestedAcquireHelper$")
	cmd.Env = append(os.Environ(), nestedAcquireEnv+"=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit) && exit.ExitCode() == 11:
		t.Errorf("the child waited for the slot its parent holds: the nested acquire deadlocks\n%s", out.String())
	case errors.As(err, &exit) && exit.ExitCode() == 10:
		t.Errorf("the child took a slot of its own instead of running inside its parent's\n%s", out.String())
	default:
		t.Errorf("child: %v\n%s", err, out.String())
	}

	release()
	if got := os.Getenv(hostslot.EnvHeld); got != "" {
		t.Errorf("after release %s = %q, want the value from before the slot was taken (empty)", hostslot.EnvHeld, got)
	}
}

// A process inside its parent's slot takes nothing, and leaves the token it
// inherited exactly as it found it.
func TestHoldHeavySlot_ReentrantLeavesInheritedTokenAlone(t *testing.T) {
	t.Setenv(hostslot.EnvDir, t.TempDir())
	t.Setenv(hostslot.EnvHeld, "")
	t.Setenv(hostslot.SlotsEnv(hostslot.Heavy), "2")

	outer, err := HoldHeavySlot(context.Background(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer outer()
	token := os.Getenv(hostslot.EnvHeld)

	// Bounded: before the fix the inner hold waited for the outer's slot.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inner, err := HoldHeavySlot(ctx, io.Discard)
	if err != nil {
		t.Fatalf("a hold nested in this process's own slot waited for it: %v", err)
	}
	inner()
	if got := os.Getenv(hostslot.EnvHeld); got != token {
		t.Errorf("a re-entrant hold changed %s from %q to %q", hostslot.EnvHeld, token, got)
	}
}
