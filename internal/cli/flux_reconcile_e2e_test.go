//go:build e2e

package cli

// END TO END, AGAINST A REAL CLUSTER: an env with no control plane and no
// declared lifecycle is applied DIRECTLY by forge, and forge puts no Flux into
// the cluster.
//
// THE CONTRACT (#461). Flux on a cluster comes only from a control plane's own
// declaration; forge never installs it into a deploy target and never routes
// a deploy through an in-cluster pointer. Every env without a
// forge.ControlPlane applies directly, and an env that declares neither a
// lifecycle nor an applier prints a note naming the two ways to say so.
//
// These tests used to pin the OPPOSITE — `forge cluster up` installing
// source- and kustomize-controller, and `forge env deploy` writing an
// OCIRepository + Kustomization pointer that Flux converged. #461 made
// that path unreachable from every env (it has since been deleted), and both
// tests went hard-red on every branch. They are
// rewritten to the new contract rather than deleted because the cluster-level
// half is not observable anywhere else: a unit test can assert that
// nothing installs Flux, but only a real cluster shows that nothing
// in flux-system was created, that the workload is owned by forge's own
// field manager rather than kustomize-controller, and that a declared Secret's
// real value lands without ever riding the bundle.
//
// The ControlPlane half of the contract (a ControlPlane env still reconciles
// through its hub's Flux) is NOT covered here: it needs a running hub, which
// is a separate test, not a rewrite of this one.
//
// TestE2EClusterUpInstallsADeclaredFluxChart is unchanged: a Flux the env
// DECLARES (forge.flux_chart()) is the user's chart, installed like any other.
//
// CLUSTER HYGIENE. This creates a cluster under a UNIQUE name, tears it down
// by that exact name, and touches nothing else. A shared dev box runs several
// k3d clusters (`k3d-control-plane-v2`, `k3d-cp-daemon-v2`, …) that other work
// depends on, so there is no `k3d cluster delete --all` here and no
// pattern-matched anything.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/goexec"
)

// TestE2EDirectApplyEnvInstallsNoFlux is the whole path, once: an env with no
// control plane gets a cluster with no Flux in it, and a deploy that forge
// applies itself.
func TestE2EDirectApplyEnvInstallsNoFlux(t *testing.T) {
	requireTool(t, "k3d", "kubectl", "helm", "docker")
	t.Parallel() // its own uniquely-named cluster, its own t.TempDir

	forgeBin := buildforgeBinary(t)

	// A name nothing else on this machine can be using, so the teardown
	// can be exact. The PID and the clock keep concurrent runs apart.
	clusterName := fmt.Sprintf("forge-flux-e2e-%d-%d", os.Getpid(), time.Now().UnixNano()%100000)
	kctx := "k3d-" + clusterName
	registryName := clusterName + "-registry"
	registryPort := freePortE2E(t)

	// THE LEDGER IS A TEMP DIR. ~/.forge/ledger holds the developer's real
	// promotion history for every project on the machine; a test must never
	// write into it.
	ledgerHome := t.TempDir()

	projectDir := scaffoldFluxE2EProject(t, forgeBin, clusterName, registryName, registryPort)

	// ── The cluster, created by us and deleted by exact name ─────────────
	t.Cleanup(func() { teardownFluxE2ECluster(t, clusterName, registryName) })

	runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, "cluster", "up", "dev-k8s", "--wait")

	// ── `forge cluster up` put no Flux into the deploy target ────────────
	assertNoFluxInstalled(t, kctx)

	runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, "env", "build", "dev-k8s", "--push")
	deployOut := runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, "env", "deploy", "dev-k8s", "--yes")

	// ── The deploy applied directly, and said so ─────────────────────────
	// Two independent signals, because either alone could be coincidence:
	//
	//  1. the deploy printed the transitional direct-apply note, and did
	//     NOT claim to record desired state for an in-cluster reconciler;
	//  2. the workload is owned by forge's own field manager, not by
	//     kustomize-controller, and no Flux object names this env.
	if !strings.Contains(deployOut, lifecycleNoticeLine) {
		t.Errorf("deploy did not print the direct-apply note for an env with no control plane:\n%s", deployOut)
	}
	if strings.Contains(deployOut, "desired state for its in-cluster reconciler") {
		t.Errorf("deploy claims it recorded desired state for an in-cluster reconciler; "+
			"no env without a control plane may route through forge's Flux path:\n%s", deployOut)
	}
	assertWorkloadAppliedByForgeNotFlux(t, kctx)
	assertNoFluxObjectsForEnv(t, kctx, "dev-k8s")

	// ── The workload is Running ──────────────────────────────────────────
	// Re-read from the cluster rather than taking the deploy's exit code.
	assertDeploymentAvailable(t, kctx, "app", "api")
}

