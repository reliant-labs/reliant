package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// covCInstallTools puts fake POSIX-sh executables on PATH. Each records its argv
// ("<tool> <args>") to the file named by $COVC_LOG before running body, so a test
// asserts both what forge ASKED the tool and what it did with the answer.
// It returns the log path.
func covCInstallTools(t *testing.T, tools map[string]string) string {
	t.Helper()
	requirePOSIXFake(t, "docker/kubectl/k3d")
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls.log")
	for name, body := range tools {
		script := "#!/bin/sh\nprintf '%s\\n' \"" + name + " $*\" >> \"$COVC_LOG\"\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("COVC_LOG", logPath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// covCCalls returns the recorded fake-tool invocations in order.
func covCCalls(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func covCHasCall(calls []string, substr string) bool {
	return slices.ContainsFunc(calls, func(c string) bool { return strings.Contains(c, substr) })
}

func TestCovC_ClusterCreateFlags(t *testing.T) {
	tests := []struct {
		name string
		c    ClusterEntity
		want []string
		has  []string
	}{
		{
			name: "defaults to one server and carries only the issuer flags",
			c:    ClusterEntity{Name: "cp"},
			want: []string{"--servers", "1"},
		},
		{
			name: "every declared field projects onto its flag",
			c: ClusterEntity{
				Name: "wl", Image: "rancher/k3s:v1", Servers: 3, Agents: 2,
				Network: "k3d-cp", APIPort: 6444, ClusterCIDR: "10.52.0.0/16", ServiceCIDR: "10.53.0.0/16",
			},
			want: []string{"--image", "rancher/k3s:v1", "--servers", "3", "--agents", "2",
				"--network", "k3d-cp", "--api-port", "0.0.0.0:6444"},
			has: []string{"--cluster-cidr=10.52.0.0/16@server:*", "--service-cidr=10.53.0.0/16@server:*"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clusterCreateFlags(tc.c)
			if !slices.Equal(got[:len(tc.want)], tc.want) {
				t.Fatalf("flag prefix = %v; want %v", got[:len(tc.want)], tc.want)
			}
			for _, h := range tc.has {
				if !slices.Contains(got, h) {
					t.Errorf("flags %v missing %q", got, h)
				}
			}
			if issuer := k3sIssuerFlags(); !slices.Equal(got[len(got)-len(issuer):], issuer) {
				t.Errorf("flags %v must end with the service-account issuer flags %v", got, issuer)
			}
		})
	}
}

func TestCovC_ContainerStartedAt(t *testing.T) {
	covCInstallTools(t, map[string]string{"docker": `case "$*" in
  *missing*) echo "Error: No such object: missing" >&2; exit 1;;
  *gone*) echo "Error: No such container: gone" >&2; exit 1;;
  *broken*) echo "daemon exploded" >&2; exit 1;;
  *badtime*) echo "yesterday";;
  *) echo "2026-11-20T10:00:00.5Z";;
esac`})
	ctx := context.Background()

	got, err := containerStartedAt(ctx, "k3d-x-server-0")
	if err != nil || !got.Equal(time.Date(2026, 11, 20, 10, 0, 0, 500_000_000, time.UTC)) {
		t.Fatalf("started = %v, %v; want the parsed RFC3339Nano instant", got, err)
	}
	for _, name := range []string{"missing", "gone"} {
		got, err := containerStartedAt(ctx, name)
		if err != nil || !got.IsZero() {
			t.Errorf("%s: = %v, %v; an absent container is a zero time, not an error", name, got, err)
		}
	}
	if _, err := containerStartedAt(ctx, "broken"); err == nil || !strings.Contains(err.Error(), "daemon exploded") {
		t.Errorf("a docker failure must surface its output; got %v", err)
	}
	if _, err := containerStartedAt(ctx, "badtime"); err == nil || !strings.Contains(err.Error(), "parse start time for badtime") {
		t.Errorf("an unparseable time must name the container; got %v", err)
	}
}

func TestCovC_EnsureClusterLoadBalancerFresh(t *testing.T) {
	// "absent" makes docker answer like a missing container for that name.
	script := `case "$*" in
  *"inspect k3d-c-serverlb --format"*) [ "$COVC_LB" = absent ] && { echo "Error: No such object: k3d-c-serverlb" >&2; exit 1; }; echo "$COVC_LB";;
  *"inspect k3d-c-server-0 --format"*) echo "$COVC_NODE";;
esac`
	tests := []struct {
		name        string
		lb, node    string
		wantRestart bool
	}{
		{"node started after the load balancer", "2026-11-20T10:00:00Z", "2026-11-20T10:05:00Z", true},
		{"load balancer is the newer container", "2026-11-20T10:05:00Z", "2026-11-20T10:00:00Z", false},
		{"load balancer absent", "absent", "2026-11-20T10:00:00Z", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logPath := covCInstallTools(t, map[string]string{"docker": script})
			t.Setenv("COVC_LB", tc.lb)
			t.Setenv("COVC_NODE", tc.node)
			var err error
			out := captureStdout(t, func() { err = ensureClusterLoadBalancerFresh(context.Background(), "c") })
			if err != nil {
				t.Fatalf("ensureClusterLoadBalancerFresh: %v", err)
			}
			restarted := covCHasCall(covCCalls(t, logPath), "docker restart k3d-c-serverlb")
			if restarted != tc.wantRestart {
				t.Fatalf("restarted = %v; want %v (calls %v)", restarted, tc.wantRestart, covCCalls(t, logPath))
			}
			if tc.wantRestart && !strings.Contains(out, "5m0s after k3d-c-serverlb") {
				t.Errorf("the operator is told how much later the node started; got %q", out)
			}
		})
	}
}

func TestCovC_RefreshK3dLoadBalancer(t *testing.T) {
	t.Run("missing load balancer has nothing to refresh", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": `echo "Error: No such container: k3d-c-serverlb" >&2; exit 1`})
		if err := refreshK3dLoadBalancer(context.Background(), "c"); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		if covCHasCall(covCCalls(t, logPath), "restart") {
			t.Fatal("restarted a container that does not exist")
		}
	})
	t.Run("other inspect failure is surfaced", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"docker": `echo "permission denied" >&2; exit 1`})
		err := refreshK3dLoadBalancer(context.Background(), "c")
		if err == nil || !strings.Contains(err.Error(), "inspect k3d-c-serverlb") {
			t.Fatalf("err = %v; want an inspect failure naming the load balancer", err)
		}
	})
	t.Run("existing load balancer is restarted", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": `exit 0`})
		out := captureStdout(t, func() {
			if err := refreshK3dLoadBalancer(context.Background(), "c"); err != nil {
				t.Errorf("refresh: %v", err)
			}
		})
		if !covCHasCall(covCCalls(t, logPath), "docker restart k3d-c-serverlb") || !strings.Contains(out, "restarting k3d-c-serverlb") {
			t.Fatalf("calls %v, out %q", covCCalls(t, logPath), out)
		}
	})
}

