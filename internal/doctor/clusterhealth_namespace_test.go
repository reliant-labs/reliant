package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/devstack"
)

// The Cluster Workloads check probes a (context, namespace) pair, and the
// namespace comes from the RENDER. So the check is only as correct as its
// render is faithful to the one `forge env deploy` applied.
//
// It was not. Every other render path pushed the parallel-dev-stack git
// facts into KCL — `-D worktree=<name>` — and this check's render omitted
// them. A project that keys its namespace on that option (control-plane's
// deploy/kcl/dev/main.k does: `option("namespace") or
// identity.namespace(option("worktree"))`) therefore rendered the UNSUFFIXED
// namespace here and the suffixed one at deploy time.
//
// Observed, live, on a worktree checkout:
//
//	forge env status dev
//	  ✗ Cluster Workloads  daemon-gateway: NO PODS — the render declares 1
//	                       replica, the cluster has none ... [4 failing]
//	kubectl --context k3d-control-plane-v2 \
//	    get pods -n control-plane-dev-forge-deploy-882e308d
//	  daemon-gateway-5bf8766475-98vkr  1/1  Running
//
// Four healthy workloads reported absent, because the check looked in
// `control-plane-dev` and the pods were in
// `control-plane-dev-forge-deploy-882e308d`. `forge env up` exits non-zero on
// that verdict, so a false negative here fails a healthy stack — and would
// equally bury a real outage under noise people learn to ignore.
//
// These tests therefore assert on the namespace that REACHES the pod lister
// (the argument the kubectl invocation is built from), not on any helper's
// return value. A helper that returns the right string is not the property
// that was broken; where the probe actually looked is.

