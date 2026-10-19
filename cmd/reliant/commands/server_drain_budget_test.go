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
// produce a zero budget (which would make shutdown instantaneous).
func TestMalformedDrainBudgetFallsBackToDefault(t *testing.T) {
	t.Setenv("PRE_STOP_DELAY", "not-a-duration")
	t.Setenv("SHUTDOWN_TIMEOUT", "-1s")

	requireServerkitDefaults(t)
}

// An explicit zero pre-stop delay is no pause at all, not the default one.
func TestZeroPreStopDelayMeansNoPause(t *testing.T) {
	t.Setenv("PRE_STOP_DELAY", "0s")
	t.Setenv("SHUTDOWN_TIMEOUT", "")

	preStop, shutdown := drainBudgetFromEnv()
	d := &serverkit.Drainer{PreStopDelay: preStop, ShutdownTimeout: shutdown}
	start := time.Now()
	d.Begin()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Begin paused %s for PRE_STOP_DELAY=0s", elapsed)
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
