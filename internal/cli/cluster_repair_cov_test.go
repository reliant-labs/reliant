package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

const covCPSOutput = `PID COMMAND
    1 /sbin/docker-init -- /bin/k3d-entrypoint.sh server --tls-san 0.0.0.0
    7 {k3d-entrypoint.} /bin/sh /bin/k3d-entrypoint.sh server --tls-san 0.0.0.0
   39 /bin/k3s server
`

func TestCovC_ContainerHasK3sProcess(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		want    bool
		wantErr string
	}{
		{"pidof finds k3s", `echo 39`, true, ""},
		{"pidof exit 1 means no k3s, not an error", `exit 1`, false, ""},
		{"any other failure is surfaced", `echo "container is not running" >&2; exit 126`, false, "inspect k3s process in k3d-c-server-0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logPath := covCInstallTools(t, map[string]string{"docker": tc.script})
			got, err := containerHasK3sProcess(context.Background(), "k3d-c-server-0")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "container is not running") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("= %v, %v; want %v", got, err, tc.want)
			}
			if !covCHasCall(covCCalls(t, logPath), "docker exec k3d-c-server-0 pidof k3s") {
				t.Errorf("calls = %v", covCCalls(t, logPath))
			}
		})
	}
}

func TestCovC_InspectDockerK3dNode(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantErr string
	}{
		{"one record", `echo '[{"Args":["server","--tls-san","x"],"State":{"StartedAt":"2026-11-20T10:00:00Z"},"NetworkSettings":{"Networks":{"k3d-c":{"IPAddress":"172.20.0.5"}}}}]'`, ""},
		{"docker failure", `echo "no such container" >&2; exit 1`, "inspect k3d-c-server-0"},
		{"not JSON", `echo 'garbage'`, "decode docker inspect for k3d-c-server-0"},
		{"two records", `echo '[{},{}]'`, "returned 2 records; want 1"},
		{"no records", `echo '[]'`, "returned 0 records; want 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			covCInstallTools(t, map[string]string{"docker": tc.script})
			info, err := inspectDockerK3dNode(context.Background(), "k3d-c-server-0")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v; want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || info.State.StartedAt != "2026-11-20T10:00:00Z" || !slices.Equal(info.Args, []string{"server", "--tls-san", "x"}) {
				t.Fatalf("= %+v, %v", info, err)
			}
			if ip, err := k3dNodeCurrentIP(info, ClusterEntity{Name: "c"}); err != nil || ip != "172.20.0.5" {
				t.Errorf("IP derived from the default network = %q, %v", ip, err)
			}
		})
	}
}

func TestCovC_DockerLogsSinceStart(t *testing.T) {
	logPath := covCInstallTools(t, map[string]string{"docker": `case "$*" in *broken*) echo "no logs" >&2; exit 1;; esac; echo "line"`})
	ctx := context.Background()
	if got, err := dockerLogsSinceStart(ctx, "n1", "2026-11-20T10:00:00Z"); err != nil || got != "line\n" {
		t.Fatalf("= %q, %v", got, err)
	}
	if _, err := dockerLogsSinceStart(ctx, "n1", ""); err != nil {
		t.Fatal(err)
	}
	calls := covCCalls(t, logPath)
	if calls[0] != "docker logs --since 2026-11-20T10:00:00Z n1" || calls[1] != "docker logs --tail 500 n1" {
		t.Errorf("a known start reads only that start's logs, an unknown one the last 500 lines: %v", calls)
	}
	if _, err := dockerLogsSinceStart(ctx, "broken", ""); err == nil || !strings.Contains(err.Error(), "read current-start logs for broken") {
		t.Errorf("err = %v", err)
	}
}