// scaffoldFluxE2EProject creates a project with ONE env, `dev-k8s`, shaped
// like the envs this path serves: a k3d cluster, a registry, no control plane,
// and — the load-bearing part — NO `lifecycle` declaration.
func scaffoldFluxE2EProject(t *testing.T, forgeBin, clusterName, registryName string, registryPort int) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, forgeBin, "project", "new", "fluxapp", "--mod", "example.com/fluxapp", "--service", "api")
	projectDir := filepath.Join(dir, "fluxapp")

	// The k3d config: our own cluster name, our own registry, our own
	// ports. Nothing here can collide with another cluster on the box.
	k3dConfig := fmt.Sprintf(`apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: %s
servers: 1
agents: 0
registries:
  use:
    - k3d-%s:5000
  config: |
    mirrors:
      "localhost:%d":
        endpoint:
          - http://k3d-%s:5000
      "host.k3d.internal:%d":
        endpoint:
          - http://k3d-%s:5000
`, clusterName, registryName, registryPort, registryName, registryPort, registryName)
	writeFluxE2EFile(t, filepath.Join(projectDir, "deploy", "k3d-"+clusterName+".yaml"), k3dConfig)

	// The env. NO `lifecycle`, no `forge.ControlPlane`: before #461 this
	// shape routed through an in-cluster reconciler; now it applies directly.
	mainK := fmt.Sprintf(`import forge
import forge.workloads as fw

_cluster = forge.Cluster {
    name = %q
    config = "deploy/k3d-%s.yaml"
}

_target = forge.ClusterTarget {
    cluster = _cluster.context
    namespace = "app"
}

# A three-line busybox httpd answering /readyz and /healthz (the default probes), so forge builds and pushes a real image
# in seconds: the subject is the reconcile path, and a Go build would add
# minutes and a second failure mode.
_api = fw.Workload {
    name = "api"
    kind = "service"
    image = "localhost:%d/fluxapp/api"
    build = forge.DockerBuild {dockerfile = "Dockerfile.pause"}
}

# DELIBERATELY NO lifecycle declaration and no control plane: a real
# environment with no hub, which forge applies directly (transitionally).
output = forge.render(forge.Bundle {
    project = "fluxapp"
    env = "dev-k8s"
    clusters = [_cluster]
    cluster_target = _target
    workloads = [_api | {runtime = forge.OnCluster {target = _target}}]
})
`, clusterName, clusterName, registryPort)
	writeFluxE2EFile(t, filepath.Join(projectDir, "deploy", "kcl", "dev-k8s", "main.k"), mainK)

	writeFluxE2EFile(t, filepath.Join(projectDir, "Dockerfile.pause"), "FROM busybox:1.36\nRUN mkdir /www && echo ok > /www/readyz && echo ok > /www/healthz\nCMD [\"httpd\", \"-f\", \"-p\", \"8080\", \"-h\", \"/www\"]\n")

	commitFluxE2EFixture(t, projectDir)
	return projectDir
}