func TestCovC_NodeFileHelpers(t *testing.T) {
	logPath := covCInstallTools(t, map[string]string{"docker": `case "$*" in
  *"exec bad "*) echo "boom" >&2; exit 3;;
  *"exec -i"*) cat > "$COVC_LOG.stdin";;
  *" cat /etc/hosts"*) echo "127.0.0.1 localhost";;
esac`})
	ctx := context.Background()

	got, err := readNodeFile(ctx, "n1", "/etc/hosts")
	if err != nil || string(got) != "127.0.0.1 localhost\n" {
		t.Fatalf("readNodeFile = %q, %v", got, err)
	}
	if _, err := readNodeFile(ctx, "bad", "/etc/hosts"); err == nil || !strings.Contains(err.Error(), "docker exec bad cat /etc/hosts") {
		t.Errorf("readNodeFile on a failing node must name the command; got %v", err)
	}
	if err := writeNodeFile(ctx, "n1", "/etc/x", []byte("payload")); err != nil {
		t.Fatalf("writeNodeFile: %v", err)
	}
	if b, _ := os.ReadFile(logPath + ".stdin"); string(b) != "payload" {
		t.Errorf("writeNodeFile stdin = %q; want the content streamed through docker exec -i", b)
	}
	if !covCHasCall(covCCalls(t, logPath), "docker exec -i n1 sh -c cat > /etc/x") {
		t.Errorf("calls %v", covCCalls(t, logPath))
	}
	if err := execNodeShell(ctx, "n1", "true"); err != nil {
		t.Errorf("execNodeShell: %v", err)
	}
	if err := execNodeShell(ctx, "bad", "true"); err == nil {
		t.Error("execNodeShell must report a non-zero exit")
	}
	if err := dockerRestart(ctx, "n1"); err != nil || !covCHasCall(covCCalls(t, logPath), "docker restart n1") {
		t.Errorf("dockerRestart: %v, calls %v", err, covCCalls(t, logPath))
	}
}

func TestCovC_ResolveDockerHostGatewayIP(t *testing.T) {
	script := `case "$*" in
  *"{{.Config.Image}}"*) [ "$COVC_MODE" = noimage ] && exit 1; [ "$COVC_MODE" = emptyimage ] && exit 0; echo rancher/k3s:v1;;
  run*) case "$COVC_MODE" in
          runfail) echo "no such image" >&2; exit 1;;
          junk) echo "not-an-ip host.k3d.internal";;
          short) echo "172.17.0.1";;
          *) echo "172.17.0.1 host.k3d.internal";;
        esac;;
esac`
	tests := []struct {
		mode    string
		want    string
		wantErr string
	}{
		{"ok", "172.17.0.1", ""},
		{"noimage", "", "inspect image for k3d-c-server-0"},
		{"emptyimage", "", "returned an empty image"},
		{"runfail", "", "resolve Docker host-gateway using rancher/k3s:v1"},
		{"junk", "", "invalid host-gateway record"},
		{"short", "", "invalid host-gateway record"},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			logPath := covCInstallTools(t, map[string]string{"docker": script})
			t.Setenv("COVC_MODE", tc.mode)
			got, err := resolveDockerHostGatewayIP(context.Background(), "k3d-c-server-0")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v; want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("= %q, %v; want %q", got, err, tc.want)
			}
			// The lookup must never pull an image or attach a network: it runs on a dev laptop.
			if !covCHasCall(covCCalls(t, logPath), "--pull=never --network none --add-host host.k3d.internal:host-gateway") {
				t.Errorf("host-gateway probe flags missing: %v", covCCalls(t, logPath))
			}
		})
	}
}

const covCDockerForGatewayDNS = `case "$*" in
  *"{{.Config.Image}}"*) echo rancher/k3s:v1;;
  run*) echo "172.17.0.1 host.k3d.internal";;
  *" cat /etc/hosts"*) printf '%s\n' "$COVC_NODE_HOSTS";;
esac`

const covCKubectlCoreDNS = `case "$*" in
  *"get configmap coredns"*) printf '%s' "$COVC_COREDNS";;
  *"rollout status"*) [ "$COVC_ROLLOUT" = fail ] && { echo "timed out" >&2; exit 1; }; true;;
esac`