func TestCovC_K3dNodeCurrentIPFallbacks(t *testing.T) {
	mk := func(nets map[string]string) dockerK3dNodeInspect {
		var info dockerK3dNodeInspect
		info.NetworkSettings.Networks = map[string]struct {
			IPAddress string `json:"IPAddress"`
		}{}
		for n, ip := range nets {
			info.NetworkSettings.Networks[n] = struct {
				IPAddress string `json:"IPAddress"`
			}{IPAddress: ip}
		}
		return info
	}
	c := ClusterEntity{Name: "c"}
	if ip, err := k3dNodeCurrentIP(mk(map[string]string{"bridge": "172.17.0.2"}), c); err != nil || ip != "172.17.0.2" {
		t.Errorf("a node on exactly one network uses it: %q, %v", ip, err)
	}
	_, err := k3dNodeCurrentIP(mk(map[string]string{"a": "1.1.1.1", "b": "2.2.2.2"}), c)
	if err == nil || !strings.Contains(err.Error(), `network "k3d-c" has no container IP`) {
		t.Errorf("an ambiguous network must not be guessed: %v", err)
	}
	if _, err := k3dNodeCurrentIP(mk(map[string]string{"k3d-c": ""}), c); err == nil {
		t.Error("an empty IP is not an IP")
	}
}

func TestCovC_FindK3dEntrypointPIDFailures(t *testing.T) {
	for name, ps := range map[string]string{
		"only PID 1 runs the entrypoint": "PID COMMAND\n    1 /bin/k3d-entrypoint.sh server\n",
		"non-numeric pid":                "x /bin/k3d-entrypoint.sh server\n",
		"no entrypoint":                  "   39 /bin/k3s server\n",
		"empty":                          "",
	} {
		if _, err := findK3dEntrypointPID(ps); err == nil || !strings.Contains(err.Error(), "could not find non-PID-1 k3d entrypoint shell") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCovC_K3dEntrypointPID(t *testing.T) {
	covCInstallTools(t, map[string]string{"docker": `case "$*" in
  *"exec noproc ps"*) echo "OCI runtime exec failed" >&2; exit 1;;
  *"exec nopid ps"*) echo "   39 /bin/k3s server";;
  *"ps -eo pid,args"*) cat "$COVC_PS";;
esac`})
	ps := t.TempDir() + "/ps.txt"
	covCWriteFile(t, ps, covCPSOutput)
	t.Setenv("COVC_PS", ps)
	ctx := context.Background()
	if pid, err := k3dEntrypointPID(ctx, "n1"); err != nil || pid != 7 {
		t.Fatalf("= %d, %v", pid, err)
	}
	if _, err := k3dEntrypointPID(ctx, "noproc"); err == nil || !strings.Contains(err.Error(), "list processes in noproc") {
		t.Errorf("err = %v", err)
	}
	if _, err := k3dEntrypointPID(ctx, "nopid"); err == nil || !strings.Contains(err.Error(), "nopid: could not find") {
		t.Errorf("err = %v", err)
	}
}

func TestCovC_ObserveK3sStart(t *testing.T) {
	const script = `case "$*" in
  *"ps -eo pid,args"*) cat "$COVC_PS";;
  *"exec n1 /bin/k3s"*) echo "Starting k3s"; echo "level=fatal msg=failed to find interface with specified node ip"; exec sleep 30;;
  *"exec quiet /bin/k3s"*) echo "k3s is up and running";;
  *"exec crash /bin/k3s"*) exit 3;;
  *"pidof k3s"*) exit 1;;
esac`
	ps := t.TempDir() + "/ps.txt"
	covCWriteFile(t, ps, covCPSOutput)
	t.Setenv("COVC_PS", ps)
	ctx := context.Background()

	t.Run("stops as soon as the drift failure is printed and always restores the entrypoint", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": script})
		t.Setenv("COVC_PS", ps)
		start := time.Now()
		got, err := observeK3sStart(ctx, "n1", []string{"server", "--tls-san", "x"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, k3sNodeIPDriftFailure) {
			t.Fatalf("observed output lacks the drift failure: %q", got)
		}
		if time.Since(start) > 20*time.Second {
			t.Errorf("the watcher must cut the foreground k3s short (took %s)", time.Since(start))
		}
		calls := covCCalls(t, logPath)
		order := []string{"kill -STOP 7", "exec n1 /bin/k3s server --tls-san x", "killall -TERM k3s", "kill -CONT 7"}
		last := -1
		for _, want := range order {
			i := slices.IndexFunc(calls, func(c string) bool { return strings.Contains(c, want) })
			if i < 0 || i < last {
				t.Fatalf("want %q after the previous step in %v", want, calls)
			}
			last = i
		}
	})
	t.Run("a quiet start returns what k3s said", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"docker": script})
		t.Setenv("COVC_PS", ps)
		got, err := observeK3sStart(ctx, "quiet", []string{"server"})
		if err != nil || got != "k3s is up and running" {
			t.Fatalf("= %q, %v", got, err)
		}
	})
	t.Run("a k3s that dies silently is an error", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"docker": script})
		t.Setenv("COVC_PS", ps)
		if _, err := observeK3sStart(ctx, "crash", []string{"server"}); err == nil || !strings.Contains(err.Error(), "docker exec crash /bin/k3s") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("non-server args are refused before touching the node", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": script})
		if _, err := observeK3sStart(ctx, "n1", []string{"agent"}); err == nil || !strings.Contains(err.Error(), "unexpected k3d server command args") {
			t.Fatalf("err = %v", err)
		}
		if len(covCCalls(t, logPath)) != 0 {
			t.Error("docker was called for refused args")
		}
	})
	t.Run("pausing the entrypoint can fail", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"docker": `case "$*" in *"ps -eo"*) cat "$COVC_PS";; *"kill -STOP"*) echo "operation not permitted" >&2; exit 1;; esac`})
		t.Setenv("COVC_PS", ps)
		if _, err := observeK3sStart(ctx, "n1", []string{"server"}); err == nil || !strings.Contains(err.Error(), "pause k3d entrypoint in n1") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCovC_WaitK3sStopped(t *testing.T) {
	covCInstallTools(t, map[string]string{"docker": `exit 1`})
	done := make(chan struct{})
	go func() { waitK3sStopped("n1"); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("waitK3sStopped must return once pidof finds no k3s")
	}
}