// commitFluxE2EFixture makes the fixture a git repo with a commit. A real
// project is one, and the deploy needs it: the image tag resolves from git
// (`git describe`), and a release cut with no commit carries none to pin. On a
// CI runner `forge project new` leaves no commit, and the deploy fails with
// "git tag resolution: exit status 128" — a fixture defect, not forge's.
//
// Idempotent, so a test that edits the fixture can call it again: init is a
// no-op on an existing repo, and the commit is skipped when nothing is staged.
func commitFluxE2EFixture(t *testing.T, projectDir string) {
	t.Helper()
	gitE2E(t, projectDir, "init", "-q")
	gitE2E(t, projectDir, "add", "-A")
	if strings.TrimSpace(gitE2E(t, projectDir, "status", "--porcelain")) == "" {
		return
	}
	// --no-verify: `forge project new` installs the scaffold's git hooks
	// whenever pre-commit is on PATH, and they are not this fixture's
	// subject. They fetch hook environments over the network and lint the
	// message (commitlint rejects "fixture"), so the commit passed or failed
	// with the machine's pre-commit setup rather than with forge.
	gitE2E(t, projectDir, "commit", "-q", "--no-verify", "-m", "fixture")
}

// runForgeFluxE2E runs forge with the ledger pointed at a temp dir.
//
// FORGE_LEDGER_HOME is the whole reason this wrapper exists: without it every
// build and deploy here would write into the developer's real
// `~/.forge/ledger`, mixing a test project's promotion history into the
// machine's.
func runForgeFluxE2E(t *testing.T, projectDir, forgeBin, ledgerHome string, args ...string) string {
	t.Helper()
	cmd := exec.Command(forgeBin, args...)
	cmd.Dir = projectDir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(),
		"FORGE_LEDGER_HOME="+ledgerHome,
		"GOFLAGS=",
		// The binary under test is a source build, and the project pins the
		// published forge: a source build cannot be fetched, so the scaffold's
		// go.mod requires the published floor and generate converges
		// forge_version to it. `env build`/`env deploy` refuse that skew
		// (forge_pin_skew.go) to stop a human shipping with a stale binary;
		// this test runs a different forge from the pin on purpose.
		allowVersionSkewEnv+"=1",
	))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("forge %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	t.Logf("forge %s:\n%s", strings.Join(args, " "), out)
	return string(out)
}

// assertFluxInstalled checks that `forge cluster up` put the reconciler in,
// and put in ONLY the two controllers this path uses.
//
// The negative half matters as much as the positive: helm-controller and
// notification-controller being absent is a security posture, and a chart
// default change upstream must not quietly turn them on.
func assertFluxInstalled(t *testing.T, kctx string) {
	t.Helper()
	deployments := kubectlJSONFluxE2E(t, kctx, "get", "deployments", "-n", "flux-system", "-o", "json")
	running := map[string]bool{}
	for _, item := range deployments["items"].([]any) {
		name := item.(map[string]any)["metadata"].(map[string]any)["name"].(string)
		running[name] = true
	}
	for _, want := range []string{"source-controller", "kustomize-controller"} {
		if !running[want] {
			t.Errorf("flux-system has no %s Deployment; `forge cluster up` must install the reconciler, "+
				"or a pointer written at it converges nothing. Present: %v", want, running)
		}
	}
	for _, unwanted := range []string{"helm-controller", "notification-controller",
		"image-automation-controller", "image-reflector-controller"} {
		if running[unwanted] {
			t.Errorf("flux-system runs %s; only source- and kustomize-controller are on this path, and "+
				"every extra controller is attack surface that has to be patched", unwanted)
		}
	}
	// The CRDs the pointer is written against must be Established, or the
	// write fails on a kind the apiserver has never registered.
	for _, crd := range []string{"ocirepositories.source.toolkit.fluxcd.io", "kustomizations.kustomize.toolkit.fluxcd.io"} {
		out := kubectlFluxE2E(t, kctx, "get", "crd", crd, "-o", "jsonpath={.status.conditions[?(@.type=='Established')].status}")
		if strings.TrimSpace(out) != "True" {
			t.Errorf("CRD %s is not Established (%q); forge's CRD-first apply is what the pointer write depends on", crd, out)
		}
	}
}

