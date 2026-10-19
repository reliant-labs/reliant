// Copyright (c) 2025 Reliant Labs
package commands

import (
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// The budget comes from the env vars control-plane already sets and forge
// already derives terminationGracePeriodSeconds from. Reading them is the
// whole point: a hardcoded 20s cannot track a grace period it does not know.
func TestDrainBudgetReadFromEnvironment(t *testing.T) {
	t.Setenv("PRE_STOP_DELAY", "7s")
	t.Setenv("SHUTDOWN_TIMEOUT", "45s")

	preStop, shutdown := drainBudgetFromEnv()
	if preStop != 7*time.Second {
		t.Errorf("PreStopDelay = %s, want 7s", preStop)
	}
	if shutdown != 45*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 45s", shutdown)
	}
}

// Unset is serverkit's default budget, which is what forge's grace-period
// arithmetic assumes for a workload that sets neither variable.
func TestDrainBudgetDefaultsToServerkits(t *testing.T) {
	t.Setenv("PRE_STOP_DELAY", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")

	requireServerkitDefaults(t)
}

// A malformed duration must not stop the process from booting, and must not
// produce a zero-length budget (which would make shutdown instantaneous).
func TestMalformedDrainBudgetFallsBackToDefault(t *testing.T) {
	t.Setenv("PRE_STOP_DELAY", "not-a-duration")
	t.Setenv("SHUTDOWN_TIMEOUT", "${SHUTDOWN_TIMEOUT}")

	requireServerkitDefaults(t)
}

// An explicit zero is the default, as it is everywhere a drain duration is
// read: forge sizes the pod's grace period for 5s / 30s when it sees "0s",
// so the server must spend the same.
func TestZeroDrainBudgetIsTheDefault(t *testing.T) {
	t.Setenv("PRE_STOP_DELAY", "0s")
	t.Setenv("SHUTDOWN_TIMEOUT", "0")

	requireServerkitDefaults(t)
}

// Negative is none: no pre-stop pause, and a shutdown window that is over as
// it opens — which is what forge's grace period then leaves room for.
func TestNegativeDrainBudgetIsNone(t *testing.T) {
	t.Setenv("PRE_STOP_DELAY", "-1s")
	t.Setenv("SHUTDOWN_TIMEOUT", "-1s")

	preStop, shutdown := drainBudgetFromEnv()
	d := &serverkit.Drainer{PreStopDelay: preStop, ShutdownTimeout: shutdown}
	start := time.Now()
	d.Begin()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Begin paused %s for PRE_STOP_DELAY=-1s", elapsed)
	}
	ctx, cancel := d.ShutdownContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 0 {
		t.Errorf("SHUTDOWN_TIMEOUT=-1s left a shutdown window open")
	}
}

// A bare number is seconds, as forge's grace-period arithmetic reads it.
func TestBareDrainBudgetIsSeconds(t *testing.T) {
	t.Setenv("PRE_STOP_DELAY", "7")
	t.Setenv("SHUTDOWN_TIMEOUT", "45")

	preStop, shutdown := drainBudgetFromEnv()
	if preStop != 7*time.Second || shutdown != 45*time.Second {
		t.Errorf("budget = %s / %s, want 7s / 45s", preStop, shutdown)
	}
}

// requireServerkitDefaults asserts the env yields serverkit's default budget:
// zero values, which a Drainer turns into 5s / 30s.
func requireServerkitDefaults(t *testing.T) {
	t.Helper()
	preStop, shutdown := drainBudgetFromEnv()
	if preStop != 0 || shutdown != 0 {
		t.Fatalf("budget = %s / %s, want zero (serverkit's defaults)", preStop, shutdown)
	}
	d := &serverkit.Drainer{PreStopDelay: preStop, ShutdownTimeout: shutdown}
	ctx, cancel := d.ShutdownContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the shutdown context has no deadline")
	}
	if left := time.Until(deadline); left < 29*time.Second || left > 30*time.Second {
		t.Errorf("shutdown budget = %s, want serverkit's 30s default", left)
	}
}