func TestCovC_EnsureClusterHostGatewayDNS(t *testing.T) {
	t.Run("stale node and CoreDNS are repaired and CoreDNS bounced", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": covCDockerForGatewayDNS, "kubectl": covCKubectlCoreDNS})
		t.Setenv("COVC_NODE_HOSTS", "10.0.0.9 host.k3d.internal")
		t.Setenv("COVC_COREDNS", "10.0.0.9 host.k3d.internal\n10.0.0.2 other.local\n")
		var err error
		out := captureStdout(t, func() { err = ensureClusterHostGatewayDNS(context.Background(), "c") })
		if err != nil {
			t.Fatalf("ensureClusterHostGatewayDNS: %v", err)
		}
		calls := covCCalls(t, logPath)
		for _, want := range []string{
			"docker exec k3d-c-server-0 sh -c", // /etc/hosts upsert, with the fresh IP
			"172.17.0.1 host.k3d.internal",
			`kubectl --context k3d-c patch configmap coredns -n kube-system --type merge -p {"data":{"NodeHosts":"10.0.0.2 other.local\n172.17.0.1 host.k3d.internal\n"}}`,
			"kubectl --context k3d-c rollout restart deployment/coredns -n kube-system",
			"kubectl --context k3d-c rollout status deployment/coredns -n kube-system --timeout=60s",
		} {
			if !covCHasCall(calls, want) {
				t.Errorf("missing call containing %q in %v", want, calls)
			}
		}
		if !strings.Contains(out, "restoring host.k3d.internal=172.17.0.1") {
			t.Errorf("out = %q", out)
		}
	})
	t.Run("already current is a no-op", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": covCDockerForGatewayDNS, "kubectl": covCKubectlCoreDNS})
		t.Setenv("COVC_NODE_HOSTS", "172.17.0.1 host.k3d.internal")
		t.Setenv("COVC_COREDNS", "172.17.0.1 host.k3d.internal\n")
		var err error
		out := captureStdout(t, func() { err = ensureClusterHostGatewayDNS(context.Background(), "c") })
		if err != nil || !strings.Contains(out, "no-op") {
			t.Fatalf("err %v out %q; want a quiet no-op", err, out)
		}
		calls := covCCalls(t, logPath)
		if covCHasCall(calls, "sh -c") || covCHasCall(calls, "patch") || covCHasCall(calls, "rollout") {
			t.Fatalf("a warm run mutated state: %v", calls)
		}
	})
	t.Run("rollout that never completes is an error", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"docker": covCDockerForGatewayDNS, "kubectl": covCKubectlCoreDNS})
		t.Setenv("COVC_NODE_HOSTS", "172.17.0.1 host.k3d.internal")
		t.Setenv("COVC_COREDNS", "")
		t.Setenv("COVC_ROLLOUT", "fail")
		var err error
		captureStdout(t, func() { err = ensureClusterHostGatewayDNS(context.Background(), "c") })
		if err == nil || !strings.Contains(err.Error(), "wait for k3d-c CoreDNS rollout") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unreadable node hosts file", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"docker": `case "$*" in
  *"{{.Config.Image}}"*) echo img;;
  run*) echo "172.17.0.1 host.k3d.internal";;
  *"cat /etc/hosts"*) exit 1;;
esac`})
		err := ensureClusterHostGatewayDNS(context.Background(), "c")
		if err == nil || !strings.Contains(err.Error(), "read k3d-c-server-0 /etc/hosts") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("CoreDNS unreadable", func(t *testing.T) {
		covCInstallTools(t, map[string]string{
			"docker":  covCDockerForGatewayDNS,
			"kubectl": `echo "connection refused" >&2; exit 1`,
		})
		t.Setenv("COVC_NODE_HOSTS", "172.17.0.1 host.k3d.internal")
		err := ensureClusterHostGatewayDNS(context.Background(), "c")
		if err == nil || !strings.Contains(err.Error(), "kubectl read k3d-c kube-system/coredns") || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("err = %v; want the kubectl detail preserved", err)
		}
	})
}

func TestCovC_SecondaryClusterNodeSetup(t *testing.T) {
	const ownerHosts = "172.17.0.1 host.k3d.internal\n"
	t.Run("copies the owner's alias into the secondary and clamps MSS", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": `exit 0`, "kubectl": `case "$*" in
  *"--context k3d-cp get configmap"*) printf '%s' "$COVC_OWNER";;
  *"--context k3d-wl get configmap"*) printf '%s' "$COVC_SEC";;