// assertNoFluxInstalled is the cluster-level half of #461: `forge cluster up`
// on an env that declares no Flux chart leaves no Flux behind — no
// flux-system controllers and no Flux CRDs. Only the cluster can
// show that nothing installed it.
func assertNoFluxInstalled(t *testing.T, kctx string) {
	t.Helper()
	// -o name, not JSON: with flux-system absent (the expected case) kubectl
	// prints nothing at all, which is the answer rather than a parse error.
	if names := strings.TrimSpace(kubectlFluxE2E(t, kctx, "get", "deployments", "-n", "flux-system",
		"-o", "name", "--ignore-not-found")); names != "" {
		t.Errorf("flux-system runs %s; forge must never install Flux into a deploy target "+
			"(Flux on a cluster comes only from a control plane's declaration)", names)
	}
	crds := kubectlFluxE2E(t, kctx, "get", "crd", "-o", "name")
	for _, crd := range []string{"ocirepositories.source.toolkit.fluxcd.io", "kustomizations.kustomize.toolkit.fluxcd.io"} {
		if strings.Contains(crds, crd) {
			t.Errorf("CRD %s is installed; forge installed Flux into a deploy target", crd)
		}
	}
}

// assertNoFluxObjectsForEnv: the deploy wrote no OCIRepository or
// Kustomization pointer for this env. With no Flux CRDs registered, kubectl
// cannot even name the kinds; that failure is the expected answer, so the
// probe runs raw rather than through the fatal helper.
func assertNoFluxObjectsForEnv(t *testing.T, kctx, env string) {
	t.Helper()
	for _, kind := range []string{"ocirepositories.source.toolkit.fluxcd.io", "kustomizations.kustomize.toolkit.fluxcd.io"} {
		out, err := exec.Command("kubectl", "--context", kctx, "get", kind, "-A", "-o", "name").CombinedOutput()
		if err != nil {
			continue // the kind does not exist on this cluster: nothing was written
		}
		if names := strings.TrimSpace(string(out)); names != "" {
			t.Errorf("the cluster holds Flux %s objects (%s); env %s has no control plane and must apply directly",
				kind, names, env)
		}
	}
}

// assertWorkloadAppliedByForgeNotFlux is the ownership evidence for a direct
// apply. Server-side apply records a field manager per object: forge's own
// apply (cluster.KubectlApply) runs under kubectl's SSA, Flux's under
// `kustomize-controller`. A direct apply must leave the former and never the
// latter — kustomize-controller here would mean something in the cluster is
// reconciling objects forge also applies, two authorities over one object.
func assertWorkloadAppliedByForgeNotFlux(t *testing.T, kctx string) {
	t.Helper()
	obj := kubectlJSONFluxE2E(t, kctx, "get", "deployment", "api", "-n", "app", "-o", "json", "--show-managed-fields")
	meta := obj["metadata"].(map[string]any)
	raw, _ := meta["managedFields"].([]any)
	var managers []string
	for _, f := range raw {
		managers = append(managers, f.(map[string]any)["manager"].(string))
	}
	if len(managers) == 0 {
		t.Fatalf("deployment/api carries no managed fields; cannot tell who applied it")
	}
	for _, m := range managers {
		if m == "kustomize-controller" {
			t.Errorf("deployment/api is managed by kustomize-controller (managers: %v); an env without a "+
				"control plane must be applied by forge directly, not reconciled by Flux", managers)
		}
	}
	applied := false
	for _, m := range managers {
		if m == "forge" || strings.HasPrefix(m, "kubectl") {
			applied = true
		}
	}
	if !applied {
		t.Errorf("deployment/api has no forge/kubectl apply manager (managers: %v); the direct apply did not put it there", managers)
	}
}

