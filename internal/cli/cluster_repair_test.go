package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSafeModeK3sArgs(t *testing.T) {
	got, err := safeModeK3sArgs([]string{
		"server", "--disable=traefik", "--disable-network-policy=false", "--tls-san", "k3d-cp-serverlb",
	})
	if err != nil {
		t.Fatalf("safeModeK3sArgs: %v", err)
	}
	want := []string{"server", "--disable=traefik", "--tls-san", "k3d-cp-serverlb", "--disable-network-policy"}
	if !slices.Equal(got, want) {
		t.Fatalf("safe args = %v; want %v", got, want)
	}
	if _, err := safeModeK3sArgs([]string{"agent"}); err == nil {
		t.Fatal("agent args unexpectedly accepted for server repair")
	}
}

func TestFindK3dEntrypointPID(t *testing.T) {
	const processes = `PID COMMAND
    1 /sbin/docker-init -- /bin/k3d-entrypoint.sh server --tls-san 0.0.0.0
    7 {k3d-entrypoint.} /bin/sh /bin/k3d-entrypoint.sh server --tls-san 0.0.0.0
   39 /bin/k3s server
`
	pid, err := findK3dEntrypointPID(processes)
	if err != nil {
		t.Fatalf("findK3dEntrypointPID: %v", err)
	}
	if pid != 7 {
		t.Fatalf("entrypoint PID = %d; want 7", pid)
	}
}

func TestK3dNodeCurrentIP(t *testing.T) {
	var info dockerK3dNodeInspect
	info.NetworkSettings.Networks = map[string]struct {
		IPAddress string `json:"IPAddress"`
	}{
		"k3d-control-plane": {IPAddress: "172.27.0.5"},
	}
	got, err := k3dNodeCurrentIP(info, ClusterEntity{
		Name: "cp-daemon", Network: "k3d-control-plane", RegistryInherit: true,
	})
	if err != nil {
		t.Fatalf("k3dNodeCurrentIP: %v", err)
	}
	if got != "172.27.0.5" {
		t.Fatalf("current IP = %q; want 172.27.0.5", got)
	}
}

func TestK3sNodeIPDriftFailureSignature(t *testing.T) {
	logs := `time="2026-08-09T17:43:27Z" level=error msg="Shutdown request received: \"failed to start networking: unable to initialize network policy controller: error getting node subnet: failed to find interface with specified node ip\""`
	if !strings.Contains(logs, k3sNodeIPDriftFailure) {
		t.Fatal("known node-IP drift log was not recognized")
	}
}