esac`})
		t.Setenv("COVC_OWNER", ownerHosts)
		t.Setenv("COVC_SEC", "10.1.1.1 something\n")
		var err error
		captureStdout(t, func() {
			err = setupSecondaryClusterNode(context.Background(), ClusterEntity{Name: "wl", Network: "k3d-cp", RegistryInherit: true})
		})
		if err != nil {
			t.Fatalf("setupSecondaryClusterNode: %v", err)
		}
		calls := covCCalls(t, logPath)
		for _, want := range []string{
			`grep -q "host.k3d.internal" /etc/hosts || echo "172.17.0.1 host.k3d.internal" >> /etc/hosts`,
			`"data":{"NodeHosts":"10.1.1.1 something\n172.17.0.1 host.k3d.internal"}`,
			"rollout restart deployment/coredns",
			"iptables -t mangle -C FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1360",
		} {
			if !covCHasCall(calls, want) {
				t.Errorf("missing %q in %v", want, calls)
			}
		}
	})
	t.Run("owner without the alias is skipped, secondary left alone", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": `exit 0`, "kubectl": `printf '%s' "10.0.0.1 unrelated"`})
		var err error
		out := captureStdout(t, func() {
			err = ensureHostGatewayDNS(context.Background(), ClusterEntity{Name: "wl"}, "cp", "k3d-wl-server-0")
		})
		if err != nil || !strings.Contains(out, `owner cluster "cp" has no host.k3d.internal alias`) {
			t.Fatalf("err %v out %q", err, out)
		}
		if covCHasCall(covCCalls(t, logPath), "docker") {
			t.Error("touched the secondary node although the owner has no alias to copy")
		}
	})
	t.Run("secondary that already resolves the alias is a no-op", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"docker": `exit 0`, "kubectl": `printf '%s' "$COVC_OWNER"`})
		t.Setenv("COVC_OWNER", ownerHosts)
		var err error
		out := captureStdout(t, func() {
			err = ensureHostGatewayDNS(context.Background(), ClusterEntity{Name: "wl"}, "cp", "k3d-wl-server-0")
		})
		if err != nil || !strings.Contains(out, "already resolves host.k3d.internal") {
			t.Fatalf("err %v out %q", err, out)
		}
		if covCHasCall(covCCalls(t, logPath), "patch") {
			t.Error("patched CoreDNS although the alias was already present")
		}
	})
	t.Run("a network that names no owner is refused", func(t *testing.T) {
		err := setupSecondaryClusterNode(context.Background(), ClusterEntity{Name: "wl", Network: "k3d-"})
		if err == nil || !strings.Contains(err.Error(), "cannot derive owner cluster") {
			t.Fatalf("err = %v", err)
		}
		err = inheritRegistryMirror(context.Background(), ClusterEntity{Name: "wl", Network: "k3d-"})
		if err == nil || !strings.Contains(err.Error(), "cannot derive owner cluster") {
			t.Fatalf("inheritRegistryMirror err = %v", err)
		}
	})
	t.Run("owner CoreDNS unreadable names the owner context", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `echo "no route" >&2; exit 1`})
		err := ensureHostGatewayDNS(context.Background(), ClusterEntity{Name: "wl"}, "cp", "k3d-wl-server-0")
		if err == nil || !strings.Contains(err.Error(), "read owner k3d-cp CoreDNS NodeHosts") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCovC_KubectlWaitNodesReady(t *testing.T) {
	origHB, origStart := nodeReadyHeartbeatsFn, nodeContainerStartedAtFn
	t.Cleanup(func() { nodeReadyHeartbeatsFn, nodeContainerStartedAtFn = origHB, origStart })
	started := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	nodeContainerStartedAtFn = func(context.Context, string) (time.Time, error) { return started, nil }

	t.Run("ready and heartbeat newer than the boot", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"kubectl": `echo "node/n1 condition met"`})
		nodeReadyHeartbeatsFn = func(context.Context, string) (map[string]time.Time, error) {
			return map[string]time.Time{"n1": started.Add(time.Minute)}, nil
		}
		out, err := kubectlWaitNodesReady(context.Background(), "k3d-c", 5*time.Second)
		if err != nil || !strings.Contains(out, "condition met") {
			t.Fatalf("= %q, %v", out, err)
		}
		if !covCHasCall(covCCalls(t, logPath), "--context k3d-c wait --for=condition=Ready nodes --all --timeout=5s") {
			t.Errorf("calls %v", covCCalls(t, logPath))
		}
	})
	t.Run("Ready persisted from the previous boot is rejected", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `echo "node/n1 condition met"`})
		nodeReadyHeartbeatsFn = func(context.Context, string) (map[string]time.Time, error) {
			return map[string]time.Time{"n1": started.Add(-time.Hour)}, nil
		}
		out, err := kubectlWaitNodesReady(context.Background(), "k3d-c", 5*time.Second)
		if err == nil || !strings.Contains(err.Error(), "node(s) n1 read Ready only from status persisted before") || out != err.Error() {
			t.Fatalf("= %q, %v", out, err)
		}
	})
	t.Run("kubectl failure returns its output with the error", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `echo "timed out waiting for the condition"; exit 1`})
		out, err := kubectlWaitNodesReady(context.Background(), "k3d-c", 5*time.Second)
		if err == nil || !strings.Contains(out, "timed out waiting") {
			t.Fatalf("= %q, %v", out, err)
		}
	})
}

func TestCovC_ClusterNodesReadyOnce(t *testing.T) {
	origHB, origStart, origTimeout := nodeReadyHeartbeatsFn, nodeContainerStartedAtFn, nodeReadyProbeTimeout
	t.Cleanup(func() {
		nodeReadyHeartbeatsFn, nodeContainerStartedAtFn, nodeReadyProbeTimeout = origHB, origStart, origTimeout
	})
	started := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	nodeContainerStartedAtFn = func(context.Context, string) (time.Time, error) { return started, nil }
	fresh := func(context.Context, string) (map[string]time.Time, error) {
		return map[string]time.Time{"n1": started.Add(time.Minute)}, nil
	}

	t.Run("healthy", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `exit 0`})
		nodeReadyHeartbeatsFn = fresh
		if ok, err := clusterNodesReadyOnce(context.Background(), "c"); !ok || err != nil {
			t.Fatalf("= %v, %v", ok, err)
		}
	})
	t.Run("stale Ready is not ready and says why", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `exit 0`})
		nodeReadyHeartbeatsFn = func(context.Context, string) (map[string]time.Time, error) {
			return map[string]time.Time{"n1": started.Add(-time.Minute)}, nil
		}
		ok, err := clusterNodesReadyOnce(context.Background(), "c")
		if ok || err == nil || !strings.Contains(err.Error(), "this boot's kubelet has not reported yet") {
			t.Fatalf("= %v, %v", ok, err)
		}
	})
	t.Run("kubectl error carries its stderr", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `echo 'Unable to connect to the server: EOF' >&2; exit 1`})
		ok, err := clusterNodesReadyOnce(context.Background(), "c")
		if ok || err == nil || !strings.Contains(err.Error(), "Unable to connect to the server: EOF") {
			t.Fatalf("= %v, %v", ok, err)
		}
	})
	t.Run("a kubectl that outlives our budget is reported as not answering", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `exec sleep 30`})
		nodeReadyProbeTimeout = 100 * time.Millisecond
		ok, err := clusterNodesReadyOnce(context.Background(), "c")
		if ok || err == nil || !strings.Contains(err.Error(), "kubectl did not answer within 100ms") {
			t.Fatalf("= %v, %v", ok, err)
		}
	})
}

func TestCovC_KubectlNodeReadyHeartbeats(t *testing.T) {
	t.Run("parses a heartbeat per node and zero for a node without one", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `printf 'n1\t2026-11-20T10:00:00Z\nn2\t\n'`})
		got, err := kubectlNodeReadyHeartbeats(context.Background(), "k3d-c")
		if err != nil {
			t.Fatal(err)
		}
		if !got["n1"].Equal(time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)) || !got["n2"].IsZero() || len(got) != 2 {
			t.Fatalf("heartbeats = %v", got)
		}
	})
	t.Run("kubectl failure", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"kubectl": `echo "forbidden" >&2; exit 1`})
		_, err := kubectlNodeReadyHeartbeats(context.Background(), "k3d-c")
		if err == nil || !strings.Contains(err.Error(), "read node heartbeats in k3d-c") || !strings.Contains(err.Error(), "forbidden") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("garbled timestamp", func(t *testing.T) {
		if _, err := parseNodeReadyHeartbeats("n1\tlast-tuesday\n"); err == nil || !strings.Contains(err.Error(), `parse Ready heartbeat "last-tuesday" for node n1`) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCovC_VerifyNodesReadyThisBootPropagatesErrors(t *testing.T) {
	origHB, origStart := nodeReadyHeartbeatsFn, nodeContainerStartedAtFn
	t.Cleanup(func() { nodeReadyHeartbeatsFn, nodeContainerStartedAtFn = origHB, origStart })
	boom := errors.New("api gone")
	nodeReadyHeartbeatsFn = func(context.Context, string) (map[string]time.Time, error) { return nil, boom }
	if err := verifyNodesReadyThisBoot(context.Background(), "k"); !errors.Is(err, boom) {
		t.Fatalf("err = %v; want the heartbeat error", err)
	}
	nodeReadyHeartbeatsFn = func(context.Context, string) (map[string]time.Time, error) {
		return map[string]time.Time{"n1": {}}, nil
	}
	nodeContainerStartedAtFn = func(context.Context, string) (time.Time, error) { return time.Time{}, boom }
	if err := verifyNodesReadyThisBoot(context.Background(), "k"); !errors.Is(err, boom) {
		t.Fatalf("err = %v; want the start-time error", err)
	}
}

func TestCovC_CheckClusterPortDrift(t *testing.T) {
	origRender := renderKCLRawHook
	t.Cleanup(func() { renderKCLRawHook = origRender })
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`{"project":"p","env":"dev","gateways":[{"name":"gw","listeners":[
			{"name":"http","port":28080,"protocol":"HTTP"},{"name":"grpc","port":28443,"protocol":"HTTPS"},{"name":"off","port":0,"protocol":"HTTP"}]}]}`), nil
	}
	t.Run("ports derived from the render drive the in-place edit", func(t *testing.T) {
		calls := stubPortDrift(t, map[int]bool{28080: true}, nil, true)
		var err error
		out := captureStdout(t, func() {
			err = checkClusterPortDrift(context.Background(), ClusterEntity{Name: "cp"}, nil, "proj", "dev")
		})
		if err != nil {
			t.Fatalf("checkClusterPortDrift: %v", err)
		}
		if len(*calls) != 1 || !slices.Equal((*calls)[0], []int{28443}) {
			t.Fatalf("edit calls = %v; want exactly the missing listener port", *calls)
		}
		if !strings.Contains(out, "missing host-port mapping(s) 28443") {
			t.Errorf("out = %q", out)
		}
	})
	t.Run("a render failure only warns", func(t *testing.T) {
		renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) {
			return nil, errors.New("kcl exploded")
		}
		calls := stubPortDrift(t, map[int]bool{}, nil, true)
		var err error
		out := captureStdout(t, func() {
			err = checkClusterPortDrift(context.Background(), ClusterEntity{Name: "cp"}, nil, "proj", "dev")
		})
		if err != nil || len(*calls) != 0 || !strings.Contains(out, "could not render Gateway listeners") {
			t.Fatalf("err %v calls %v out %q", err, *calls, out)
		}
	})
	t.Run("a drift read failure only warns", func(t *testing.T) {
		orig := runningClusterHostPortsFn
		t.Cleanup(func() { runningClusterHostPortsFn = orig })
		runningClusterHostPortsFn = func(context.Context, string) (map[int]bool, error) { return nil, errors.New("docker down") }
		var err error
		out := captureStdout(t, func() {
			err = reconcileClusterPortDrift(context.Background(), ClusterEntity{Name: "cp"}, nil, "dev", map[int]bool{1: true})
		})
		if err != nil || !strings.Contains(out, "could not verify host-port mappings") {
			t.Fatalf("err %v out %q", err, out)
		}
	})
}

func TestCovC_AddClusterHostPorts(t *testing.T) {
	logPath := covCInstallTools(t, map[string]string{"k3d": `case "$*" in *fail*) echo "edit refused" >&2; exit 1;; esac`})
	if err := addClusterHostPorts(context.Background(), "cp", []int{28080, 28443}); err != nil {
		t.Fatal(err)
	}
	want := "k3d cluster edit cp --port-add 28080:28080@loadbalancer --port-add 28443:28443@loadbalancer"
	if calls := covCCalls(t, logPath); len(calls) != 1 || calls[0] != want {
		t.Fatalf("calls = %v; want one edit carrying every port: %q", calls, want)
	}
	err := addClusterHostPorts(context.Background(), "fail", []int{1})
	if err == nil || !strings.Contains(err.Error(), "edit refused") {
		t.Fatalf("err = %v; want k3d's output preserved", err)
	}
}

func TestCovC_RunningClusterHostPorts(t *testing.T) {
	covCInstallTools(t, map[string]string{"docker": `case "$*" in
  *k3d-ok-serverlb*) printf '6443/tcp -> 0.0.0.0:6443\n28080/tcp -> 0.0.0.0:28080\n\nweird\n80/tcp -> [::]:abc\n';;
  *) exit 1;;