// assertDeploymentAvailable re-reads the rollout from the cluster rather than
// trusting the deploy's exit code.
func assertDeploymentAvailable(t *testing.T, kctx, namespace, name string) {
	t.Helper()
	// Flux applied under `wait: true`, so this should already be true; the
	// short retry covers the gap between the Kustomization reporting Ready
	// and this read.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		out := kubectlFluxE2E(t, kctx, "get", "deployment", name, "-n", namespace,
			"-o", "jsonpath={.status.availableReplicas}")
		if strings.TrimSpace(out) != "" && strings.TrimSpace(out) != "0" {
			return
		}
		if !time.Now().Before(deadline) {
			describe, _ := exec.Command("kubectl", "--context", kctx, "describe",
				"deployment", name, "-n", namespace).CombinedOutput()
			t.Fatalf("deployment %s/%s never became available (availableReplicas=%q)\n%s",
				namespace, name, out, describe)
		}
		time.Sleep(3 * time.Second)
	}
}

// recordedBundleDigestFromDeployOutput is the digest the LEDGER recorded for
// the bundle this
// deploy pinned: the LAST `oci@sha256:<digest>` the deploy reported.
//
// The deploy builds (and so records) its own bundle after our explicit build,
// so the newest line is the one the pointer names. It comes from forge's own
// report of what it wrote to the ledger, never from the Kustomization, because
// taking it from the cluster would compare the cluster to itself.
func recordedBundleDigestFromDeployOutput(t *testing.T, deployOut string) string {
	t.Helper()
	const marker = "oci@"
	i := strings.LastIndex(deployOut, marker)
	if i < 0 {
		t.Fatalf("the deploy reported no recorded bundle (no %q line):\n%s", marker, deployOut)
	}
	digest := firstSHA256InFluxE2E(deployOut[i:])
	if digest == "" {
		t.Fatalf("no canonical digest after %q:\n%s", marker, deployOut[i:])
	}
	return digest
}

// firstSHA256InFluxE2E pulls the first canonical digest out of some text.
func firstSHA256InFluxE2E(s string) string {
	const prefix = "sha256:"
	for i := 0; i+len(prefix)+64 <= len(s); i++ {
		if s[i:i+len(prefix)] != prefix {
			continue
		}
		candidate := s[i : i+len(prefix)+64]
		hex := candidate[len(prefix):]
		ok := true
		for _, r := range hex {
			if !strings.ContainsRune("0123456789abcdef", r) {
				ok = false
				break
			}
		}
		if ok {
			return candidate
		}
	}
	return ""
}

// teardownFluxE2ECluster removes everything one test created, by exact name,
// unconditionally — and never anything else on the machine. Errors are logged
// rather than failed: the test's verdict is about forge, not about teardown.
//
// The NETWORK is the step that used to be missing. forge attaches its
// standalone registry to the cluster's docker network, so `k3d cluster
// delete` cannot remove that network while the registry is still connected,
// and deleting the registry afterwards does not go back for it. Every run
// leaked one `k3d-<cluster>` network; Docker's predefined address pools hold
// a few dozen, and once they were exhausted every later k3d cluster on the
// machine — this suite's and everyone else's — failed with "all predefined
// address pools have been fully subnetted".
func teardownFluxE2ECluster(t *testing.T, clusterName, registryName string) {
	t.Helper()
	// The delete rewrites the default kubeconfig, while a parallel test's
	// forge may be merging its own context into it: take forge's kubeconfig
	// lock, or this teardown can erase that context (kubeconfig_lock.go).
	_ = withKubeconfigLock(func() error {
		if out, err := exec.Command("k3d", "cluster", "delete", clusterName).CombinedOutput(); err != nil {
			t.Logf("teardown: k3d cluster delete %s: %v\n%s", clusterName, err, out)
		}
		return nil
	})
	if out, err := exec.Command("k3d", "registry", "delete", "k3d-"+registryName).CombinedOutput(); err != nil {
		t.Logf("teardown: k3d registry delete %s: %v\n%s", registryName, err, out)
	}
	network := "k3d-" + clusterName
	if err := exec.Command("docker", "network", "inspect", network).Run(); err != nil {
		return // already gone (k3d removed it, or it was never created)
	}
	if out, err := exec.Command("docker", "network", "rm", network).CombinedOutput(); err != nil {
		t.Logf("teardown: docker network rm %s: %v\n%s", network, err, out)
	}
}