func TestEnsureRunningK3dClusterHealthyRepairsKnownNodeIPDrift(t *testing.T) {
	origReady := runningClusterNodesReadyFn
	origProcess := containerHasK3sProcessFn
	origInspect := inspectDockerK3dNodeFn
	origLogs := dockerLogsSinceStartFn
	origRepair := repairK3sNodeIPDriftFn
	origGrace := k3sProcessGrace
	t.Cleanup(func() {
		runningClusterNodesReadyFn = origReady
		containerHasK3sProcessFn = origProcess
		inspectDockerK3dNodeFn = origInspect
		dockerLogsSinceStartFn = origLogs
		repairK3sNodeIPDriftFn = origRepair
		k3sProcessGrace = origGrace
	})
	// k3s is stubbed permanently absent; don't pay the real startup grace.
	k3sProcessGrace = 0

	runningClusterNodesReadyFn = func(context.Context, string) (bool, error) {
		return false, errors.New("connection refused")
	}
	containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return false, nil }
	inspectDockerK3dNodeFn = func(_ context.Context, node string) (dockerK3dNodeInspect, error) {
		if node != "k3d-cp-daemon-server-0" {
			t.Fatalf("inspect node = %q", node)
		}
		var info dockerK3dNodeInspect
		info.Args = []string{"server", "--tls-san", "k3d-cp-daemon-serverlb"}
		info.State.StartedAt = "2026-08-09T17:43:24Z"
		info.NetworkSettings.Networks = map[string]struct {
			IPAddress string `json:"IPAddress"`
		}{"k3d-control-plane": {IPAddress: "172.27.0.5"}}
		return info, nil
	}
	dockerLogsSinceStartFn = func(_ context.Context, node, startedAt string) (string, error) {
		if node != "k3d-cp-daemon-server-0" || startedAt != "2026-08-09T17:43:24Z" {
			t.Fatalf("logs args = node %q startedAt %q", node, startedAt)
		}
		return "Shutdown request received: " + k3sNodeIPDriftFailure, nil
	}
	repaired := false
	repairK3sNodeIPDriftFn = func(_ context.Context, c ClusterEntity, node, currentIP string, args []string) error {
		repaired = true
		if c.Name != "cp-daemon" || node != "k3d-cp-daemon-server-0" || currentIP != "172.27.0.5" {
			t.Fatalf("repair args = cluster %q node %q IP %q", c.Name, node, currentIP)
		}
		if !slices.Equal(args, []string{"server", "--tls-san", "k3d-cp-daemon-serverlb"}) {
			t.Fatalf("repair container args = %v", args)
		}
		return nil
	}

	err := ensureRunningK3dClusterHealthy(t.Context(), ClusterEntity{
		Name: "cp-daemon", Network: "k3d-control-plane", RegistryInherit: true, Servers: 1,
	})
	if err != nil {
		t.Fatalf("ensureRunningK3dClusterHealthy: %v", err)
	}
	if !repaired {
		t.Fatal("known node-IP drift did not trigger repair")
	}
}

func TestEnsureRunningK3dClusterHealthyReadyFastPath(t *testing.T) {
	origReady := runningClusterNodesReadyFn
	origProcess := containerHasK3sProcessFn
	t.Cleanup(func() {
		runningClusterNodesReadyFn = origReady
		containerHasK3sProcessFn = origProcess
	})

	runningClusterNodesReadyFn = func(_ context.Context, name string) (bool, error) {
		return name == "control-plane", nil
	}
	containerHasK3sProcessFn = func(context.Context, string) (bool, error) {
		t.Fatal("healthy fast path inspected the container process")
		return false, nil
	}
	if err := ensureRunningK3dClusterHealthy(t.Context(), ClusterEntity{Name: "control-plane"}); err != nil {
		t.Fatalf("healthy fast path: %v", err)
	}
}

// TestEnsureRunningK3dClusterHealthySaysWhyTheProbeFailed pins the reporting
// contract that motivated the probe's signature: when the API does not confirm
// Ready, forge must say what it actually observed rather than assert an
// unestablished diagnosis. A run that only ever printed "API is still
// starting" for a cluster whose API was answering fine left the operator with
// no way to tell a slow start from a blip from a wedged host.
func TestEnsureRunningK3dClusterHealthySaysWhyTheProbeFailed(t *testing.T) {
	origReady := runningClusterNodesReadyFn
	origProcess := containerHasK3sProcessFn
	origAttempt := nodeReadyAttemptFn
	origBudget := nodeReadyWaitBudget
	t.Cleanup(func() {
		runningClusterNodesReadyFn = origReady
		containerHasK3sProcessFn = origProcess
		nodeReadyAttemptFn = origAttempt
		nodeReadyWaitBudget = origBudget
	})

	runningClusterNodesReadyFn = func(context.Context, string) (bool, error) {
		return false, errors.New("Unable to connect to the server: EOF")
	}
	containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return true, nil }
	nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) { return "", nil }
	nodeReadyWaitBudget = time.Second

	out := captureStdout(t, func() {
		if err := ensureRunningK3dClusterHealthy(t.Context(), ClusterEntity{Name: "control-plane"}); err != nil {
			t.Fatalf("ensureRunningK3dClusterHealthy: %v", err)
		}
	})
	if !strings.Contains(out, "Unable to connect to the server: EOF") {
		t.Fatalf("probe reason was not reported; got:\n%s", out)
	}
}

