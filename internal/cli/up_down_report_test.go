package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestEnvDownProcessInspectionFailurePreservesRecords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const projectID = "inspection-denied"
	ledger, err := upStatePath(projectID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ledger), 0700); err != nil {
		t.Fatal(err)
	}
	state, err := upEnvStatePath(projectID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ledger, state} {
		if err := os.WriteFile(p, []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := stopStackWithFacts(projectID, "dev", &osProcFacts{})
	if res.stopped != 0 || err == nil || !strings.Contains(err.Error(), "cannot inspect") {
		t.Fatalf("failed inspection must refuse teardown: stopped=%d err=%v", res.stopped, err)
	}
	for _, p := range []string{ledger, state} {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != "preserve" {
			t.Fatalf("failed inspection lost stack record %s: %q, %v", p, b, err)
		}
	}
}

func TestEnvDownReportsHostInfraFailure(t *testing.T) {
	cause := errors.New("cannot render declared infrastructure")
	var err error
	out := captureStdout(t, func() {
		err = reportUpStop("dev", "/project", stackStop{}, 0, cause)
	})
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "shutdown incomplete") {
		t.Fatalf("host infrastructure error was lost: %v", err)
	}
	if strings.Contains(out, "no forge processes running") {
		t.Fatalf("unverified shutdown reported an empty environment: %s", out)
	}
}

func TestEnvDownEmptyHostStackExplainsDockerScope(t *testing.T) {
	var err error
	out := captureStdout(t, func() {
		err = reportUpStop("dev", "/project", stackStop{}, 0, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[down]", "no owned Forge host processes", "owned Compose containers", "Kubernetes workloads/clusters", "Docker Desktop were not stopped"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty host stack output lacks %q: %s", want, out)
		}
	}
}

func TestEnvDownSignalFailurePreservesRecords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const project = "signal-denied"
	ledger, err := upStatePath(project, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ledger), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger, []byte("api\t42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := completeStackStop(project, "dev", nil, stackStopPlan{roots: []int{42}}, func(pids []int) error {
		return killTreesAndWaitWith(pids, func(int, syscall.Signal) error { return syscall.EPERM }, func(int) bool { return true })
	})
	if res.stopped != 0 || !errors.Is(err, syscall.EPERM) {
		t.Fatalf("signal failure hidden: %d, %v", res.stopped, err)
	}
	if _, err := os.Stat(ledger); err != nil {
		t.Fatalf("failed signal discarded retry record: %v", err)
	}
}

// TestEnvDownStopsInfraWhenAHostStopFails: a host process that could not be
// proven stopped must not leave the IdP and databases running — they held
// their ports against the next `forge env up`. Everything is stopped, then
// both failures are reported, and the presence row is not marked stopped.
func TestEnvDownStopsInfraWhenAHostStopFails(t *testing.T) {
	hostErr := errors.New("signal terminated to pid/group -75074: operation not permitted")
	infraErr := errors.New("zitadel would not stop")
	infraRan, reported := false, false
	var err error
	out := captureStdout(t, func() {
		err = stopEnv("dev", "/project", envDownSteps{
			hosts:         func() (stackStop, error) { return stackStop{planned: true, unproven: 5}, hostErr },
			reportStopped: func() { reported = true },
			infra:         func() (int, error) { infraRan = true; return 1, infraErr },
		})
	})
	if !infraRan {
		t.Fatal("host infrastructure was not stopped after a host-process stop failed")
	}
	if reported {
		t.Error("the presence row was marked stopped although a host process was not proven stopped")
	}
	if !errors.Is(err, hostErr) || !errors.Is(err, infraErr) {
		t.Fatalf("a failure was lost: %v", err)
	}
	if !strings.Contains(out, "could not prove them stopped") || strings.Contains(out, "no owned Forge host processes") {
		t.Errorf("report misdescribes the unproven stop:\n%s", out)
	}
}

// TestEnvDownLeavesInfraWhenTheStackScopeIsUnknown: without a process table
// the teardown cannot tell whether the stack hosts this command, so its
// infrastructure (the database under this very session, perhaps) stays up.
// Likewise for a stack that does host it.
func TestEnvDownLeavesInfraWhenTheStackScopeIsUnknown(t *testing.T) {
	for name, hosts := range map[string]func() (stackStop, error){
		"no process table": func() (stackStop, error) { return stackStop{}, errors.New("cannot inspect the host process table") },
		"hosts this command": func() (stackStop, error) {
			return stackStop{planned: true, skipped: []ancestorSkip{{pid: 42, command: "forge"}}}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			infraRan := false
			captureStdout(t, func() {
				_ = stopEnv("dev", "/project", envDownSteps{
					hosts:         hosts,
					reportStopped: func() {},
					infra:         func() (int, error) { infraRan = true; return 0, nil },
				})
			})
			if infraRan {
				t.Error("host infrastructure was stopped")
			}
		})
	}
}