// nsProbeModule writes deploy/kcl/<env>/main.k for a project whose namespace
// is a function of option("worktree") — the shape control-plane uses, reduced
// to the one rule under test. If the binding does not arrive, option()
// yields None, `or` falls through to the bare base, and the rendered
// namespace is unsuffixed. That is the production bug, reproduced in a
// module small enough to read.
func nsProbeModule(t *testing.T, projectDir, env, base string) {
	t.Helper()
	dir := filepath.Join(projectDir, "deploy", "kcl", env)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The Deployment rides `output.manifests` — the one applyable stream a
	// render exports — and its cluster comes from the env-wide
	// cluster_target, exactly where the deploy path reads them.
	body := `_key = option("worktree") or ""
_suffix = "-" + _key if _key else ""
_namespace = "` + base + `" + _suffix

output = {
    cluster_target = {cluster = "k3d-probe", namespace = _namespace}
    manifests = [
        {
            apiVersion = "apps/v1"
            kind = "Deployment"
            metadata = {
                name = "daemon-gateway"
                namespace = _namespace
                labels = {"app.kubernetes.io/name" = "daemon-gateway"}
            }
            spec = {
                replicas = 1
                template = {spec = {containers = [{name = "app", image = "x:1"}]}}
            }
        }
    ]
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.k"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// recordingLister captures every (context, namespace) the check actually
// probed, and answers each with the supplied pods. This is the observation
// point the assertions use: it stands exactly where kubectlPods stands, so
// what it records is what kubectl would have been asked.
type recordingLister struct {
	mu      sync.Mutex
	probed  []string
	answers map[string][]string
}

func (r *recordingLister) list(t *testing.T) podLister {
	t.Helper()
	return func(ctx context.Context, kctx, ns string) ([]podView, error) {
		r.mu.Lock()
		r.probed = append(r.probed, kctx+"/"+ns)
		r.mu.Unlock()
		return cwPods(t, r.answers)(ctx, kctx, ns)
	}
}

func (r *recordingLister) targets() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.probed...)
}

// runNSCheck runs the EXPORTED entry point — the one `forge env status`
// calls — against a real render, with the pod lister substituted. Going
// through CheckClusterWorkloads rather than clusterWorkloadReport is the
// point: the defect lived in the render, which the inner seam skips.
func runNSCheck(t *testing.T, projectDir, env string, rec *recordingLister) CheckResult {
	t.Helper()
	prev := clusterPodLister
	clusterPodLister = rec.list(t)
	t.Cleanup(func() { clusterPodLister = prev })
	return CheckClusterWorkloads(context.Background(), &Environment{
		ProjectName: "probe", ProjectDir: projectDir, Env: env,
	})
}

// withActiveDevStack arms the process-global render context the up/deploy
// path arms (internal/cli.activateDevStack), and restores it. Tests in this
// package otherwise render with the zero value, which is the no-worktree
// case pinned below.
func withActiveDevStack(t *testing.T, o devstack.Options) {
	t.Helper()
	prev := devstack.Active()
	devstack.SetActive(o)
	t.Cleanup(func() { devstack.SetActive(prev) })
}

// THE REGRESSION. With a worktree active, the check must probe the SUFFIXED
// namespace — the one the deploy phase applied into. Before the fix it
// probed `cp-dev` while the pods were in `cp-dev-wt7`, and reported a
// running workload as NO PODS.
func TestClusterWorkloadsProbesTheWorktreeSuffixedNamespace(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL through the embedded runtime")
	}
	projectDir := t.TempDir()
	nsProbeModule(t, projectDir, "dev", "cp-dev")
	withActiveDevStack(t, devstack.Options{Worktree: "wt7", Branch: "feature"})

	// The pods exist, and they exist ONLY in the suffixed namespace —
	// exactly the live situation. A check that looks anywhere else finds
	// nothing and says so.
	rec := &recordingLister{answers: map[string][]string{
		"k3d-probe/cp-dev-wt7": {cwPod{
			name: "daemon-gateway-5bf8766475-98vkr", app: "daemon-gateway", ready: true,
		}.json()},
	}}

	got := runNSCheck(t, projectDir, "dev", rec)

	probed := rec.targets()
	want := "k3d-probe/cp-dev-wt7"
	if len(probed) != 1 || probed[0] != want {
		t.Fatalf("the check probed %v, want exactly [%s].\n"+
			"The status check resolved its namespace WITHOUT the active worktree, so it "+
			"listed pods somewhere the deploy phase never applied to. Every workload then "+
			"reads as absent — a confident wrong answer about a healthy stack.",
			probed, want)
	}
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — the workload is running in the namespace this env deploys to\nmessage: %s",
			got.Status, StatusPass, got.Message)
	}
	if strings.Contains(got.Message+got.Evidence, "NO PODS") {
		t.Errorf("report says NO PODS about a running workload:\n%s\n%s", got.Message, got.Evidence)
	}
}

// NON-REGRESSION. With no worktree active — the primary checkout, which is
// most projects most of the time — the namespace is unchanged. This passes
// both before and after the fix BY DESIGN: it pins the behaviour the fix had
// to preserve, so a future "just always append a suffix" cannot pass.
func TestClusterWorkloadsProbesTheBareNamespaceWithNoWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL through the embedded runtime")
	}
	projectDir := t.TempDir()
	nsProbeModule(t, projectDir, "dev", "cp-dev")
	withActiveDevStack(t, devstack.Options{}) // primary checkout: no worktree

	rec := &recordingLister{answers: map[string][]string{
		"k3d-probe/cp-dev": {cwPod{
			name: "daemon-gateway-abc-1", app: "daemon-gateway", ready: true,
		}.json()},
	}}

	got := runNSCheck(t, projectDir, "dev", rec)

	probed := rec.targets()
	want := "k3d-probe/cp-dev"
	if len(probed) != 1 || probed[0] != want {
		t.Fatalf("the check probed %v, want exactly [%s] — a checkout with no worktree must "+
			"resolve the historical namespace, byte-identically to before the dev-stack primitive existed",
			probed, want)
	}
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusPass, got.Message)
	}
}

// THE FIX MUST NOT BE "STOP CHECKING". A workload that is genuinely absent
// from the correct, suffixed namespace still reports NO PODS and still
// fails. Without this, pointing the probe at a namespace that happens to
// answer — or dropping the probe — would satisfy the two tests above.
func TestClusterWorkloadsStillReportsNoPodsInTheSuffixedNamespace(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL through the embedded runtime")
	}
	projectDir := t.TempDir()
	nsProbeModule(t, projectDir, "dev", "cp-dev")
	withActiveDevStack(t, devstack.Options{Worktree: "wt7", Branch: "feature"})

	// The suffixed namespace is empty. Pods DO sit in the unsuffixed one,
	// so a check that regressed to the old namespace would wrongly pass —
	// which makes this the mirror image of the first test.
	rec := &recordingLister{answers: map[string][]string{
		"k3d-probe/cp-dev": {cwPod{
			name: "daemon-gateway-stale-1", app: "daemon-gateway", ready: true,
		}.json()},
	}}

	got := runNSCheck(t, projectDir, "dev", rec)

	if probed := rec.targets(); len(probed) != 1 || probed[0] != "k3d-probe/cp-dev-wt7" {
		t.Fatalf("the check probed %v, want exactly [k3d-probe/cp-dev-wt7]", probed)
	}
	if got.Status != StatusFail {
		t.Fatalf("status = %q, want %q — the render declares a replica and the env's own namespace has none\nmessage: %s",
			got.Status, StatusFail, got.Message)
	}
	cwMustContain(t, got, "daemon-gateway", "NO PODS")
}