// TestEnsureRunningK3dClusterHealthyCarriesProbeReasonIntoTheError covers the
// same contract on the error path: k3s exited for a reason forge cannot
// auto-repair, and the message has to carry BOTH halves of the evidence — the
// dead process and what Kubernetes said.
func TestEnsureRunningK3dClusterHealthyCarriesProbeReasonIntoTheError(t *testing.T) {
	origReady := runningClusterNodesReadyFn
	origProcess := containerHasK3sProcessFn
	origInspect := inspectDockerK3dNodeFn
	origLogs := dockerLogsSinceStartFn
	origGrace := k3sProcessGrace
	t.Cleanup(func() {
		runningClusterNodesReadyFn = origReady
		containerHasK3sProcessFn = origProcess
		inspectDockerK3dNodeFn = origInspect
		dockerLogsSinceStartFn = origLogs
		k3sProcessGrace = origGrace
	})
	// k3s is stubbed permanently absent; don't pay the real startup grace.
	k3sProcessGrace = 0

	runningClusterNodesReadyFn = func(context.Context, string) (bool, error) {
		return false, errors.New("x509: certificate has expired")
	}
	containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return false, nil }
	inspectDockerK3dNodeFn = func(context.Context, string) (dockerK3dNodeInspect, error) {
		return dockerK3dNodeInspect{}, nil
	}
	dockerLogsSinceStartFn = func(context.Context, string, string) (string, error) {
		return "some unrelated shutdown", nil
	}
	origObserve := observeK3sStartFn
	t.Cleanup(func() { observeK3sStartFn = origObserve })
	observeK3sStartFn = func(context.Context, string, []string) (string, error) {
		return `level=fatal msg="etcd cluster join failed"`, nil
	}

	err := ensureRunningK3dClusterHealthy(t.Context(), ClusterEntity{Name: "control-plane"})
	if err == nil {
		t.Fatal("an unrecognized k3s exit was accepted as healthy")
	}
	if !strings.Contains(err.Error(), "x509: certificate has expired") {
		t.Fatalf("error dropped the probe reason: %v", err)
	}
	if !strings.Contains(err.Error(), "etcd cluster join failed") {
		t.Fatalf("error dropped what the foreground start printed: %v", err)
	}
}

// stubK3dDriftDiagnosis wires the docker-facing seams for a single-server node
// whose current IP is 172.20.0.5 and whose `docker logs` returns logs, and
// records whether the repair ran.
func stubK3dDriftDiagnosis(t *testing.T, logs string, observed string) (repaired *bool, observedCalls *int) {
	t.Helper()
	origInspect := inspectDockerK3dNodeFn
	origLogs := dockerLogsSinceStartFn
	origObserve := observeK3sStartFn
	origRepair := repairK3sNodeIPDriftFn
	t.Cleanup(func() {
		inspectDockerK3dNodeFn = origInspect
		dockerLogsSinceStartFn = origLogs
		observeK3sStartFn = origObserve
		repairK3sNodeIPDriftFn = origRepair
	})
	inspectDockerK3dNodeFn = func(context.Context, string) (dockerK3dNodeInspect, error) {
		var info dockerK3dNodeInspect
		info.Args = []string{"server", "--tls-san", "k3d-control-plane-v2-serverlb"}
		info.State.StartedAt = "2026-10-20T23:19:25Z"
		info.NetworkSettings.Networks = map[string]struct {
			IPAddress string `json:"IPAddress"`
		}{"k3d-control-plane-v2": {IPAddress: "172.20.0.5"}}
		return info, nil
	}
	dockerLogsSinceStartFn = func(context.Context, string, string) (string, error) { return logs, nil }
	observedCalls = new(int)
	observeK3sStartFn = func(_ context.Context, node string, args []string) (string, error) {
		*observedCalls++
		if node != "k3d-control-plane-v2-server-0" || len(args) == 0 || args[0] != "server" {
			t.Fatalf("observe args = node %q args %v", node, args)
		}
		return observed, nil
	}
	repaired = new(bool)
	repairK3sNodeIPDriftFn = func(_ context.Context, c ClusterEntity, node, currentIP string, _ []string) error {
		*repaired = true
		if node != "k3d-control-plane-v2-server-0" || currentIP != "172.20.0.5" {
			t.Fatalf("repair args = node %q IP %q", node, currentIP)
		}
		return nil
	}
	return repaired, observedCalls
}

