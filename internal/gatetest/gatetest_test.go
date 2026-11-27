// Copyright (c) 2025 Reliant Labs
package gatetest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
)

func TestTheContractMatchesGaterun(t *testing.T) {
	if envDockerSkips != gaterun.EnvDockerSkips || skipMessage != gaterun.DockerSkipMessage {
		t.Fatalf("gatetest (%q, %q) and gaterun (%q, %q) disagree", envDockerSkips, skipMessage, gaterun.EnvDockerSkips, gaterun.DockerSkipMessage)
	}
}

// skipped runs DockerUnavailable in a subtest and returns whether it
// skipped, and with what message.
func skipped(t *testing.T) (bool, string) {
	t.Helper()
	var ok bool
	var msg string
	t.Run("container", func(t *testing.T) {
		rec := &recorder{TB: t}
		defer func() { ok, msg = t.Skipped(), rec.msg }()
		DockerUnavailable(rec, "no docker on this host")
	})
	return ok, msg
}

// recorder captures the skip message on its way to the real test.
type recorder struct {
	testing.TB
	msg string
}

func (r *recorder) Skip(args ...any) {
	r.msg = fmt.Sprint(args...)
	r.TB.Skip(args...)
}

func TestOutsideAGateItIsAPlainSkip(t *testing.T) {
	t.Setenv(envGate, "")
	log := filepath.Join(t.TempDir(), "skips")
	t.Setenv(envDockerSkips, log)
	ok, msg := skipped(t)
	if !ok || msg != "no docker on this host" {
		t.Fatalf("skipped %v with %q", ok, msg)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("a skip outside a gate was recorded")
	}
}

func TestInAGateTheSkipIsMarkedAndRecorded(t *testing.T) {
	t.Setenv(envGate, "1")
	log := filepath.Join(t.TempDir(), "skips")
	t.Setenv(envDockerSkips, log)
	for range 2 {
		ok, msg := skipped(t)
		if !ok || msg != "skip: docker unavailable (gate): no docker on this host" {
			t.Fatalf("skipped %v with %q", ok, msg)
		}
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "TestInAGateTheSkipIsMarkedAndRecorded/container"); got != 2 {
		t.Fatalf("skip log %q, want two lines naming the test", b)
	}
}
