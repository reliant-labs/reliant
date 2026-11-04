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