// TestEnsureRunningK3dClusterHealthyObservesStartWhenLogsAreUnreadable pins
// the recovery for a node whose `docker logs` returns nothing: Docker Desktop
// VMs that stop uncleanly leave NUL bytes in json-file logs, past which Docker
// reads nothing. The drift failure is still there to be seen — by starting
// k3s and watching it — and must still be repaired.
func TestEnsureRunningK3dClusterHealthyObservesStartWhenLogsAreUnreadable(t *testing.T) {
	origReady := runningClusterNodesReadyFn
	origProcess := containerHasK3sProcessFn
	origGrace := k3sProcessGrace
	t.Cleanup(func() {
		runningClusterNodesReadyFn = origReady
		containerHasK3sProcessFn = origProcess
		k3sProcessGrace = origGrace
	})
	k3sProcessGrace = 0
	runningClusterNodesReadyFn = func(context.Context, string) (bool, error) {
		return false, errors.New("Unable to connect to the server: EOF")
	}
	containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return false, nil }
	repaired, observedCalls := stubK3dDriftDiagnosis(t, "",
		`level=error msg="Shutdown request received: \"failed to start networking: unable to initialize network policy controller: error getting node subnet: `+k3sNodeIPDriftFailure+`\""`)

	_ = captureStdout(t, func() {
		if err := ensureRunningK3dClusterHealthy(t.Context(), ClusterEntity{Name: "control-plane-v2", Servers: 1}); err != nil {
			t.Fatalf("ensureRunningK3dClusterHealthy: %v", err)
		}
	})
	if *observedCalls != 1 {
		t.Fatalf("foreground start observed %d times; want 1", *observedCalls)
	}
	if !*repaired {
		t.Fatal("drift seen in a foreground start did not trigger repair")
	}
}

// TestEnsureRunningK3dClusterHealthyDiagnosesK3sThatExitsDuringWait pins the
// sequence that let a dead cluster through: k3s was alive when sampled, then
// exited seconds later (drift kills it just after its API first answers).
// forge must notice the exit while waiting and repair, not wait out its
// budget — or worse, accept the stale Ready status and report healthy.
func TestEnsureRunningK3dClusterHealthyDiagnosesK3sThatExitsDuringWait(t *testing.T) {
	origReady := runningClusterNodesReadyFn
	origProcess := containerHasK3sProcessFn
	origGrace := k3sProcessGrace
	origAttempt := nodeReadyAttemptFn
	origBudget := nodeReadyWaitBudget
	origInterval := nodeReadyRetryInterval
	t.Cleanup(func() {
		runningClusterNodesReadyFn = origReady
		containerHasK3sProcessFn = origProcess
		k3sProcessGrace = origGrace
		nodeReadyAttemptFn = origAttempt
		nodeReadyWaitBudget = origBudget
		nodeReadyRetryInterval = origInterval
	})
	k3sProcessGrace = 0
	nodeReadyWaitBudget = time.Minute
	nodeReadyRetryInterval = time.Millisecond
	runningClusterNodesReadyFn = func(context.Context, string) (bool, error) {
		return false, errors.New("the server is currently unable to handle the request")
	}
	samples := 0
	containerHasK3sProcessFn = func(context.Context, string) (bool, error) {
		samples++
		return samples == 1, nil // alive when first sampled, gone after
	}
	nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) {
		return "Unable to connect to the server: EOF", errors.New("exit status 1")
	}
	repaired, _ := stubK3dDriftDiagnosis(t, "", "Shutdown request received: "+k3sNodeIPDriftFailure)

	done := make(chan error, 1)
	_ = captureStdout(t, func() {
		go func() {
			done <- ensureRunningK3dClusterHealthy(t.Context(), ClusterEntity{Name: "control-plane-v2", Servers: 1})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ensureRunningK3dClusterHealthy: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("forge kept waiting on a k3s that had exited")
		}
	})
	if !*repaired {
		t.Fatal("k3s exiting during the readiness wait did not lead to repair")
	}
}

