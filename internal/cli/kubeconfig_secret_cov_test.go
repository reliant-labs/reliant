package cli

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

// covAStubBins puts shell stubs for the named tools first on PATH. Each
// appends "<tool> <argv>" to the returned log and then runs its script, which
// may use $LOG-independent shell only. scripts maps tool -> script body.
func covAStubBins(t *testing.T, scripts map[string]string) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs")
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	for name, body := range scripts {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> '" + logPath + "'\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

const covAK3dKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: k3d-hub
  cluster:
    server: https://0.0.0.0:6445
    certificate-authority-data: ` + "Q0E=" + `
contexts:
- name: k3d-hub
  context: {cluster: k3d-hub, user: admin@k3d-hub}
current-context: k3d-hub
users:
- name: admin@k3d-hub
  user: {token: t}
`

func TestK3dKubeconfigGetReturnsTheClustersKubeconfig(t *testing.T) {
	log := covAStubBins(t, map[string]string{"k3d": "cat <<'EOF'\n" + covAK3dKubeconfig + "EOF"})
	got, err := k3dKubeconfigGet(context.Background(), "hub")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != covAK3dKubeconfig {
		t.Errorf("kubeconfig = %q", got)
	}
	if calls := covAReadLog(t, log); len(calls) != 1 || calls[0] != "k3d kubeconfig get hub" {
		t.Errorf("calls = %q", calls)
	}
}

func TestK3dKubeconfigGetNamesTheClusterOnFailure(t *testing.T) {
	covAStubBins(t, map[string]string{"k3d": "exit 1"})
	_, err := k3dKubeconfigGet(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "k3d kubeconfig get ghost") {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerInspectIP(t *testing.T) {
	tests := []struct {
		name, script, want string
	}{
		{"an address", `echo "  172.20.0.4 "`, "172.20.0.4"},
		{"a container not on the network", `echo "<no value>"`, ""},
		{"an empty answer", `echo ""`, ""},
		{"no such container", `echo "Error: No such object" >&2; exit 1`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log := covAStubBins(t, map[string]string{"docker": tc.script})
			if got := dockerInspectIP(context.Background(), "k3d-hub-serverlb", "{{fmt}}"); got != tc.want {
				t.Errorf("ip = %q, want %q", got, tc.want)
			}
			if calls := covAReadLog(t, log); len(calls) != 1 || calls[0] != "docker inspect k3d-hub-serverlb --format {{fmt}}" {
				t.Errorf("calls = %q", calls)
			}
		})
	}
}

func TestResolveInNetworkAPIServerIP(t *testing.T) {
	ctx := context.Background()

	t.Run("prefers the load balancer container", func(t *testing.T) {
		log := covAStubBins(t, map[string]string{"docker": `echo 172.20.0.2`})
		ip, container, err := resolveInNetworkAPIServerIP(ctx, "hub", "k3d-owner")
		if err != nil || ip != "172.20.0.2" || container != "k3d-hub-serverlb" {
			t.Fatalf("ip=%q container=%q err=%v", ip, container, err)
		}
		calls := covAReadLog(t, log)
		if len(calls) != 1 || !strings.Contains(calls[0], `(index .NetworkSettings.Networks "k3d-owner").IPAddress`) {
			t.Errorf("the template must read the owner network's address: %q", calls)
		}
	})
	t.Run("falls back to server-0 for a cluster created with --no-lb", func(t *testing.T) {
		log := covAStubBins(t, map[string]string{"docker": `case "$*" in *serverlb*) exit 1 ;; *) echo 172.20.0.9 ;; esac`})
		ip, container, err := resolveInNetworkAPIServerIP(ctx, "hub", "k3d-owner")
		if err != nil || ip != "172.20.0.9" || container != "k3d-hub-server-0" {
			t.Fatalf("ip=%q container=%q err=%v", ip, container, err)
		}
		if calls := covAReadLog(t, log); len(calls) != 2 {
			t.Errorf("calls = %q", calls)
		}
	})
	t.Run("neither container is on the network", func(t *testing.T) {
		covAStubBins(t, map[string]string{"docker": `exit 1`})
		_, _, err := resolveInNetworkAPIServerIP(ctx, "hub", "k3d-owner")
		if err == nil || !strings.Contains(err.Error(), `could not resolve API-server IP for cluster "hub" on network "k3d-owner"`) ||
			!strings.Contains(err.Error(), "k3d-hub-serverlb and k3d-hub-server-0") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no owner network is refused before docker is asked", func(t *testing.T) {
		log := covAStubBins(t, map[string]string{"docker": `echo 1.2.3.4`})
		_, _, err := resolveInNetworkAPIServerIP(ctx, "hub", "")
		if err == nil || !strings.Contains(err.Error(), "declare Cluster.network") {
			t.Fatalf("err = %v", err)
		}
		if calls := covAReadLog(t, log); len(calls) != 0 {
			t.Errorf("docker was called: %q", calls)
		}
	})
}

func TestRunKubectlConfigTargetsTheNamedFileAndSurfacesStderr(t *testing.T) {
	log := covAStubBins(t, map[string]string{"kubectl": `case "$*" in *rename-context*bad*) echo "error: no context exists with the name: bad" >&2; exit 1 ;; esac`})
	ctx := context.Background()
	if err := kubectlConfigRenameContext(ctx, "/tmp/kc.yaml", "k3d-hub", "hub"); err != nil {
		t.Fatal(err)
	}
	if calls := covAReadLog(t, log); len(calls) != 1 || calls[0] != "kubectl config --kubeconfig /tmp/kc.yaml rename-context k3d-hub hub" {
		t.Errorf("calls = %q: the explicit kubeconfig, never the user's default", calls)
	}
	err := kubectlConfigRenameContext(ctx, "/tmp/kc.yaml", "bad", "hub")
	if err == nil || !strings.Contains(err.Error(), "kubectl config rename-context bad hub") || !strings.Contains(err.Error(), "no context exists with the name: bad") {
		t.Fatalf("kubectl's own explanation must reach the operator: %v", err)
	}
}

func TestRewriteInNetworkKubeconfigRefusals(t *testing.T) {
	const noCA = "apiVersion: v1\nkind: Config\nclusters:\n- name: k3d-hub\n  cluster: {server: 'https://0.0.0.0:1', insecure-skip-tls-verify: true}\n"
	tests := []struct {
		name, raw, want string
	}{
		{"not a kubeconfig", "clusters: [", "parse kubeconfig"},
		{"the cluster entry is missing", covAK3dKubeconfig, `kubeconfig has no cluster entry "k3d-other"`},
		{"no CA to verify against", noCA, "carries no CA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := "k3d-hub"
			if strings.Contains(tc.want, "k3d-other") {
				entry = "k3d-other"
			}
			_, err := rewriteInNetworkKubeconfig([]byte(tc.raw), entry, "https://k3d-hub-serverlb:6443", "k3d-hub-serverlb")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("a stale insecure flag is cleared when a CA is present", func(t *testing.T) {
		raw := strings.Replace(covAK3dKubeconfig, "server: https://0.0.0.0:6445", "server: https://0.0.0.0:6445\n    insecure-skip-tls-verify: true", 1)
		out, err := rewriteInNetworkKubeconfig([]byte(raw), "k3d-hub", "https://k3d-hub-serverlb:6443", "k3d-hub-serverlb")
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := clientcmd.Load(out)
		if err != nil {
			t.Fatal(err)
		}
		if c := cfg.Clusters["k3d-hub"]; c.InsecureSkipTLSVerify || c.Server != "https://k3d-hub-serverlb:6443" {
			t.Errorf("cluster = %+v", c)
		}
	})
}

func TestMintOneKubeconfigSecretRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("no namespace anywhere", func(t *testing.T) {
		log := covAStubBins(t, map[string]string{"k3d": "exit 1"})
		err := mintOneKubeconfigSecret(ctx, KubeconfigSecretEntity{Name: "hub", TargetCluster: "hub", InCluster: "k3d-app"}, "", "")
		if err == nil || !strings.Contains(err.Error(), "no namespace") {
			t.Fatalf("err = %v", err)
		}
		if calls := covAReadLog(t, log); len(calls) != 0 {
			t.Errorf("must refuse before shelling out: %q", calls)
		}
	})
	t.Run("k3d cannot produce the kubeconfig", func(t *testing.T) {
		covAStubBins(t, map[string]string{"k3d": "exit 1"})
		err := mintOneKubeconfigSecret(ctx, KubeconfigSecretEntity{Name: "hub", TargetCluster: "hub", InCluster: "k3d-app", Namespace: "flux"}, "", "")
		if err == nil || !strings.Contains(err.Error(), "k3d kubeconfig get hub") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("in-network without a resolvable API server", func(t *testing.T) {
		covAStubBins(t, map[string]string{"k3d": "cat <<'EOF'\n" + covAK3dKubeconfig + "EOF", "docker": "exit 1"})
		k := KubeconfigSecretEntity{Name: "hub", TargetCluster: "hub", InCluster: "k3d-app", Namespace: "flux", Reachability: "in-network", ContextName: "k3d-hub"}
		err := mintOneKubeconfigSecret(ctx, k, "k3d-owner", "")
		if err == nil || !strings.Contains(err.Error(), "could not resolve API-server IP") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("in-network with a kubeconfig that has no CA", func(t *testing.T) {
		noCA := strings.Replace(covAK3dKubeconfig, "certificate-authority-data: Q0E=", "insecure-skip-tls-verify: true", 1)
		covAStubBins(t, map[string]string{"k3d": "cat <<'EOF'\n" + noCA + "EOF", "docker": "echo 172.20.0.2"})
		k := KubeconfigSecretEntity{Name: "hub", TargetCluster: "hub", InCluster: "k3d-app", Namespace: "flux", Reachability: "in-network", ContextName: "k3d-hub"}
		err := mintOneKubeconfigSecret(ctx, k, "k3d-owner", "")
		if err == nil || !strings.Contains(err.Error(), "carries no CA") {
			t.Fatalf("a mint must never fall back to skipping verification: %v", err)
		}
	})
	t.Run("renaming the context fails", func(t *testing.T) {
		covAStubBins(t, map[string]string{"k3d": "cat <<'EOF'\n" + covAK3dKubeconfig + "EOF", "kubectl": `echo "boom" >&2; exit 1`})
		k := KubeconfigSecretEntity{Name: "hub", TargetCluster: "hub", InCluster: "k3d-app", Namespace: "flux", Reachability: "endpoint", ContextName: "hub"}
		err := mintOneKubeconfigSecret(ctx, k, "", "")
		if err == nil || !strings.Contains(err.Error(), "kubectl config rename-context k3d-hub hub") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the namespace cannot be ensured on the consumer cluster", func(t *testing.T) {
		covAStubBins(t, map[string]string{"k3d": "cat <<'EOF'\n" + covAK3dKubeconfig + "EOF", "kubectl": `echo "connection refused" >&2; exit 1`})
		k := KubeconfigSecretEntity{Name: "hub", TargetCluster: "hub", InCluster: "k3d-app", Namespace: "flux", Reachability: "endpoint", ContextName: "k3d-hub"}
		err := mintOneKubeconfigSecret(ctx, k, "", "")
		if err == nil || !strings.Contains(err.Error(), `ensure namespace "flux" in "k3d-app"`) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestKubeconfigSecretYAMLRoundTripsBinaryContents(t *testing.T) {
	raw := []byte("a: b\n\x00\xff")
	y := kubeconfigSecretYAML("hub", "flux", "value.yaml", raw)
	if !strings.Contains(y, "  value.yaml: "+base64.StdEncoding.EncodeToString(raw)+"\n") || !strings.Contains(y, "app.kubernetes.io/managed-by: forge") {
		t.Errorf("yaml:\n%s", y)
	}
}
