// Copyright (c) 2025 Reliant Labs

// Package gatetest is how a test that needs a docker engine says it cannot
// get one.
//
// Outside a merge queue gate, DockerUnavailable is a plain skip. Inside one
// (RELIANT_GATE=1), the skip says "skip: docker unavailable (gate)", and
// the test is recorded in the stage's skip log (RELIANT_GATE_DOCKER_SKIPS),
// so the gate's verdict counts the container tests it did not run
// (gaterun.StageResult.SkippedDockerTests). A test that skips this way is
// reported as a skip, never as a pass.
//
// Gates have no docker engine. runc gates have no nested engine, and the
// GitHub merge queue on cicd has none either, because its runner users are
// not in the docker group. A dedicated gate machine with sysbox, where each
// gate gets a nested engine of its own, closes this gap.
//
// Test-only by intent: only _test.go files import it.
//
//forge:exclude-contract: test-only helper imported by _test.go files; one function over testing.TB, nothing to mock
package gatetest

import (
	"fmt"
	"os"
	"testing"
)

// These equal gaterun's EnvDockerSkips and DockerSkipMessage. They are
// copied rather than imported so that a test binary does not link the merge
// queue; TestTheContractMatchesGaterun holds them equal.
const (
	envGate        = "RELIANT_GATE"
	envDockerSkips = "RELIANT_GATE_DOCKER_SKIPS"
	skipMessage    = "skip: docker unavailable (gate)"
)

// DockerUnavailable skips t, which needs a docker engine it cannot get. The
// format and args say why.
func DockerUnavailable(t testing.TB, format string, args ...any) {
	t.Helper()
	why := fmt.Sprintf(format, args...)
	if os.Getenv(envGate) != "1" { //nolint:forbidigo // the gate's environment (gaterun.GateEnv), not this program's configuration
		t.Skip(why)
	}
	if log := os.Getenv(envDockerSkips); log != "" { //nolint:forbidigo // set per stage by the gate's stage script, not configuration
		// One short O_APPEND write per skip: concurrent test processes
		// cannot interleave inside a line.
		if f, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644); err == nil {
			_, _ = fmt.Fprintln(f, t.Name())
			_ = f.Close()
		} else {
			t.Logf("record the skip in %s: %v", log, err)
		}
	}
	t.Skip(skipMessage + ": " + why)
}