func TestWaitNodeReadyWhileStopsWhenTheProcessIsGone(t *testing.T) {
	origAttempt := nodeReadyAttemptFn
	origBudget := nodeReadyWaitBudget
	origInterval := nodeReadyRetryInterval
	t.Cleanup(func() {
		nodeReadyAttemptFn = origAttempt
		nodeReadyWaitBudget = origBudget
		nodeReadyRetryInterval = origInterval
	})
	nodeReadyWaitBudget = time.Minute
	nodeReadyRetryInterval = time.Millisecond
	nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) {
		return "connection refused", errors.New("exit status 1")
	}

	err := waitNodeReadyWhile(t.Context(), "cp", func(context.Context) (bool, error) { return false, nil })
	if !errors.Is(err, errK3sExitedDuringWait) {
		t.Fatalf("err = %v; want errK3sExitedDuringWait", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error dropped the last attempt's output: %v", err)
	}
}

// TestVerifyNodesReadyThisBootRejectsPreviousBootStatus pins the gap a
// restarted k3s leaves: its API answers with the Ready=True the PREVIOUS
// boot's kubelet wrote, which `kubectl wait` accepts at once.
func TestVerifyNodesReadyThisBootRejectsPreviousBootStatus(t *testing.T) {
	origHeartbeats := nodeReadyHeartbeatsFn
	origStarted := nodeContainerStartedAtFn
	t.Cleanup(func() {
		nodeReadyHeartbeatsFn = origHeartbeats
		nodeContainerStartedAtFn = origStarted
	})
	started := time.Date(2026, 10, 20, 23, 19, 25, 270_000_000, time.UTC)
	nodeContainerStartedAtFn = func(context.Context, string) (time.Time, error) { return started, nil }

	nodeReadyHeartbeatsFn = func(context.Context, string) (map[string]time.Time, error) {
		return map[string]time.Time{"k3d-cp-server-0": time.Date(2026, 10, 11, 1, 38, 0, 0, time.UTC)}, nil
	}
	err := verifyNodesReadyThisBoot(t.Context(), "k3d-cp")
	if err == nil || !strings.Contains(err.Error(), "k3d-cp-server-0") {
		t.Fatalf("stale Ready accepted (err = %v)", err)
	}

	// The same second as the start counts as this boot: heartbeats carry
	// whole seconds only.
	nodeReadyHeartbeatsFn = func(context.Context, string) (map[string]time.Time, error) {
		return map[string]time.Time{"k3d-cp-server-0": started.Truncate(time.Second)}, nil
	}
	if err := verifyNodesReadyThisBoot(t.Context(), "k3d-cp"); err != nil {
		t.Fatalf("fresh Ready rejected: %v", err)
	}
}

func TestStaleReadyNodes(t *testing.T) {
	start := time.Date(2026, 10, 20, 23, 19, 25, 0, time.UTC)
	got := staleReadyNodes(
		map[string]time.Time{
			"fresh":        start.Add(3 * time.Second),
			"stale":        start.Add(-time.Hour),
			"no-heartbeat": {},
			"not-docker":   start.Add(-time.Hour),
		},
		map[string]time.Time{
			"fresh":        start,
			"stale":        start,
			"no-heartbeat": start,
		})
	if want := []string{"no-heartbeat", "stale"}; !slices.Equal(got, want) {
		t.Fatalf("stale nodes = %v; want %v", got, want)
	}
}

