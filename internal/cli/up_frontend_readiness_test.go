package cli

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestFrontendStartupRequiresCurrentRunner(t *testing.T) {
	for _, tc := range []struct {
		name     string
		registry string
		listener int
		parents  map[int]int
		want     portReadyState
		exited   bool
	}{
		{name: "nil registry with listener", listener: 200, want: portReadyNobody},
		{name: "empty registry with listener", registry: "empty", listener: 200, want: portReadyNobody},
		{name: "registered runner listens", registry: "live", listener: 100, want: portReadyOurs},
		{name: "registered runner descendant", registry: "live", listener: 200, parents: map[int]int{200: 150, 150: 100}, want: portReadyOurs},
		{name: "exited runner leaves listener", registry: "exited", listener: 200, parents: map[int]int{200: 100}, want: portReadyNobody, exited: true},
		{name: "stale same project env runner", registry: "live", listener: 200, parents: map[int]int{200: 99, 99: 1}, want: portReadyForeign},
		{name: "foreign listener", registry: "live", listener: 300, parents: map[int]int{300: 1}, want: portReadyForeign},
		{name: "unknown listener pid", registry: "live", want: portReadyNobody},
		{name: "unknown ancestry", registry: "live", listener: 200, want: portReadyForeign},
		{name: "cyclic ancestry", registry: "live", listener: 200, parents: map[int]int{200: 201, 201: 200}, want: portReadyForeign},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var procs *procRegistry
			if tc.registry != "" {
				procs = &procRegistry{}
			}
			if tc.registry == "live" || tc.registry == "exited" {
				done := make(chan struct{})
				if tc.exited {
					close(done)
				}
				procs.processes = []*managedProcess{{name: "frontend:web", pid: 100, done: done}}
			}
			facts := fakeFacts{ppid: tc.parents}
			probe := func(name string, port int) hostReadyResult {
				return probeFrontendStartup(name, port, procs, func(int) bool { return true }, func(int) int { return tc.listener }, facts)
			}
			row := probe("web", 1234)
			if row.state != tc.want || (row.exited != "") != tc.exited {
				t.Fatalf("got %+v", row)
			}
			// Exercise the same real probe through waiting and the aggregate filter,
			// not a callback that fabricates an already-approved readiness result.
			err := waitFrontendStartup(t.Context(), "web", startupFixture(), "dev", time.Nanosecond, time.Hour, probe)
			if (err == nil) != (tc.want == portReadyOurs) {
				t.Fatalf("wait: %v; row %+v", err, row)
			}
			err = checkStartupDependencySnapshot([]startupNode{{name: "web", frontend: true}}, startupFixture(), testProj, "dev", procs, facts, probe)
			if (err == nil) != (tc.want == portReadyOurs) {
				t.Fatalf("aggregate disagrees: %v", err)
			}
		})
	}
}

// Start the test process with a stale launch's markers: OS process inspection
// reads exec-time environment, so t.Setenv alone does not model this on macOS.
func TestFrontendStartupMarkedListenerRegression(t *testing.T) {
	testkit.Slow(t, "executes marked test process for OS listener ownership")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestFrontendStartupProductionRejectsUnregisteredListener$")
	cmd.Env = append(os.Environ(), "FORGE_FRONTEND_MARKED_TEST=1", "FORGE_UP_PROJECT="+testProj, "FORGE_UP_ENV=dev", "FORGE_UP_SERVICE=frontend:web")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("marked listener regression: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "--- SKIP:") {
		t.Skipf("marked listener OS resource unavailable: %s", out)
	}
}

// Exercise both production entry points against a real listening socket. No
// registry means it cannot belong to this launch, regardless of OS PID probes.
func TestFrontendStartupProductionRejectsUnregisteredListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local socket resource unavailable: %v", err)
	}
	defer listener.Close()
	e := startupFixture()
	e.Frontends[0].Port = listener.Addr().(*net.TCPAddr).Port
	if os.Getenv("FORGE_FRONTEND_MARKED_TEST") == "1" {
		// Prove the old broad check would accept this exact listener. Missing
		// OS process inspection is a platform resource limitation, not proof.
		if state := classifyPortReadiness(e.Frontends[0].Port, testProj, "dev", portInUse, portListenerPID, newOSProcFacts()); state != portReadyOurs {
			t.Skip("OS process ownership inspection unavailable for marked listener")
		}
	}
	dep := startupNode{name: "web", frontend: true}
	for _, registry := range []*procRegistry{nil, {}} {
		if err := waitStartupDependency(t.Context(), dep, e, testProj, "dev", time.Nanosecond, registry); err == nil {
			t.Fatal("wait accepted an unregistered listener")
		}
		if err := checkStartupDependencies([]startupNode{dep}, e, testProj, "dev", registry); err == nil {
			t.Fatal("aggregate accepted an unregistered listener")
		}
	}
}

func TestFrontendStartupAbsentRegistryBlocksConsumer(t *testing.T) {
	nodes, err := hostStartupPlan(startupFixture(), nil)
	if err != nil {
		t.Fatal(err)
	}
	consumerStarted := false
	err = launchStartupPlan(t.Context(), nodes, func(n startupNode) error {
		if n.name == "desktop" {
			consumerStarted = true
		}
		return nil
	}, func(n startupNode) error {
		return waitFrontendStartup(t.Context(), n.name, startupFixture(), "dev", time.Nanosecond, time.Hour, func(name string, port int) hostReadyResult {
			return probeFrontendStartup(name, port, nil, func(int) bool { return true }, func(int) int { return 200 }, fakeFacts{ppid: map[int]int{200: 100}})
		})
	})
	if err == nil || consumerStarted {
		t.Fatalf("missing runner released consumer: %v", err)
	}
}
