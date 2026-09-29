// Copyright (c) 2025 Reliant Labs
//go:build !windows

package shell

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Listing background processes must cost a FIXED number of OS scans, not one
// per process and not one per PID in each process's tree.
//
// This is the whole cost of exec.bg_list. The old code descended each tree with
// a `pgrep -P` per node and then ran `lsof -p` once per resulting PID, so
// listing a handful of dev servers with deep node trees meant dozens of forks —
// which is how the daemon once logged `exec.bg_list completed slowly
// elapsed=15.7s`. The parsing was never the problem; the fan-out was.
//
// The test counts real subprocess invocations by putting counting shims for
// `ps` and `lsof` ahead of the real ones on PATH, so it measures what the
// process actually spends rather than an internal call count that could drift
// from it.
func TestGetProcessPortsForRoots_ScansTheOSAFixedNumberOfTimes(t *testing.T) {
	counts := installPortScanCounters(t)

	// Three separate trees, each of which the old code would have walked and
	// scanned independently.
	roots := []int{os.Getpid(), os.Getppid(), 1}

	if _, err := getProcessPortsForRoots(roots); err != nil {
		t.Fatalf("getProcessPortsForRoots: %v", err)
	}

	if got := counts.read(t, "ps"); got != 1 {
		t.Errorf("ps invocations = %d, want exactly 1 process-table snapshot for all %d trees", got, len(roots))
	}
	if got := counts.read(t, "lsof"); got != 1 {
		t.Errorf("lsof invocations = %d, want exactly 1 port scan for all %d trees", got, len(roots))
	}
}

// GetAllProcesses is the call exec.bg_list makes. Its port refresh must be
// batched across every process it returns.
func TestGetAllProcesses_RefreshesPortsWithOneBatchedScan(t *testing.T) {
	m := newTestBGManager()

	// Real running processes, so refreshPortsBatch sees non-zero pids and does
	// the OS work rather than short-circuiting.
	for i := 0; i < 3; i++ {
		if _, err := m.StartProcess(context.Background(), StartProcessOptions{
			Command:    "sleep 30",
			WorkingDir: t.TempDir(),
		}); err != nil {
			t.Fatalf("StartProcess: %v", err)
		}
	}
	defer m.KillAllRunning()
	waitForRunning(t, m, 3)

	// Install the counters only now, so process startup is not counted.
	counts := installPortScanCounters(t)

	processes := m.GetAllProcesses()
	if len(processes) != 3 {
		t.Fatalf("GetAllProcesses returned %d processes, want 3", len(processes))
	}

	if got := counts.read(t, "ps"); got != 1 {
		t.Errorf("ps invocations = %d, want 1 for a list of 3 processes", got)
	}
	if got := counts.read(t, "lsof"); got != 1 {
		t.Errorf("lsof invocations = %d, want 1 for a list of 3 processes", got)
	}
}