func covCRepairDocker(extra string) string {
	return `case "$*" in
  *"ps -eo pid,args"*) cat "$COVC_PS";;
  *"exec -d n1 /bin/k3s"*) ` + extra + `;;
  *"container inspect k3d-c-serverlb"*) exit 0;;
esac`
}

func TestCovC_RepairK3sNodeIPDrift(t *testing.T) {
	ps := t.TempDir() + "/ps.txt"
	covCWriteFile(t, ps, covCPSOutput)
	origAttempt, origTimeout, origPoll := nodeReadyAttemptFn, k3sNodeIPRepairTimeout, k3sNodeIPRepairPollInterval
	t.Cleanup(func() {
		nodeReadyAttemptFn, k3sNodeIPRepairTimeout, k3sNodeIPRepairPollInterval = origAttempt, origTimeout, origPoll
	})
	k3sNodeIPRepairPollInterval = time.Millisecond
	k3sNodeIPRepairTimeout = 2 * time.Second
	c := ClusterEntity{Name: "c"}
	args := []string{"server", "--disable-network-policy=false", "--tls-san", "x"}
	ctx := context.Background()

	t.Run("full repair: pause, temporary k3s with network policy off, wait for the IP, resume, restart", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": covCRepairDocker(`exit 0`), "kubectl": `echo 172.20.0.9`})
		t.Setenv("COVC_PS", ps)
		var ready []string
		nodeReadyAttemptFn = func(_ context.Context, kctx string, _ time.Duration) (string, error) {
			ready = append(ready, kctx)
			return "", nil
		}
		var err error
		out := captureStdout(t, func() { err = repairK3sNodeIPDrift(ctx, c, "n1", "172.20.0.9", args) })
		if err != nil {
			t.Fatalf("repair: %v", err)
		}
		calls := covCCalls(t, logPath)
		wantInOrder := []string{
			"docker exec n1 kill -STOP 7",
			"docker exec -d n1 /bin/k3s server --tls-san x --disable-network-policy",
			"kubectl --request-timeout=2s --context k3d-c get node n1",
			"docker exec n1 killall -TERM k3s",
			"docker exec n1 kill -CONT 7",
			"docker restart n1",
			"docker restart k3d-c-serverlb",
		}
		last := -1
		for _, want := range wantInOrder {
			i := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, want) })
			if i < 0 || i < last {
				t.Fatalf("missing or out-of-order %q in %v", want, calls)
			}
			last = i
		}
		if !slices.Equal(ready, []string{"k3d-c"}) || !strings.Contains(out, "node n1 now advertises 172.20.0.9") {
			t.Errorf("ready waits %v, out %q", ready, out)
		}
	})
	t.Run("the node never reports the new IP: the entrypoint is resumed and the temporary k3s stopped", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": covCRepairDocker(`exit 0`), "kubectl": `echo 10.9.9.9`})
		t.Setenv("COVC_PS", ps)
		k3sNodeIPRepairTimeout = 30 * time.Millisecond
		defer func() { k3sNodeIPRepairTimeout = 2 * time.Second }()
		err := repairK3sNodeIPDrift(ctx, c, "n1", "172.20.0.9", args)
		if err == nil || !strings.Contains(err.Error(), "temporary k3s did not update node n1 to current IP 172.20.0.9") {
			t.Fatalf("err = %v", err)
		}
		calls := covCCalls(t, logPath)
		if !covCHasCall(calls, "docker exec n1 killall -TERM k3s") || !covCHasCall(calls, "docker exec n1 kill -CONT 7") {
			t.Errorf("cleanup must undo the pause and stop the temporary k3s even on failure: %v", calls)
		}
		if covCHasCall(calls, "docker restart n1") {
			t.Error("a node that never took the new IP must not be restarted as repaired")
		}
	})
	t.Run("the temporary k3s cannot start", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": covCRepairDocker(`exit 1`)})
		t.Setenv("COVC_PS", ps)
		err := repairK3sNodeIPDrift(ctx, c, "n1", "1.2.3.4", args)
		if err == nil || !strings.Contains(err.Error(), "start temporary network-policy-disabled k3s in n1") {
			t.Fatalf("err = %v", err)
		}
		if !covCHasCall(covCCalls(t, logPath), "kill -CONT 7") {
			t.Error("the paused entrypoint must be resumed")
		}
	})
	t.Run("non-server args are refused", func(t *testing.T) {
		if err := repairK3sNodeIPDrift(ctx, c, "n1", "1.2.3.4", []string{"agent"}); err == nil || !strings.Contains(err.Error(), "unexpected k3d server command args") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("entrypoint not found", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"docker": `echo "   39 /bin/k3s server"`})
		if err := repairK3sNodeIPDrift(ctx, c, "n1", "1.2.3.4", args); err == nil || !strings.Contains(err.Error(), "could not find non-PID-1 k3d entrypoint shell") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("pause refused", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"docker": `case "$*" in *"ps -eo"*) cat "$COVC_PS";; *"kill -STOP"*) exit 1;; esac`})
		t.Setenv("COVC_PS", ps)
		if err := repairK3sNodeIPDrift(ctx, c, "n1", "1.2.3.4", args); err == nil || !strings.Contains(err.Error(), "pause k3d entrypoint in n1") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cancellation while waiting for the IP", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `echo wrong`})
		cctx, cancel := context.WithCancel(ctx)
		go func() { time.Sleep(30 * time.Millisecond); cancel() }()
		k3sNodeIPRepairTimeout = time.Minute
		defer func() { k3sNodeIPRepairTimeout = 2 * time.Second }()
		if err := waitForK3sNodeInternalIP(cctx, "c", "n1", "1.2.3.4"); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCovC_WaitForK3sProcessEdges(t *testing.T) {
	origFn, origGrace, origPoll := containerHasK3sProcessFn, k3sProcessGrace, k3sProcessPollInterval
	t.Cleanup(func() {
		containerHasK3sProcessFn, k3sProcessGrace, k3sProcessPollInterval = origFn, origGrace, origPoll
	})
	k3sProcessPollInterval = time.Millisecond

	boom := errors.New("docker gone")
	containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return false, boom }
	if running, err := waitForK3sProcess(context.Background(), "n1"); running || !errors.Is(err, boom) {
		t.Errorf("= %v, %v; an inspection error ends the wait", running, err)
	}
	n := 0
	containerHasK3sProcessFn = func(context.Context, string) (bool, error) { n++; return n == 3, nil }
	k3sProcessGrace = time.Minute
	if running, err := waitForK3sProcess(context.Background(), "n1"); !running || err != nil || n != 3 {
		t.Errorf("k3s appearing inside the grace period counts as running: %v, %v after %d samples", running, err, n)
	}
	containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return false, nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitForK3sProcess(ctx, "n1"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestCovC_EnsureRunningK3dClusterHealthyBranches(t *testing.T) {
	origReady, origProc, origInspect, origLogs := runningClusterNodesReadyFn, containerHasK3sProcessFn, inspectDockerK3dNodeFn, dockerLogsSinceStartFn
	origObserve, origRepair, origRefresh, origAttempt := observeK3sStartFn, repairK3sNodeIPDriftFn, refreshLoadBalancerFn, nodeReadyAttemptFn
	origGrace, origBudget, origInterval, origPoll := k3sProcessGrace, nodeReadyWaitBudget, nodeReadyRetryInterval, k3sProcessPollInterval
	t.Cleanup(func() {
		k3sProcessPollInterval = origPoll
		runningClusterNodesReadyFn, containerHasK3sProcessFn, inspectDockerK3dNodeFn, dockerLogsSinceStartFn = origReady, origProc, origInspect, origLogs
		observeK3sStartFn, repairK3sNodeIPDriftFn, refreshLoadBalancerFn, nodeReadyAttemptFn = origObserve, origRepair, origRefresh, origAttempt
		k3sProcessGrace, nodeReadyWaitBudget, nodeReadyRetryInterval = origGrace, origBudget, origInterval
	})
	k3sProcessGrace, k3sProcessPollInterval = 5*time.Millisecond, time.Millisecond
	nodeReadyWaitBudget, nodeReadyRetryInterval = 30*time.Millisecond, time.Millisecond
	c := ClusterEntity{Name: "c"}
	ctx := context.Background()
	notReady := func(err error) {
		runningClusterNodesReadyFn = func(context.Context, string) (bool, error) { return false, err }
	}
	run := func(c ClusterEntity) error {
		var err error
		captureStdout(t, func() { err = ensureRunningK3dClusterHealthy(ctx, c) })
		return err
	}

	t.Run("a cancelled context is returned without diagnosing", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		notReady(errors.New("x"))
		if err := ensureRunningK3dClusterHealthy(cctx, c); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("k3s alive and an untrusted certificate refreshes the load balancer once", func(t *testing.T) {
		notReady(errAPICertificateUntrusted)
		containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return true, nil }
		refreshed := 0
		refreshLoadBalancerFn = func(context.Context, string) error { refreshed++; return nil }
		nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) { return "", nil }
		if err := run(c); err != nil || refreshed != 1 {
			t.Fatalf("err %v, refreshed %d", err, refreshed)
		}
	})
	t.Run("a refresh that fails keeps the untrusted-certificate sentinel and the remedy", func(t *testing.T) {
		refreshLoadBalancerFn = func(context.Context, string) error { return errors.New("docker restart failed") }
		err := run(c)
		if !errors.Is(err, errAPICertificateUntrusted) || !strings.Contains(err.Error(), "docker restart k3d-c-serverlb") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("still untrusted after the refresh points at the stale kubeconfig CA", func(t *testing.T) {
		refreshLoadBalancerFn = func(context.Context, string) error { return nil }
		nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) {
			return "Unable to connect to the server: tls: failed to verify certificate: x509: certificate signed by unknown authority", errors.New("exit 1")
		}
		err := run(c)
		if !errors.Is(err, errAPICertificateUntrusted) || !strings.Contains(err.Error(), "kubeconfig most likely holds the CA of an earlier cluster") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("k3s alive but the API never answers returns the last reason", func(t *testing.T) {
		notReady(errors.New("connection refused"))
		nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) {
			return "still booting", errors.New("exit 1")
		}
		err := run(c)
		if err == nil || !strings.Contains(err.Error(), "nodes not Ready within") || !strings.Contains(err.Error(), "still booting") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("k3s alive and the wait succeeds", func(t *testing.T) {
		nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) { return "", nil }
		if err := run(c); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("process lookup failure", func(t *testing.T) {
		boom := errors.New("docker down")
		containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return false, boom }
		if err := run(c); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})

	exited := func() {
		containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return false, nil }
		inspectDockerK3dNodeFn = func(context.Context, string) (dockerK3dNodeInspect, error) {
			var info dockerK3dNodeInspect
			info.Args = []string{"server"}
			info.NetworkSettings.Networks = map[string]struct {
				IPAddress string `json:"IPAddress"`
			}{"k3d-c": {IPAddress: "172.20.0.5"}}
			return info, nil
		}
	}
	t.Run("k3s gone and the logs name the drift: single-server cluster is repaired", func(t *testing.T) {
		exited()
		dockerLogsSinceStartFn = func(context.Context, string, string) (string, error) { return k3sNodeIPDriftFailure, nil }
		var gotIP string
		repairK3sNodeIPDriftFn = func(_ context.Context, _ ClusterEntity, _, ip string, _ []string) error { gotIP = ip; return nil }
		if err := run(c); err != nil || gotIP != "172.20.0.5" {
			t.Fatalf("err %v ip %q", err, gotIP)
		}
	})
	t.Run("drift on a multi-node cluster is detected but not auto-repaired", func(t *testing.T) {
		err := run(ClusterEntity{Name: "c", Servers: 3, Agents: 1})
		if err == nil || !strings.Contains(err.Error(), "limited to single-server clusters without agents (servers=3 agents=1)") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("drift with no resolvable IP", func(t *testing.T) {
		inspectDockerK3dNodeFn = func(context.Context, string) (dockerK3dNodeInspect, error) {
			return dockerK3dNodeInspect{Args: []string{"server"}}, nil
		}
		if err := run(c); err == nil || !strings.Contains(err.Error(), "resolve current IP for k3d-c-server-0") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("logs say nothing and the foreground start cannot be observed", func(t *testing.T) {
		exited()
		dockerLogsSinceStartFn = func(context.Context, string, string) (string, error) { return "", nil }
		observeK3sStartFn = func(context.Context, string, []string) (string, error) { return "", errors.New("pause refused") }
		notReady(errors.New("EOF"))
		err := run(c)
		if err == nil || !strings.Contains(err.Error(), "a foreground start could not be observed: pause refused") || !strings.Contains(err.Error(), "The Kubernetes API reported: EOF") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unknown failure shows the foreground output instead of guessing", func(t *testing.T) {
		observeK3sStartFn = func(context.Context, string, []string) (string, error) { return "level=fatal something else", nil }
		err := run(c)
		if err == nil || !strings.Contains(err.Error(), "neither its current-start logs nor a foreground start showed the known Docker node-IP drift failure") || !strings.Contains(err.Error(), "level=fatal something else") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("inspect and log read failures are returned", func(t *testing.T) {
		boom := errors.New("inspect failed")
		inspectDockerK3dNodeFn = func(context.Context, string) (dockerK3dNodeInspect, error) { return dockerK3dNodeInspect{}, boom }
		if err := run(c); !errors.Is(err, boom) {
			t.Fatalf("inspect err = %v", err)
		}
		exited()
		dockerLogsSinceStartFn = func(context.Context, string, string) (string, error) { return "", boom }
		if err := run(c); !errors.Is(err, boom) {
			t.Fatalf("logs err = %v", err)
		}
	})
	t.Run("k3s exits during the wait and the probe error is not the sentinel: returned as is", func(t *testing.T) {
		boom := errors.New("api timeout")
		notReady(boom)
		containerHasK3sProcessFn = func(context.Context, string) (bool, error) { return true, nil }
		nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) { return "", errors.New("exit 1") }
		// the wait itself times out with its own "nodes not Ready" error
		if err := run(c); err == nil || !strings.Contains(err.Error(), "nodes not Ready within") {
			t.Fatalf("err = %v", err)
		}
	})
}
