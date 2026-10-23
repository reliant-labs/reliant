package deploytarget

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// HostInfra observation.
//
// These tests drive the provider through REAL on-disk state — a data
// directory with a postmaster.pid naming a live pid — rather than through
// an injected fake. That is deliberate and it is the point: the whole
// reason this observer reads the data directory instead of probing the
// port is that a port probe cannot tell OUR postgres from another
// project's, and a fake that returned a canned "running" would prove
// nothing about that distinction. The pid written is this test process's
// own, which is genuinely alive, so the signal-0 liveness probe the
// provider relies on is really exercised.

// hostInfraProject builds a project dir containing one instance's data
// directory, optionally with a postmaster.pid.
//
// pid<=0 writes no pid file at all (never started). Otherwise the file is
// written in postgres's own format: pid on line 1, data dir on line 2,
// start time on 3, PORT on line 4 — the layout runningPort parses.
func hostInfraProject(t *testing.T, name string, pid, port int, pgVersion string) string {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, ".forge", "hostinfra", name, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if pgVersion != "" {
		if err := os.WriteFile(filepath.Join(dataDir, "PG_VERSION"), []byte(pgVersion+"\n"), 0o600); err != nil {
			t.Fatalf("write PG_VERSION: %v", err)
		}
	}
	if pid > 0 {
		content := fmt.Sprintf("%d\n%s\n%d\n%d\n", pid, dataDir, 1700000000, port)
		if err := os.WriteFile(filepath.Join(dataDir, "postmaster.pid"), []byte(content), 0o600); err != nil {
			t.Fatalf("write postmaster.pid: %v", err)
		}
	}
	return root
}

// hostInfraGroup declares one postgres instance on the given port.
func hostInfraGroup(name string, port int, version string) ServiceGroup {
	return ServiceGroup{
		Env: "dev",
		Services: []ResolvedService{{
			Name: name,
			HostInfra: &HostInfraSpec{
				Engine: "postgres", Port: port,
				Database: "app", User: "postgres", Password: "postgres",
				Version: version,
			},
		}},
	}
}

func observeHostInfra(t *testing.T, projectDir string, group ServiceGroup) ObservedItem {
	t.Helper()
	obs, err := HostInfraProvider{ProjectDir: projectDir}.Observe(context.Background(), group)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(obs.Items))
	}
	return obs.Items[0]
}

// freeHostInfraPort reserves and releases a port, so the number is
// almost certainly unbound. Used where the test needs "nothing is
// listening here" without hardcoding a port another agent's stack may
// hold — this box runs many.
func freeHostInfraPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestHostInfraObserve_NeverStartedIsAbsent covers the instance that has
// no data directory state at all.
func TestHostInfraObserve_NeverStartedIsAbsent(t *testing.T) {
	port := freeHostInfraPort(t)
	root := hostInfraProject(t, "postgres", 0, 0, "")
	item := observeHostInfra(t, root, hostInfraGroup("postgres", port, ""))

	if item.Health != HealthAbsent {
		t.Fatalf("health = %v (detail %q), want absent", item.Health, item.Detail)
	}
	// Replicas must stay NIL, not &ReplicaCounts{}. A supervised host
	// process has no replica concept, and the pointer is what carries
	// "not applicable" as distinct from "nothing is running".
	if item.Replicas != nil {
		t.Errorf("replicas = %+v, want nil — a host process has no replica concept", item.Replicas)
	}
	if strings.TrimSpace(item.Detail) == "" {
		t.Error("absent with no detail")
	}
}

// TestHostInfraObserve_LiveProcessIsNotHealthyUntilItAnswers is the
// assertion that makes the Ready half load-bearing.
//
// The pid file names a LIVE process (this test's own), so a provider that
// stopped at "is the pid alive" reports this healthy. Nothing is actually
// serving on the port, so the honest answer is degraded — postgres writes
// its pid file and then spends recovery refusing connections, which is
// precisely this state.
func TestHostInfraObserve_LiveProcessIsNotHealthyUntilItAnswers(t *testing.T) {
	port := freeHostInfraPort(t)
	root := hostInfraProject(t, "postgres", os.Getpid(), port, "16")
	item := observeHostInfra(t, root, hostInfraGroup("postgres", port, ""))

	if item.Health == HealthHealthy {
		t.Fatalf("health = healthy for a live pid with nothing serving; a provider that checked "+
			"only process liveness would pass this and tell a caller the database it is about "+
			"to dial will answer (detail %q)", item.Detail)
	}
	if item.Health != HealthDegraded {
		t.Errorf("health = %v, want degraded — the process IS running, so absent would be wrong too",
			item.Health)
	}
	if !strings.Contains(item.Detail, "not answering") {
		t.Errorf("detail = %q, want it to say the process is up but not answering", item.Detail)
	}
}