esac`})
	got, err := runningClusterHostPorts(context.Background(), "ok")
	if err != nil || len(got) != 2 || !got[6443] || !got[28080] {
		t.Fatalf("= %v, %v; want the two parseable host ports", got, err)
	}
	// An unreadable serverlb must read as "unknown", not "everything is missing".
	got, err = runningClusterHostPorts(context.Background(), "absent")
	if err != nil || len(got) != 0 {
		t.Fatalf("= %v, %v; want an empty set", got, err)
	}
}

func TestCovC_ReconcileExistingCluster(t *testing.T) {
	withFreshLoadBalancers(t)
	orig := struct {
		healthy func(context.Context, ClusterEntity) error
		dns     func(context.Context, string) error
		setup   func(context.Context, ClusterEntity) error
		start   func(context.Context, string) error
		wait    func(context.Context, string) error
	}{ensureRunningClusterHealthyFn, ensureClusterHostGatewayDNSFn, setupSecondaryClusterNodeFn, startDeclaredClusterFn, waitDeclaredClusterReadyFn}
	t.Cleanup(func() {
		ensureRunningClusterHealthyFn, ensureClusterHostGatewayDNSFn, setupSecondaryClusterNodeFn = orig.healthy, orig.dns, orig.setup
		startDeclaredClusterFn, waitDeclaredClusterReadyFn = orig.start, orig.wait
	})
	ensureRunningClusterHealthyFn = func(context.Context, ClusterEntity) error { return nil }
	ensureClusterHostGatewayDNSFn = func(context.Context, string) error { return nil }
	setupSecondaryClusterNodeFn = func(context.Context, ClusterEntity) error { return nil }
	startDeclaredClusterFn = func(context.Context, string) error { return nil }
	waitDeclaredClusterReadyFn = func(context.Context, string) error { return nil }
	ctx := context.Background()
	c := ClusterEntity{Name: "cp"}
	boom := errors.New("boom")

	t.Run("running: load balancer refresh failure", func(t *testing.T) {
		ensureClusterLBFreshFn = func(context.Context, string) error { return boom }
		err := reconcileExistingCluster(ctx, c, k3dClusterRuntimeState{Exists: true, Running: true}, nil, "", "dev")
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), `refresh load balancer for cluster "cp"`) {
			t.Fatalf("err = %v", err)
		}
		ensureClusterLBFreshFn = func(context.Context, string) error { return nil }
	})
	t.Run("running: unhealthy", func(t *testing.T) {
		ensureRunningClusterHealthyFn = func(context.Context, ClusterEntity) error { return boom }
		defer func() { ensureRunningClusterHealthyFn = func(context.Context, ClusterEntity) error { return nil } }()
		err := reconcileExistingCluster(ctx, c, k3dClusterRuntimeState{Exists: true, Running: true}, nil, "", "dev")
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), `verify running cluster "cp"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("stopped: start, wait, then continue", func(t *testing.T) {
		var order []string
		startDeclaredClusterFn = func(context.Context, string) error { order = append(order, "start"); return nil }
		waitDeclaredClusterReadyFn = func(context.Context, string) error { order = append(order, "wait"); return nil }
		captureStdout(t, func() {
			if err := reconcileExistingCluster(ctx, c, k3dClusterRuntimeState{Exists: true}, nil, "", "dev"); err != nil {
				t.Errorf("reconcile: %v", err)
			}
		})
		if !slices.Equal(order, []string{"start", "wait"}) {
			t.Fatalf("order = %v", order)
		}
	})
	t.Run("stopped: wait failure", func(t *testing.T) {
		waitDeclaredClusterReadyFn = func(context.Context, string) error { return boom }
		var err error
		captureStdout(t, func() { err = reconcileExistingCluster(ctx, c, k3dClusterRuntimeState{Exists: true}, nil, "", "dev") })
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), `wait for restarted cluster "cp"`) {
			t.Fatalf("err = %v", err)
		}
		waitDeclaredClusterReadyFn = func(context.Context, string) error { return nil }
	})
	t.Run("stopped: load balancer refresh failure after start", func(t *testing.T) {
		ensureClusterLBFreshFn = func(context.Context, string) error { return boom }
		var err error
		captureStdout(t, func() { err = reconcileExistingCluster(ctx, c, k3dClusterRuntimeState{Exists: true}, nil, "", "dev") })
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		ensureClusterLBFreshFn = func(context.Context, string) error { return nil }
	})
	t.Run("host-gateway DNS failure", func(t *testing.T) {
		ensureClusterHostGatewayDNSFn = func(context.Context, string) error { return boom }
		var err error
		captureStdout(t, func() {
			err = reconcileExistingCluster(ctx, c, k3dClusterRuntimeState{Exists: true, Running: true}, nil, "", "dev")
		})
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "ensure host-gateway DNS") {
			t.Fatalf("err = %v", err)
		}
		ensureClusterHostGatewayDNSFn = func(context.Context, string) error { return nil }
	})
	t.Run("CIDR drift against the running cluster is an error naming the recreate command", func(t *testing.T) {
		orig := liveClusterCIDRsFn
		t.Cleanup(func() { liveClusterCIDRsFn = orig })
		liveClusterCIDRsFn = func(context.Context, string) (liveClusterCIDRs, error) {
			return liveClusterCIDRs{PodCIDRs: []string{"10.42.0.0/24"}}, nil
		}
		var err error
		captureStdout(t, func() {
			err = reconcileExistingCluster(ctx, ClusterEntity{Name: "cp", ClusterCIDR: "10.52.0.0/16"},
				k3dClusterRuntimeState{Exists: true, Running: true}, nil, "", "dev")
		})
		if err == nil || !strings.Contains(err.Error(), "k3d cluster delete cp && forge env up dev") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nested secondary gets its node setup; a setup failure is returned", func(t *testing.T) {
		sec := ClusterEntity{Name: "wl", Network: "k3d-cp", RegistryInherit: true}
		setupSecondaryClusterNodeFn = func(context.Context, ClusterEntity) error { return boom }
		var err error
		captureStdout(t, func() {
			err = reconcileExistingCluster(ctx, sec, k3dClusterRuntimeState{Exists: true, Running: true}, nil, "", "dev")
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("host-port drift is only checked for config-driven clusters with host ports", func(t *testing.T) {
		origRender := renderKCLRawHook
		t.Cleanup(func() { renderKCLRawHook = origRender })
		renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) {
			return []byte(`{"project":"p","env":"dev","gateways":[{"name":"gw","listeners":[{"name":"h","port":9,"protocol":"HTTP"}]}]}`), nil
		}
		calls := stubPortDrift(t, map[int]bool{1: true}, errors.New("edit failed"), false)
		var err error
		captureStdout(t, func() {
			err = reconcileExistingCluster(ctx, ClusterEntity{Name: "cp", Config: "deploy/k3d.yaml", HostPorts: true},
				k3dClusterRuntimeState{Exists: true, Running: true}, nil, "proj", "dev")
		})
		if err == nil || !strings.Contains(err.Error(), "adding them in place failed: edit failed") || len(*calls) != 1 {
			t.Fatalf("err %v calls %v", err, *calls)
		}
	})
}

func TestCovC_EnsureDeclaredCluster(t *testing.T) {
	withReadyNewNodes(t)
	origState, origCreate, origDNS, origConv, origReg := clusterRuntimeStateFn, createDeclaredClusterFn, ensureClusterHostGatewayDNSFn, convergeStorageFn, registryExistsFn
	t.Cleanup(func() {
		clusterRuntimeStateFn, createDeclaredClusterFn, ensureClusterHostGatewayDNSFn, convergeStorageFn, registryExistsFn = origState, origCreate, origDNS, origConv, origReg
	})
	clusterRuntimeStateFn = func(context.Context, string) (k3dClusterRuntimeState, error) { return k3dClusterRuntimeState{}, nil }
	ensureClusterHostGatewayDNSFn = func(context.Context, string) error { return nil }
	convergeStorageFn = origConv
	var createArgs []string
	createDeclaredClusterFn = func(_ context.Context, _ string, args []string) error {
		createArgs = slices.Clone(args)
		return nil
	}
	ctx := context.Background()

	t.Run("state lookup failure is returned untouched", func(t *testing.T) {
		boom := errors.New("k3d missing")
		clusterRuntimeStateFn = func(context.Context, string) (k3dClusterRuntimeState, error) { return k3dClusterRuntimeState{}, boom }
		if err := ensureDeclaredCluster(ctx, ClusterEntity{Name: "cp"}, nil, "", "dev"); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		clusterRuntimeStateFn = func(context.Context, string) (k3dClusterRuntimeState, error) { return k3dClusterRuntimeState{}, nil }
	})
	t.Run("config-driven create passes the merged config, not flags", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "k3d.yaml")
		if err := os.WriteFile(cfg, []byte("apiVersion: k3d.io/v1alpha5\nkind: Simple\nmetadata:\n  name: cp\nservers: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var err error
		captureStdout(t, func() {
			err = ensureDeclaredCluster(ctx, ClusterEntity{Name: "cp", Config: cfg, APIPort: 6443, ClusterCIDR: "10.52.0.0/16"}, nil, dir, "dev")
		})
		if err != nil {
			t.Fatalf("ensureDeclaredCluster: %v", err)
		}
		if len(createArgs) != 5 || createArgs[0] != "cluster" || createArgs[2] != "cp" || createArgs[3] != "--config" {
			t.Fatalf("create args = %v; want `cluster create cp --config <merged>`", createArgs)
		}
		if slices.Contains(createArgs, "--servers") {
			t.Errorf("a config-driven cluster must take no declared flags: %v", createArgs)
		}
	})
	t.Run("config that cannot be read is a registry error", func(t *testing.T) {
		var err error
		captureStdout(t, func() {
			err = ensureDeclaredCluster(ctx, ClusterEntity{Name: "cp", Config: filepath.Join(t.TempDir(), "absent.yaml")}, nil, "", "dev")
		})
		if err == nil || !strings.Contains(err.Error(), `ensure standalone registry for cluster "cp"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("create failure and wait failure stop the phase", func(t *testing.T) {
		boom := errors.New("create refused")
		createDeclaredClusterFn = func(context.Context, string, []string) error { return boom }
		if err := ensureDeclaredCluster(ctx, ClusterEntity{Name: "cp"}, nil, "", "dev"); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		createDeclaredClusterFn = func(context.Context, string, []string) error { return nil }
		waitDeclaredClusterReadyFn = func(context.Context, string) error { return boom }
		var err error
		captureStdout(t, func() { err = ensureDeclaredCluster(ctx, ClusterEntity{Name: "cp"}, nil, "", "dev") })
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), `wait for new cluster "cp"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("registry_inherit without a derived network is refused", func(t *testing.T) {
		createDeclaredClusterFn = func(context.Context, string, []string) error { return nil }
		waitDeclaredClusterReadyFn = func(context.Context, string) error { return nil }
		var err error
		captureStdout(t, func() {
			err = ensureDeclaredCluster(ctx, ClusterEntity{Name: "wl", RegistryInherit: true}, nil, "", "dev")
		})
		if err == nil || !strings.Contains(err.Error(), "an `owner` Cluster reference is required") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCovC_InheritRegistryMirror(t *testing.T) {
	origRead, origWrite, origRestart, origRefresh, origHealthy := readNodeFileFn, writeNodeFileFn, dockerRestartFn, refreshLoadBalancerFn, ensureRunningClusterHealthyFn
	t.Cleanup(func() {
		readNodeFileFn, writeNodeFileFn, dockerRestartFn, refreshLoadBalancerFn, ensureRunningClusterHealthyFn = origRead, origWrite, origRestart, origRefresh, origHealthy
	})
	ensureRunningClusterHealthyFn = func(context.Context, ClusterEntity) error { return nil }
	dockerRestartFn = func(context.Context, string) error { return nil }
	refreshLoadBalancerFn = func(context.Context, string) error { return nil }
	c := ClusterEntity{Name: "wl", Network: "k3d-cp", RegistryInherit: true}
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		read    func(context.Context, string, string) ([]byte, error)
		want    string
		wantSrc string
	}{
		{"owner's file is mirrored verbatim", func(_ context.Context, node, _ string) ([]byte, error) {
			return []byte("mirrors: {owner: {}}\n"), nil
		}, "mirrors: {owner: {}}\n", "k3d-cp-server-0"},
		{"unreadable owner file falls back to the default mirror", func(context.Context, string, string) ([]byte, error) {
			return nil, errors.New("no such file")
		}, fallbackRegistriesYAML, ""},
		{"blank owner file falls back to the default mirror", func(context.Context, string, string) ([]byte, error) {
			return []byte("  \n"), nil
		}, fallbackRegistriesYAML, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wrote string
			var wroteNode string
			readNodeFileFn = tc.read
			writeNodeFileFn = func(_ context.Context, node, path string, content []byte) error {
				wrote, wroteNode = string(content), node+":"+path
				return nil
			}
			captureStdout(t, func() {
				if err := inheritRegistryMirror(ctx, c); err != nil {
					t.Errorf("inheritRegistryMirror: %v", err)
				}
			})
			if wrote != tc.want || wroteNode != "k3d-wl-server-0:/etc/rancher/k3s/registries.yaml" {
				t.Fatalf("wrote %q onto %q; want %q onto the secondary node", wrote, wroteNode, tc.want)
			}
		})
	}
	boom := errors.New("boom")
	readNodeFileFn = func(context.Context, string, string) ([]byte, error) { return []byte("x: 1"), nil }
	for _, tc := range []struct {
		name    string
		arrange func()
		want    string
	}{
		{"write", func() { writeNodeFileFn = func(context.Context, string, string, []byte) error { return boom } }, "write registries.yaml onto k3d-wl-server-0"},
		{"restart", func() {
			writeNodeFileFn = func(context.Context, string, string, []byte) error { return nil }
			dockerRestartFn = func(context.Context, string) error { return boom }
		}, "restart k3d-wl-server-0"},
		{"refresh", func() {
			dockerRestartFn = func(context.Context, string) error { return nil }
			refreshLoadBalancerFn = func(context.Context, string) error { return boom }
		}, "refresh wl load balancer after node restart"},
		{"heal", func() {
			refreshLoadBalancerFn = func(context.Context, string) error { return nil }
			ensureRunningClusterHealthyFn = func(context.Context, ClusterEntity) error { return boom }
		}, "wait wl ready after reload"},
	} {
		t.Run(tc.name+" failure", func(t *testing.T) {
			tc.arrange()
			var err error
			captureStdout(t, func() { err = inheritRegistryMirror(ctx, c) })
			if !errors.Is(err, boom) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestCovC_LoadBalancerFreshnessPropagatesDockerFailure(t *testing.T) {
	// ensureClusterLoadBalancerFresh must propagate an unreadable container
	// rather than guess: a docker failure is not "no stale upstream".
	covCInstallTools(t, map[string]string{"docker": `echo "cannot connect to the Docker daemon" >&2; exit 1`})
	err := ensureClusterLoadBalancerFresh(context.Background(), "c")
	if err == nil || !strings.Contains(err.Error(), "inspect k3d-c-serverlb") {
		t.Fatalf("err = %v", err)
	}
}

// covCWriteTar writes a minimal tar archive of name->content to path.
func covCWriteTar(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func covCWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