// A listening port must still be reported — batching must not lose the data it
// was making cheaper to collect.
func TestGetProcessPorts_StillReportsAListeningPort(t *testing.T) {
	m := newTestBGManager()

	// Bind a port in a child of the shell, which is the case that needs the
	// process tree: the port belongs to a descendant, not to the pid we hold.
	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    "exec python3 -c 'import socket,time; s=socket.socket(); s.bind((\"127.0.0.1\",0)); s.listen(1); print(s.getsockname()[1], flush=True); time.sleep(30)'",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	defer m.KillAllRunning()

	var wantPort int
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		stdout, _, err := m.GetOutput(proc.ID)
		if err == nil {
			if line := strings.TrimSpace(stdout); line != "" {
				if p, convErr := strconv.Atoi(strings.Fields(line)[0]); convErr == nil {
					wantPort = p
					break
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if wantPort == 0 {
		stdout, stderr, _ := m.GetOutput(proc.ID)
		t.Skipf("helper never reported a port (stdout=%q stderr=%q); python3 may be unavailable", stdout, stderr)
	}

	var found bool
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range m.GetAllProcesses() {
			p.outputMu.RLock()
			ports := append([]PortInfo(nil), p.Ports...)
			p.outputMu.RUnlock()
			for _, portInfo := range ports {
				if portInfo.Port == wantPort {
					found = true
				}
			}
		}
		if found {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !found {
		t.Errorf("listening port %d was not reported by GetAllProcesses", wantPort)
	}
}

// processTreeFromSnapshot replaces a recursive pgrep descent, so it must expand
// a tree to the same set of PIDs, and must not hang on a cyclic snapshot taken
// while the process table was changing.
func TestProcessTreeFromSnapshot(t *testing.T) {
	children := map[int][]int{
		10: {20, 21},
		20: {30},
		30: {40},
	}

	got := processTreeFromSnapshot(10, children)
	sort.Ints(got)
	want := []int{10, 20, 21, 30, 40}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tree = %v, want %v", got, want)
	}

	if got := processTreeFromSnapshot(99, children); !reflect.DeepEqual(got, []int{99}) {
		t.Errorf("leaf tree = %v, want [99]", got)
	}

	// A cycle must terminate rather than recurse forever.
	cyclic := map[int][]int{1: {2}, 2: {1}}
	gotCyclic := processTreeFromSnapshot(1, cyclic)
	sort.Ints(gotCyclic)
	if !reflect.DeepEqual(gotCyclic, []int{1, 2}) {
		t.Errorf("cyclic tree = %v, want [1 2]", gotCyclic)
	}
}

// A port bound by both a parent and a child is reported once, as it was when
// each PID was scanned separately and deduplicated by port number.
func TestDedupePortsByNumber(t *testing.T) {
	got := dedupePortsByNumber([]PortInfo{
		{Port: 3000, Address: "127.0.0.1"},
		{Port: 3000, Address: "0.0.0.0"},
		{Port: 3001, Address: "127.0.0.1"},
	})
	want := []PortInfo{
		{Port: 3000, Address: "127.0.0.1"},
		{Port: 3001, Address: "127.0.0.1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deduped = %v, want %v", got, want)
	}
	if got := dedupePortsByNumber(nil); got != nil {
		t.Errorf("deduped nil = %v, want nil", got)
	}
}

// getPortsForPids must attribute each port to the PID that owns it, since one
// invocation now covers many processes.
func TestGetPortsForPids_AttributesPortsToOwningPID(t *testing.T) {
	if _, err := getPortsForPids(nil); err != nil {
		t.Fatalf("empty pid list: %v", err)
	}

	self := os.Getpid()
	ports, err := getPortsForPids([]int{self, os.Getppid()})
	if err != nil {
		t.Fatalf("getPortsForPids: %v", err)
	}
	for pid := range ports {
		if pid != self && pid != os.Getppid() {
			t.Errorf("ports attributed to pid %d, which was not requested", pid)
		}
	}
}

// --- counting shims -------------------------------------------------------

type portScanCounters struct{ dir string }

// installPortScanCounters puts shims for `ps` and `lsof` at the front of PATH.
// Each shim appends a line to a counter file and then execs the real binary, so
// behaviour is unchanged and only the invocation count is observed.
func installPortScanCounters(t *testing.T) *portScanCounters {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"ps", "lsof"} {
		real, err := realBinaryPath(name)
		if err != nil {
			t.Skipf("%s not found on PATH: %v", name, err)
		}
		script := "#!/bin/sh\n" +
			"echo call >> " + filepath.Join(dir, name+".count") + "\n" +
			"exec " + real + " \"$@\"\n"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatalf("write %s shim: %v", name, err)
		}
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &portScanCounters{dir: dir}
}

func (c *portScanCounters) read(t *testing.T, name string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.dir, name+".count"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read %s count: %v", name, err)
	}
	return len(strings.Fields(string(data)))
}

// realBinaryPath finds a binary on PATH, skipping the shim directory so the
// shim cannot exec itself.
func realBinaryPath(name string) (string, error) {
	for _, candidate := range []string{"/bin/" + name, "/usr/bin/" + name, "/usr/sbin/" + name, "/sbin/" + name} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func waitForRunning(t *testing.T, m *BackgroundManager, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		running := 0
		for _, p := range m.GetAllProcesses() {
			if p.StatusSafe() == "running" && p.GetPID() != 0 {
				running++
			}
		}
		if running >= want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d running processes", want)
}