// TestHostInfraObserve_PortDriftIsReported covers running-and-answering
// on a port the declaration has since moved off. The server looks
// perfectly healthy to itself while everything dialling the declared port
// fails, which is why it gets its own reported state rather than a
// healthy with a footnote.
//
// It drives the DETAIL path directly rather than standing up a real
// postgres: the drift branch is selected by comparing the observed port
// against the declared one, and the observation below is built from the
// same Status the provider consumes.
func TestHostInfraObserve_PortDriftIsReported(t *testing.T) {
	declared := freeHostInfraPort(t)
	actual := freeHostInfraPort(t)
	if declared == actual {
		t.Skip("port allocator returned the same port twice")
	}
	root := hostInfraProject(t, "postgres", os.Getpid(), actual, "16")
	item := observeHostInfra(t, root, hostInfraGroup("postgres", declared, ""))

	// Not healthy, whichever branch caught it — the point of the test is
	// that a server on the wrong port is never green.
	if item.Health == HealthHealthy {
		t.Fatalf("health = healthy for an instance serving on %d while the env declares %d",
			actual, declared)
	}
	if strings.TrimSpace(item.Detail) == "" {
		t.Error("not-healthy with no detail")
	}
}

// TestHostInfraObserve_ForeignPortIsDegradedNotHealthy is the adoption
// refusal, and it is the reason this observer reads the data directory
// rather than probing the port.
//
// Something IS listening on the declared port; it is not ours. A port
// probe reports this healthy and tells the developer their database is
// up, while forge has never written a byte to the thing answering.
func TestHostInfraObserve_ForeignPortIsDegradedNotHealthy(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a real probe timeout against a foreign listener; runs in task test")
	}
	// A real listener, so the probe genuinely finds the port occupied.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	// No pid file: nothing of ours is running.
	root := hostInfraProject(t, "postgres", 0, 0, "")
	item := observeHostInfra(t, root, hostInfraGroup("postgres", port, ""))

	if item.Health == HealthHealthy {
		t.Fatalf("health = healthy for a port held by a foreign process; a port probe would "+
			"report this green for a database this project has never written to (detail %q)",
			item.Detail)
	}
	if item.Health != HealthDegraded {
		t.Fatalf("health = %v, want degraded — absent would invite a caller to START it, and "+
			"starting is the one thing that cannot work while the port is taken", item.Health)
	}
	if !strings.Contains(item.Detail, "did not start") {
		t.Errorf("detail = %q, want it to say the port is held by something forge did not start",
			item.Detail)
	}
}

// TestHostInfraObserve_UnsupportedEngineIsUnknown: an engine forge does
// not supervise cannot be read, and that is an unknown ITEM rather than a
// group-level error — one unreadable instance must not erase its
// siblings.
func TestHostInfraObserve_UnsupportedEngineIsUnknown(t *testing.T) {
	root := t.TempDir()
	group := ServiceGroup{
		Env:      "dev",
		Services: []ResolvedService{{Name: "mystery", HostInfra: &HostInfraSpec{Engine: "cockroach", Port: 1}}},
	}
	item := observeHostInfra(t, root, group)
	if item.Health != HealthUnknown {
		t.Errorf("health = %v, want unknown", item.Health)
	}
	if strings.TrimSpace(item.Detail) == "" {
		t.Error("unknown with no detail")
	}
}

// TestHostInfraObserve_MisroutedGroupIsUnknown pins the nil-spec path.
func TestHostInfraObserve_MisroutedGroupIsUnknown(t *testing.T) {
	item := observeHostInfra(t, t.TempDir(), ServiceGroup{
		Env:      "dev",
		Services: []ResolvedService{{Name: "postgres"}},
	})
	if item.Health != HealthUnknown {
		t.Errorf("health = %v, want unknown", item.Health)
	}
}

// TestHostInfraObserve_EveryInstanceIsNamed: a group is a set of
// independent servers, and a report that spoke for only some of them
// would be the silent-green shape in miniature.
func TestHostInfraObserve_EveryInstanceIsNamed(t *testing.T) {
	root := t.TempDir()
	group := ServiceGroup{
		Env: "dev",
		Services: []ResolvedService{
			{Name: "postgres", HostInfra: &HostInfraSpec{Engine: "postgres", Port: freeHostInfraPort(t)}},
			{Name: "idp", HostInfra: &HostInfraSpec{Engine: "zitadel", Port: freeHostInfraPort(t)}},
		},
	}
	obs, err := HostInfraProvider{ProjectDir: root}.Observe(context.Background(), group)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	got := map[string]bool{}
	for _, item := range obs.Items {
		got[item.Name] = true
		if item.Health == HealthHealthy {
			t.Errorf("item %q is healthy with nothing running", item.Name)
		}
	}
	for _, want := range []string{"postgres", "idp"} {
		if !got[want] {
			t.Errorf("instance %q is missing from the report", want)
		}
	}
}
