package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/kclplugin"
)

// crossClusterBundle is the shape of control-plane's dev env, reduced to the
// two workloads that deadlocked it: `bridge` on one cluster cannot become
// ready until `proxy`, on ANOTHER cluster, is running. The clusters are named
// so the dependent one sorts first — the order forge dispatches groups in —
// exactly as k3d-control-plane-v2 sorts ahead of k3d-cp-daemon-v2.
const crossClusterBundle = `    project = "acme"
    env = "dev"
    cluster_target = forge.ClusterTarget { cluster = "k3d-alpha", namespace = "acme-dev" }
    workloads = [
        fw.Workload {
            name = "bridge"
            image = "ghcr.io/acme/bridge:v1"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-alpha", namespace = "acme-dev"}}
        }
        fw.Workload {
            name = "proxy"
            image = "ghcr.io/acme/proxy:v1"
            ports = [fw.Port {name = "http", port = 8081}]
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-beta", namespace = "acme-dev"}}
        }
    ]`

// fakeCrossClusterKubectl installs a fake `kubectl` on PATH that models two
// clusters with a readiness dependency between them, and returns a reader for
// every invocation as "<context> <argv>", in order.
//
//   - apply echoes `<kind>/<name> serverside-applied` per document (so the
//     apply-completeness check passes) and remembers which Deployments landed
//     on which context — that is what `get deployments` lists back.
//   - `rollout status deployment/bridge` on k3d-alpha reports kubectl's own
//     timeout UNLESS proxy has already been applied to k3d-beta. Every other
//     rollout is ready at once.
//
// So the fake answers the one question the deadlock turns on: was the second
// cluster applied before the first cluster's rollout was awaited?
func fakeCrossClusterKubectl(t *testing.T) func() []string {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	dir := t.TempDir()
	script := `#!/bin/sh
state='` + dir + `'
ctx=""
if [ "$1" = "--context" ]; then ctx="$2"; shift 2; fi
printf '%s %s\n' "$ctx" "$*" >> "$state/calls.log"
case "$1" in
  apply)
    in=$(mktemp "$state/stdin.XXXXXX")
    cat > "$in"
    awk -v dfile="$state/deployments-$ctx" '
      function emit() { if (k != "" && n != "") { print k "/" n " serverside-applied"; if (k == "deployment") print n >> dfile } k = ""; n = ""; m = 0 }
      /^---$/ { emit(); next }
      /^kind:/ { k = tolower($2) }
      /^metadata:/ { m = 1; next }
      /^[^ ]/ { m = 0 }
      m && $1 == "name:" && n == "" { n = $2 }
      END { emit() }' "$in"
    rm -f "$in"
    exit 0 ;;
  rollout)
    dep=${3#deployment/}
    if [ "$ctx" = "k3d-alpha" ] && [ "$dep" = "bridge" ] && ! grep -qx proxy "$state/deployments-k3d-beta" 2>/dev/null; then
      echo "error: timed out waiting for the condition" >&2
      exit 1
    fi
    echo "deployment \"$dep\" successfully rolled out"
    exit 0 ;;
  get)
    if [ "$2" = "deployments" ]; then sort -u "$state/deployments-$ctx" 2>/dev/null; fi
    exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		data, err := os.ReadFile(filepath.Join(dir, "calls.log"))
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

// TestApplyDeployGroups_CrossClusterReadinessDoesNotDeadlock is the
// multi-cluster rollout deadlock, end to end through the real deploy path
// (applyDeployGroups → the k8s provider → cluster apply) with a real KCL
// render of a two-cluster env.
//
// The groups used to be dispatched one at a time, each applied AND awaited
// before the next was touched. So the first cluster's wait for bridge ran
// while proxy had never been sent to the second cluster, timed out, and failed
// the deploy before the second cluster was applied at all:
//
//	deploy k8s-cluster: k8s-cluster deploy (ns=acme-dev, cluster=k3d-alpha):
//	rollout failed: bridge did not become ready
//
// Kubernetes converges concurrently, so the deploy must too: every cluster is
// applied, and only then is any rollout awaited.
func TestApplyDeployGroups_CrossClusterReadinessDoesNotDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	kclplugin.Register()
	ctx := context.Background()
	dir := workloadURLProject(t, "dev", crossClusterBundle)
	entities, err := RenderKCL(ctx, dir, "dev")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	groups, err := buildDeployGroups("dev", entities, "acme-dev")
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	if len(groups) != 2 || groups[0].Cluster != "k3d-alpha" || groups[1].Cluster != "k3d-beta" {
		t.Fatalf("precondition: want the dependent cluster's group dispatched first, got %+v", groups)
	}

	readCalls := fakeCrossClusterKubectl(t)
	report := newDeployReport("dev", true)
	err = applyDeployGroups(ctx, deployApplyInput{
		groups: groups, entities: entities, hasK8sServices: true,
		mainK:    filepath.Join(dir, "deploy", "kcl", "dev", "main.k"),
		imageTag: "t", namespace: "acme-dev", envName: "dev",
		rollout: cluster.RolloutPolicy{Timeout: 5 * time.Second},
		report:  report,
	})
	calls := readCalls()
	if err != nil {
		t.Fatalf("a deploy whose clusters depend on each other must converge once every cluster is applied, got: %v\nkubectl calls:\n%s",
			err, strings.Join(calls, "\n"))
	}

	// The property itself, not just its consequence: the last apply to the
	// second cluster precedes the first rollout wait anywhere.
	lastBetaApply, firstWait := -1, -1
	for i, c := range calls {
		if strings.HasPrefix(c, "k3d-beta apply ") {
			lastBetaApply = i
		}
		if strings.Contains(c, " rollout status ") && firstWait < 0 {
			firstWait = i
		}
	}
	if lastBetaApply < 0 || firstWait < 0 || lastBetaApply > firstWait {
		t.Errorf("every cluster must be applied before any rollout is awaited (last k3d-beta apply=%d, first wait=%d):\n%s",
			lastBetaApply, firstWait, strings.Join(calls, "\n"))
	}

	// Both clusters' outcomes reach the ONE --json document.
	got := map[string]deployJSONRolloutState{}
	for _, res := range report.document().Rollout.Results {
		got[res.Kind+"/"+res.Name] = res.State
	}
	for _, want := range []string{"Deployment/bridge", "Deployment/proxy"} {
		if got[want] != deployRolloutStateReady {
			t.Errorf("rollout result for %s = %q, want ready (all results: %v)", want, got[want], got)
		}
	}
}