func TestParseNodeReadyHeartbeats(t *testing.T) {
	got, err := parseNodeReadyHeartbeats("k3d-cp-server-0\t2026-10-20T23:19:31Z\nk3d-cp-agent-0\t\n")
	if err != nil {
		t.Fatalf("parseNodeReadyHeartbeats: %v", err)
	}
	if want := time.Date(2026, 10, 20, 23, 19, 31, 0, time.UTC); !got["k3d-cp-server-0"].Equal(want) {
		t.Fatalf("server heartbeat = %v; want %v", got["k3d-cp-server-0"], want)
	}
	if hb, ok := got["k3d-cp-agent-0"]; !ok || !hb.IsZero() {
		t.Fatalf("agent heartbeat = %v (present %v); want zero and present", hb, ok)
	}
}

func TestK3sStartWatcherStopsOnDriftSignature(t *testing.T) {
	stopped := false
	w := newK3sStartWatcher(2, func() { stopped = true })
	_, _ = w.Write([]byte("Starting k3s\nk3s is up and running\nShutdown request received: failed to find interface "))
	if stopped {
		t.Fatal("stopped before the signature line was complete")
	}
	_, _ = w.Write([]byte("with specified node ip\ntrailing"))
	if !stopped {
		t.Fatal("drift signature split across writes did not stop the observation")
	}
	// A limit of 2 keeps the last two complete lines, plus the partial one.
	want := "k3s is up and running\nShutdown request received: " + k3sNodeIPDriftFailure + "\ntrailing"
	if got := w.Lines(); got != want {
		t.Fatalf("lines = %q; want %q", got, want)
	}
}

// TestWaitForK3sProcessToleratesTheStartupWindow pins that a node sampled
// moments after a restart is not mistaken for a dead one. k3d's entrypoint
// does its own setup before it execs k3s, so absence is only conclusive once
// it has persisted — a single sample would send a healthy, still-starting node
// down the log-diagnosis path, where it finds no failure signature (nothing
// failed) and aborts with a misleading error.
func TestWaitForK3sProcessToleratesTheStartupWindow(t *testing.T) {
	origProcess := containerHasK3sProcessFn
	origGrace := k3sProcessGrace
	origPoll := k3sProcessPollInterval
	t.Cleanup(func() {
		containerHasK3sProcessFn = origProcess
		k3sProcessGrace = origGrace
		k3sProcessPollInterval = origPoll
	})
	k3sProcessGrace = time.Second
	k3sProcessPollInterval = time.Millisecond

	calls := 0
	containerHasK3sProcessFn = func(context.Context, string) (bool, error) {
		calls++
		return calls >= 3, nil // absent, absent, then k3s appears
	}
	running, err := waitForK3sProcess(t.Context(), "k3d-cp-daemon-server-0")
	if err != nil {
		t.Fatalf("waitForK3sProcess: %v", err)
	}
	if !running {
		t.Fatalf("a node whose k3s appeared after %d samples was reported as exited", calls)
	}
}

// TestWaitForK3sProcessReportsASustainedAbsence keeps the drift diagnosis
// reachable: k3s that never appears within the grace IS exited, and the caller
// must go on to inspect the logs and repair.
func TestWaitForK3sProcessReportsASustainedAbsence(t *testing.T) {
	origProcess := containerHasK3sProcessFn
	origGrace := k3sProcessGrace
	origPoll := k3sProcessPollInterval
	t.Cleanup(func() {
		containerHasK3sProcessFn = origProcess
		k3sProcessGrace = origGrace
		k3sProcessPollInterval = origPoll
	})
	k3sProcessGrace = 20 * time.Millisecond
	k3sProcessPollInterval = time.Millisecond

	containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return false, nil }
	running, err := waitForK3sProcess(t.Context(), "k3d-cp-daemon-server-0")
	if err != nil {
		t.Fatalf("waitForK3sProcess: %v", err)
	}
	if running {
		t.Fatal("a node with no k3s at all was reported as running")
	}
}