// ── kubectl helpers, scoped to OUR context only ─────────────────────────────

// kubectlFluxE2E runs kubectl against the test's own cluster.
//
// The context is ALWAYS passed explicitly. A kubectl with no `--context` uses
// whatever is current, and `k3d cluster create` silently flips that — so an
// unscoped command here could read, or worse write, a cluster another agent's
// work depends on.
func kubectlFluxE2E(t *testing.T, kctx string, args ...string) string {
	t.Helper()
	full := append([]string{"--context", kctx}, args...)
	out, err := exec.Command("kubectl", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s failed: %v\n%s", strings.Join(full, " "), err, out)
	}
	return string(out)
}

func kubectlJSONFluxE2E(t *testing.T, kctx string, args ...string) map[string]any {
	t.Helper()
	raw := kubectlFluxE2E(t, kctx, args...)
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("parse kubectl JSON: %v\n%s", err, raw)
	}
	return out
}

func writeFluxE2EFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestE2EClusterUpInstallsADeclaredFluxChart: an env that DECLARES
// `forge.flux_chart()` gets that Flux from `forge cluster up`. forge's own
// install steps aside for a declared one, so before the platform phase nothing
// installed it and the first pointer write failed on `no matches for kind
// OCIRepository`.
func TestE2EClusterUpInstallsADeclaredFluxChart(t *testing.T) {
	requireTool(t, "k3d", "kubectl", "helm", "docker")
	t.Parallel()

	forgeBin := buildforgeBinary(t)
	clusterName := fmt.Sprintf("forge-flux-decl-%d-%d", os.Getpid(), time.Now().UnixNano()%100000)
	kctx := "k3d-" + clusterName
	registryName := clusterName + "-registry"
	registryPort := freePortE2E(t)
	ledgerHome := t.TempDir()

	projectDir := scaffoldFluxE2EProject(t, forgeBin, clusterName, registryName, registryPort)
	mainK := filepath.Join(projectDir, "deploy", "kcl", "dev-k8s", "main.k")
	raw, err := os.ReadFile(mainK)
	if err != nil {
		t.Fatal(err)
	}
	declared := strings.Replace(string(raw), "    clusters = [_cluster]\n",
		"    clusters = [_cluster]\n    helm_charts = [forge.flux_chart()]\n", 1)
	if declared == string(raw) {
		t.Fatal("could not declare flux_chart in the scaffolded env")
	}
	writeFluxE2EFile(t, mainK, declared)

	t.Cleanup(func() { teardownFluxE2ECluster(t, clusterName, registryName) })

	runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, "cluster", "up", "dev-k8s", "--wait")
	assertFluxInstalled(t, kctx)
}

