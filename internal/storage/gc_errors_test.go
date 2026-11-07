package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// unreadableLogsProject makes a registered project whose .forge/logs cannot
// be listed — the LaunchAgent's view of a project under ~/Documents without
// TCC access (EPERM) — and returns it.
func unreadableLogsProject(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions enforced for this user")
	}
	project := t.TempDir()
	logs := filepath.Join(project, ".forge", "logs")
	if err := os.MkdirAll(filepath.Join(logs, "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(logs, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(logs, 0o700) })
	return project
}

// projectWithExpiredLogs makes a project with nine rotated logs of one stream,
// all older than the retention window: four are expired by any Logs pass.
func projectWithExpiredLogs(t *testing.T) (project, dir string) {
	t.Helper()
	project = t.TempDir()
	dir = filepath.Join(project, ".forge", "logs", "dev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		path := filepath.Join(dir, fmt.Sprintf("worker.2026-01-%02dT00-00-00.log", i+1))
		if err := os.WriteFile(path, []byte("log"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-time.Duration(i+10) * 24 * time.Hour)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	return project, dir
}

// TestLogsContinuePastAnUnreadableProject is H2 inside the Logs layer: the
// first unreadable project ended the layer, so every project registered after
// it was never maintained.
func TestLogsContinuePastAnUnreadableProject(t *testing.T) {
	bad := unreadableLogsProject(t)
	good, dir := projectWithExpiredLogs(t)
	p := DefaultPolicy()
	p.Projects = []string{bad, good}
	err := (Runner{Policy: p}).Logs(true)
	if entries, _ := os.ReadDir(dir); len(entries) != 5 {
		t.Fatalf("the project after an unreadable one was not maintained: %d logs remain, want 5 (err: %v)", len(entries), err)
	}
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("the unreadable project must still be reported, by path: %v", err)
	}
}

// TestGCRunsEveryLayerPastABadProject is H2 across layers: an error in Logs
// returned from GC immediately, so the temp sweep, source eviction and every
// Docker layer never ran — the whole schedule disabled by one entry.
func TestGCRunsEveryLayerPastABadProject(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake lsof through shell subprocesses; runs in task test")
	}
	bad := unreadableLogsProject(t)
	tempRoot := t.TempDir()
	tempEntry := writeTempEntry(t, tempRoot, "go-link-h2", 48*time.Hour)
	fakeLsof(t, unrelatedOpenFile, "exit 0")

	p := DefaultPolicy()
	p.Projects = []string{bad}
	p.Builders = []string{"default"}
	var pruned bool
	r := Runner{
		Policy: p, TempRoot: tempRoot, SourceCacheRoot: t.TempDir(), PolicyPath: filepath.Join(t.TempDir(), "storage.json"),
		Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			joined := strings.Join(args, " ")
			switch {
			case joined == "context inspect":
				return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
			case joined == "buildx inspect default":
				return []byte(buildxInspectText("default", "docker-container", "unix:///var/run/docker.sock")), nil
			case strings.HasPrefix(joined, "buildx prune --builder default "):
				pruned = true
				return nil, nil
			}
			return nil, fmt.Errorf("unexpected %s %s", name, joined)
		},
	}
	err := r.GC(context.Background(), true)
	if _, statErr := os.Stat(tempEntry); !os.IsNotExist(statErr) {
		t.Errorf("the temp sweep did not run after a bad project (entry still present: %v)", statErr)
	}
	if !pruned {
		t.Error("the builder layer did not run after a bad project")
	}
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("the bad project must be in GC's combined error: %v", err)
	}
}

// TestNonDisruptiveGCRunsEveryLayerPastABadProject: the same, for the pass
// `forge env up` runs.
func TestNonDisruptiveGCRunsEveryLayerPastABadProject(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake lsof through shell subprocesses; runs in task test")
	}
	bad := unreadableLogsProject(t)
	tempRoot := t.TempDir()
	tempEntry := writeTempEntry(t, tempRoot, "go-link-h2", 48*time.Hour)
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	p := DefaultPolicy()
	p.Projects = []string{bad}
	p.Builders = nil
	r := Runner{
		Policy: p, TempRoot: tempRoot, SourceCacheRoot: t.TempDir(),
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if strings.Join(args, " ") == "context inspect" {
				return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
			}
			return nil, fmt.Errorf("unexpected docker %v", args)
		},
	}
	err := r.NonDisruptiveGC(context.Background(), true)
	if _, statErr := os.Stat(tempEntry); !os.IsNotExist(statErr) {
		t.Errorf("the temp sweep did not run after a bad project (entry still present: %v)", statErr)
	}
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("the bad project must be in the combined error: %v", err)
	}
}

// TestGCStillRefusesARemoteDockerAfterAHostLayerFailure: continuing past a
// host-layer error must not continue past the endpoint check — no Docker
// layer may run against a daemon that is not local.
func TestGCStillRefusesARemoteDockerAfterAHostLayerFailure(t *testing.T) {
	bad := unreadableLogsProject(t)
	p := DefaultPolicy()
	p.Projects = []string{bad}
	var calls []string
	r := Runner{
		Policy: p, TempRoot: t.TempDir(), SourceCacheRoot: t.TempDir(),
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			return []byte(`[{"Endpoints":{"docker":{"Host":"ssh://prod"}}}]`), nil
		},
	}
	err := r.GC(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "nonlocal") {
		t.Fatalf("remote Docker accepted: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("Docker commands ran after the endpoint was refused: %v", calls)
	}
}