// TestE2EDirectApplySecretsLandAndStayOutOfTheBundle is the F-13 contract on
// the direct-apply path every env without a control plane takes (#461),
// against a real cluster:
//
//   - a Secret's REAL value ends up in the cluster — both a declared
//     rendered_secret and a raw Secret manifest;
//   - no preview (--explain, --dry-run) and no deploy ever prints the value;
//   - the recorded bundle's layer holds no Secret object at all — the bundle
//     is the artifact a control plane's Flux would fetch, so a Secret riding
//     it would leak through the hub the day this env gets one;
//   - a second deploy is idempotent (the Secret is not rewritten);
//   - dropping the Secrets from the KCL does not make forge delete them —
//     removal is the user's call.
//
// It used to assert the in-cluster Flux path's own artifacts (a
// "synced N secret(s)" line, the forge-secrets field manager, an --explain
// that listed Secrets to sync). That path is unreachable since #461; those
// assertions are gone and every property above is kept.
func TestE2EDirectApplySecretsLandAndStayOutOfTheBundle(t *testing.T) {
	requireTool(t, "k3d", "kubectl", "helm", "docker")
	t.Parallel()

	forgeBin := buildforgeBinary(t)
	clusterName := fmt.Sprintf("forge-flux-sec-%d-%d", os.Getpid(), time.Now().UnixNano()%100000)
	kctx := "k3d-" + clusterName
	registryName := clusterName + "-registry"
	registryPort := freePortE2E(t)
	ledgerHome := t.TempDir()

	projectDir := scaffoldFluxE2EProject(t, forgeBin, clusterName, registryName, registryPort)
	t.Cleanup(func() { teardownFluxE2ECluster(t, clusterName, registryName) })

	const realValue = "real-secret-value-9f3a"
	mainK := filepath.Join(projectDir, "deploy", "kcl", "dev-k8s", "main.k")
	base, err := os.ReadFile(mainK)
	if err != nil {
		t.Fatal(err)
	}
	withSecrets := strings.Replace(string(base), "    workloads = [_api",
		`    rendered_secrets = [forge.RenderedSecret {
        name = "store-creds"
        keys = {"token" = forge.RenderedSecretKey {key = "STORE_TOKEN"}}
    }]
    manifests = [forge.Manifests {objects = [{
        apiVersion = "v1"
        kind = "Secret"
        metadata = {name = "raw-creds", namespace = "app"}
        type = "Opaque"
        stringData = {token = "`+realValue+`"}
    }]}]
    workloads = [_api`, 1)
	if withSecrets == string(base) {
		t.Fatal("the fixture's Bundle shape changed; the Secrets were not injected")
	}
	writeFluxE2EFile(t, mainK, withSecrets)
	writeFluxE2EFile(t, filepath.Join(projectDir, "secrets", "dev-k8s.yaml"), "STORE_TOKEN: "+realValue+"\n")

	// --dry-run resolves its image tag from git, and has no build state of
	// its own to fall back on: commit the Secrets edit too.
	commitFluxE2EFixture(t, projectDir)

	runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, "cluster", "up", "dev-k8s", "--wait")
	runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, "env", "build", "dev-k8s", "--push")

	// ── Previews never print a value ─────────────────────────────────────
	// --dry-run renders the Secret manifests (redacted); --explain prints
	// the cluster-guard decision. Neither may carry the value.
	for _, args := range [][]string{
		{"env", "deploy", "dev-k8s", "--explain"},
		{"env", "deploy", "dev-k8s", "--dry-run"},
	} {
		if out := runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, args...); strings.Contains(out, realValue) {
			t.Fatalf("forge %s printed a Secret value:\n%s", strings.Join(args, " "), out)
		}
	}

	deployOut := runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, "env", "deploy", "dev-k8s", "--yes")
	if strings.Contains(deployOut, realValue) {
		t.Fatalf("the deploy printed a Secret value:\n%s", deployOut)
	}
	if !strings.Contains(deployOut, lifecycleNoticeLine) {
		t.Errorf("deploy did not print the direct-apply note for an env with no control plane:\n%s", deployOut)
	}

	// ── The REAL value is in the cluster, applied by forge, not Flux ─────
	for _, name := range []string{"store-creds", "raw-creds"} {
		assertSecretValue(t, kctx, name, "token", realValue)
		assertSecretNotManagedByFlux(t, kctx, name)
	}
	assertNoFluxInstalled(t, kctx)

	// ── The bundle holds no Secret object ────────────────────────────────
	assertBundleHasNoSecret(t, ledgerHome, recordedBundleDigestFromDeployOutput(t, deployOut))

	// ── Re-deploy is idempotent ──────────────────────────────────────────
	before := kubectlFluxE2E(t, kctx, "get", "secret", "store-creds", "-n", "app", "-o", "jsonpath={.metadata.resourceVersion}")
	runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, "env", "deploy", "dev-k8s", "--yes")
	after := kubectlFluxE2E(t, kctx, "get", "secret", "store-creds", "-n", "app", "-o", "jsonpath={.metadata.resourceVersion}")
	if before != after {
		t.Errorf("a re-deploy rewrote an unchanged Secret (resourceVersion %s -> %s)", before, after)
	}

	// ── Removing the Secrets from the KCL deletes nothing ────────────────
	writeFluxE2EFile(t, mainK, string(base))
	runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, "env", "build", "dev-k8s", "--push")
	runForgeFluxE2E(t, projectDir, forgeBin, ledgerHome, "env", "deploy", "dev-k8s", "--yes")
	for _, name := range []string{"store-creds", "raw-creds"} {
		assertSecretValue(t, kctx, name, "token", realValue)
	}
}

func assertSecretValue(t *testing.T, kctx, name, key, want string) {
	t.Helper()
	got := kubectlFluxE2E(t, kctx, "get", "secret", name, "-n", "app", "-o", "go-template={{index .data \""+key+"\" | base64decode}}")
	if strings.TrimSpace(got) != want {
		t.Errorf("secret %s key %s = %q in the cluster, want the real value %q", name, key, got, want)
	}
}

// assertSecretNotManagedByFlux: the Secret was applied by forge's direct
// apply, and nothing reconciled it out of a bundle — kustomize-controller as a
// manager would mean the Secret rode an artifact a reconciler fetched.
func assertSecretNotManagedByFlux(t *testing.T, kctx, name string) {
	t.Helper()
	managers := kubectlFluxE2E(t, kctx, "get", "secret", name, "-n", "app", "--show-managed-fields",
		"-o", "jsonpath={.metadata.managedFields[*].manager}")
	if strings.TrimSpace(managers) == "" {
		t.Errorf("secret %s carries no managed fields; cannot tell who applied it", name)
	}
	if strings.Contains(managers, "kustomize-controller") {
		t.Errorf("secret %s is managed by kustomize-controller (%s): a Secret must not ride the bundle", name, managers)
	}
}

// assertBundleHasNoSecret reads the recorded bundle back out of the ledger's
// OCI layout and scans its manifest layer, so the check is on the bytes Flux
// would fetch rather than on forge's belief about them.
func assertBundleHasNoSecret(t *testing.T, ledgerHome, digest string) {
	t.Helper()
	var ociDir string
	_ = filepath.WalkDir(ledgerHome, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == "oci" {
			ociDir = path
			return filepath.SkipAll
		}
		return nil
	})
	if ociDir == "" {
		t.Fatalf("no OCI layout under %s", ledgerHome)
	}
	layout, err := bundle.NewLocalLayout(ociDir)
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := bundle.Fetch(context.Background(), layout, digest)
	if err != nil {
		t.Fatalf("read bundle %s: %v", digest, err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(fetched.Manifests))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(tr)
		if strings.Contains(string(data), "kind: Secret") {
			t.Errorf("the bundle layer carries a Secret object at %s:\n%s", hdr.Name, data)
		}
	}
	if len(fetched.Doc.Secrets) != 2 {
		t.Errorf("the bundle document names %d Secrets, want 2: %+v", len(fetched.Doc.Secrets), fetched.Doc.Secrets)
	}
}
