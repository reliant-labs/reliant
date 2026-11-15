//go:build e2e

package cli

// END TO END, AGAINST A REAL CLUSTER: an `applier = "flux"` env, delivered by
// the Flux forge bootstraps into its own k3d cluster.
//
// One cluster, one project, the whole lifecycle in order:
//
//  1. bootstrap (`--target flux-delivery`), then deploy: the deploy records,
//     pushes the bundle, points the Flux, and every Kustomization reports
//     lastAppliedRevision == the recorded digest; the pointer-to-converged
//     time is logged;
//  2. the release adds a CRD and an instance of it, which the bootstrap never
//     granted. Deployed with a POINTER-ONLY credential (a ServiceAccount that
//     may write the pointer and read the record, nothing else), the deploy
//     refuses with exit 9 and the "re-run the bootstrap" message, and NOTHING
//     moved: no promotion, no bundle in the registry, the pointer unchanged;
//  3. the same release deployed with the admin credential re-runs the
//     bootstrap's grants and converges;
//  4. a grant taken away behind forge's back (the backstop): Flux fails
//     Forbidden, and `forge env status` (and --wait) map it to the same
//     message, exit 1; re-running the bootstrap heals it;
//  5. the pointer is deleted: the delivery ResourceSet's empty-set garbage
//     collection ORPHANS everything (prune-disabled), and the workloads run on.
//
// HYGIENE. The cluster and its registry have unique names and are deleted by
// those names. Every k3d, kubectl and forge call runs with KUBECONFIG pointed
// at a file in t.TempDir(), so the machine's kubeconfig is never read or
// written; the test asserts at the end that it holds no context for this
// test's cluster. (Not that its current-context is unchanged: parallel e2e
// tests that DO use the machine's kubeconfig, such as
// TestE2EDirectApplySecretsLandAndStayOutOfTheBundle, switch and unset it
// mid-run, which failed this test on main for a change it never made.) The
// ledger is a t.TempDir() too.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

const fluxDeliveryE2EEnv = "selfhosted"

// fluxDeliveryE2E is one run's cluster, project and credentials.
type fluxDeliveryE2E struct {
	t            *testing.T
	forgeBin     string
	projectDir   string
	ledgerHome   string
	adminConfig  string // KUBECONFIG of the cluster's admin
	clusterName  string
	registryName string
	registryPort int
	kctx         string
	deliveryNS   string
	kicks        int // reconcile requests made, for a unique request value each
}

func TestE2EFluxDeliverySelfHosted(t *testing.T) {
	requireTool(t, "k3d", "kubectl", "helm", "docker")
	t.Parallel() // its own uniquely-named cluster, registry, kubeconfig and t.TempDir

	e := &fluxDeliveryE2E{
		t:            t,
		forgeBin:     buildforgeBinary(t),
		ledgerHome:   t.TempDir(),
		adminConfig:  filepath.Join(t.TempDir(), "kubeconfig"),
		clusterName:  fmt.Sprintf("forge-fluxdel-%d-%d", os.Getpid(), time.Now().UnixNano()%100000),
		registryPort: freePortE2E(t),
	}
	e.registryName = e.clusterName + "-registry"
	e.kctx = "k3d-" + e.clusterName
	e.deliveryNS = fluxdelivery.SelfHostedNamespace("fluxapp", fluxDeliveryE2EEnv)
	if err := os.WriteFile(e.adminConfig, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.projectDir = e.scaffold()
	t.Cleanup(e.teardown)
	t.Cleanup(func() {
		if slices.Contains(defaultKubeconfigContexts(t), e.kctx) {
			t.Errorf("the machine's kubeconfig gained this test's context %q: something ignored KUBECONFIG", e.kctx)
		}
	})

	// ── 1. Bootstrap, then deploy ────────────────────────────────────────
	e.mustForge(e.adminConfig, "cluster", "up", fluxDeliveryE2EEnv, "--wait")
	e.mustForge(e.adminConfig, "env", "deploy", fluxDeliveryE2EEnv, "--target", fluxdelivery.ChartName)
	out := e.mustForge(e.adminConfig, "env", "deploy", fluxDeliveryE2EEnv, "--yes")
	first := e.pointerDigest()
	e.assertConverged(first)
	e.assertDeploymentAvailable("app", "api")
	if m := regexp.MustCompile(`converged: .* in (\S+)`).FindStringSubmatch(out); m != nil {
		t.Logf("MEASURED pointer -> converged: %s", m[1])
	} else {
		t.Errorf("the deploy did not report convergence:\n%s", out)
	}

	// ── 2. A new CRD, deployed with a pointer-only credential ─────────────
	e.writeMain(true)
	commitFluxE2EFixture(t, e.projectDir)
	pointerOnly := e.pointerOnlyKubeconfig()
	before := e.pointerSnapshot()
	promotionsBefore := e.ledgerLines("promotions")
	out, code := e.forge(pointerOnly, "env", "deploy", fluxDeliveryE2EEnv, "--yes")
	if code != exitNeedsHuman {
		t.Fatalf("pointer-only deploy of a release that needs a new kind: exit %d, want %d\n%s", code, exitNeedsHuman, out)
	}
	for _, want := range []string{
		"needs permissions the delivery identities were not granted",
		"apiextensions.k8s.io/customresourcedefinitions",
		"(cluster)",
		"Ask a cluster admin to run:   forge env deploy " + fluxDeliveryE2EEnv + " --target flux-delivery",
		"Nothing was recorded, pushed or pointed.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, out)
		}
	}
	if after := e.pointerSnapshot(); after != before {
		t.Errorf("a refused deploy moved the pointer:\nbefore %s\nafter  %s", before, after)
	}
	if got := e.ledgerLines("promotions"); got != promotionsBefore {
		t.Errorf("a refused deploy recorded a promotion (%d -> %d lines)", promotionsBefore, got)
	}
	if refused := e.newestBundleDigest(); refused == first {
		t.Fatalf("the new release recorded no new bundle (still %s)", first)
	} else if e.registryHas(refused) {
		t.Errorf("a refused deploy pushed bundle %s", refused)
	}

	// ── 3. The same release with the admin credential re-grants ──────────
	out = e.mustForge(e.adminConfig, "env", "deploy", fluxDeliveryE2EEnv, "--yes")
	if !strings.Contains(out, "re-running the bootstrap") {
		t.Errorf("the admin deploy did not say it re-granted:\n%s", out)
	}
	second := e.pointerDigest()
	if second == first {
		t.Fatalf("the admin deploy did not move the pointer")
	}
	e.assertConverged(second)
	e.kubectl("get", "widgets.e2e.forge.dev", "w", "-n", "app")
	if rec := e.kubectl("get", "configmap", fluxdelivery.GrantsRecord, "-n", e.deliveryNS, "-o", "jsonpath={.data.grants}"); !strings.Contains(rec, "e2e.forge.dev/widgets") {
		t.Errorf("the grants record does not record the new kind:\n%s", rec)
	}

	// ── 4. The backstop: a grant taken away, Flux's Forbidden mapped ─────
	e.revokeWidgetGrant()
	e.kickPreTree()
	// --fail-fast: the default wait tolerates the failure until its deadline
	// (Flux retries, and the grant may land); this asks for it at once.
	out, code = e.forge(e.adminConfig, "env", "status", fluxDeliveryE2EEnv, "--wait", "--fail-fast", "--timeout", "4m")
	if code != exitWrong || !strings.Contains(out, "Ask a cluster admin to run:   forge env deploy "+fluxDeliveryE2EEnv+" --target flux-delivery") ||
		!strings.Contains(out, "e2e.forge.dev/widgets") {
		t.Errorf("status --wait on a Forbidden apply: exit %d, want 1 with the grant message\n%s", code, out)
	}
	e.mustForge(e.adminConfig, "env", "deploy", fluxDeliveryE2EEnv, "--target", fluxdelivery.ChartName)
	e.kickPreTree()
	e.mustForge(e.adminConfig, "env", "status", fluxDeliveryE2EEnv, "--wait", "--timeout", "4m")

	// ── 5. Delete the pointer: everything is orphaned, nothing deleted ───
	e.kubectl("delete", "resourcesetinputproviders.fluxcd.controlplane.io", fluxdelivery.InputProviderName(fluxDeliveryE2EEnv, ""), "-n", e.deliveryNS)
	e.waitRendererEmpty()
	e.assertDeploymentAvailable("app", "api")
	e.kubectl("get", "widgets.e2e.forge.dev", "w", "-n", "app")
	if ks := e.kubectl("get", "kustomizations.kustomize.toolkit.fluxcd.io", "-n", e.deliveryNS, "-o", "name"); strings.TrimSpace(ks) == "" {
		t.Errorf("deleting the pointer deleted the Kustomizations; the prune opt-out should have orphaned them")
	}
}

// scaffold creates the project: one env on its own k3d cluster and registry,
// `applier = "flux"`, one busybox service.
func (e *fluxDeliveryE2E) scaffold() string {
	t := e.t
	dir := t.TempDir()
	runCmd(t, dir, e.forgeBin, "project", "new", "fluxapp", "--mod", "example.com/fluxapp")
	projectDir := filepath.Join(dir, "fluxapp")
	writeFluxE2EFile(t, filepath.Join(projectDir, "deploy", "k3d-"+e.clusterName+".yaml"), fmt.Sprintf(`apiVersion: k3d.io/v1alpha5
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
`, e.clusterName, e.registryName, e.registryPort, e.registryName, e.registryPort, e.registryName))
	writeFluxE2EFile(t, filepath.Join(projectDir, "Dockerfile.pause"),
		"FROM busybox:1.36\nRUN mkdir /www && echo ok > /www/readyz && echo ok > /www/healthz\nCMD [\"httpd\", \"-f\", \"-p\", \"8080\", \"-h\", \"/www\"]\n")
	e.projectDir = projectDir
	e.writeMain(false)
	commitFluxE2EFixture(t, projectDir)
	return projectDir
}

// writeMain writes the env, with or without the CRD and its instance.
func (e *fluxDeliveryE2E) writeMain(withCRD bool) {
	extra := "[]"
	if withCRD {
		extra = `[forge.Manifests {objects = [_crd, _widget]}]`
	}
	writeFluxE2EFile(e.t, filepath.Join(e.projectDir, "deploy", "kcl", fluxDeliveryE2EEnv, "main.k"), fmt.Sprintf(`import forge
import forge.workloads as fw

_cluster = forge.Cluster {
    name = %q
    config = "deploy/k3d-%s.yaml"
}

_target = forge.ClusterTarget {
    cluster = _cluster.context
    namespace = "app"
}

_api = fw.Workload {
    name = "api"
    kind = "service"
    image = "localhost:%d/fluxapp/api"
    build = forge.DockerBuild {dockerfile = "Dockerfile.pause"}
}

_crd = {
    apiVersion = "apiextensions.k8s.io/v1"
    kind = "CustomResourceDefinition"
    metadata = {name = "widgets.e2e.forge.dev"}
    spec = {
        group = "e2e.forge.dev"
        scope = "Namespaced"
        names = {kind = "Widget", plural = "widgets", singular = "widget", listKind = "WidgetList"}
        versions = [{
            name = "v1"
            served = True
            storage = True
            schema = {openAPIV3Schema = {type = "object", "x-kubernetes-preserve-unknown-fields" = True}}
        }]
    }
}

_widget = {
    apiVersion = "e2e.forge.dev/v1"
    kind = "Widget"
    metadata = {name = "w", namespace = "app"}
    spec = {size = 1}
}

# A self-hosted env: no control plane, and the Flux forge installs in its one
# cluster applies every release.
output = forge.render(forge.Bundle {
    project = "fluxapp"
    env = %q
    clusters = [_cluster]
    cluster_target = _target
    applier = "flux"
    workloads = [_api | {runtime = forge.OnCluster {target = _target}}]
    manifests = %s
})
`, e.clusterName, e.clusterName, e.registryPort, fluxDeliveryE2EEnv, extra))
}

// environ is the environment every subprocess runs with: the test's own
// kubeconfig and ledger, and nothing of the machine's.
func (e *fluxDeliveryE2E) environ(kubeconfig string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "KUBECONFIG=") {
			env = append(env, kv)
		}
	}
	return goexec.WithTrimpath(append(env,
		"KUBECONFIG="+kubeconfig,
		"FORGE_LEDGER_HOME="+e.ledgerHome,
		"GOFLAGS=",
		allowVersionSkewEnv+"=1",
	))
}

// forge runs forge with kubeconfig and returns its output and exit code.
func (e *fluxDeliveryE2E) forge(kubeconfig string, args ...string) (string, int) {
	e.t.Helper()
	cmd := exec.Command(e.forgeBin, args...)
	cmd.Dir = e.projectDir
	cmd.Env = e.environ(kubeconfig)
	raw, err := cmd.CombinedOutput()
	out := string(raw)
	e.t.Logf("forge %s:\n%s", strings.Join(args, " "), out)
	if err == nil {
		return out, 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		e.t.Fatalf("forge %s: %v", strings.Join(args, " "), err)
	}
	return out, ee.ExitCode()
}

func (e *fluxDeliveryE2E) mustForge(kubeconfig string, args ...string) string {
	e.t.Helper()
	out, code := e.forge(kubeconfig, args...)
	if code != 0 {
		e.t.Fatalf("forge %s: exit %d\n%s", strings.Join(args, " "), code, out)
	}
	return out
}

// kubectl runs against the test's cluster as its admin.
func (e *fluxDeliveryE2E) kubectl(args ...string) string {
	e.t.Helper()
	out, err := e.kubectlErr(args...)
	if err != nil {
		e.t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (e *fluxDeliveryE2E) kubectlErr(args ...string) (string, error) {
	cmd := exec.Command("kubectl", append([]string{"--context", e.kctx}, args...)...)
	cmd.Env = e.environ(e.adminConfig)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// pointerDigest is the bundle digest the live pointer names.
func (e *fluxDeliveryE2E) pointerDigest() string {
	e.t.Helper()
	return strings.TrimSpace(e.kubectl("get", "resourcesetinputproviders.fluxcd.controlplane.io",
		fluxdelivery.InputProviderName(fluxDeliveryE2EEnv, ""), "-n", e.deliveryNS,
		"-o", "jsonpath={.spec.defaultValues.source.digest}"))
}

// pointerSnapshot is the pointer's resourceVersion and digest.
func (e *fluxDeliveryE2E) pointerSnapshot() string {
	e.t.Helper()
	return strings.TrimSpace(e.kubectl("get", "resourcesetinputproviders.fluxcd.controlplane.io",
		fluxdelivery.InputProviderName(fluxDeliveryE2EEnv, ""), "-n", e.deliveryNS,
		"-o", "jsonpath={.metadata.resourceVersion} {.spec.defaultValues.source.digest}"))
}

// assertConverged reads every delivery Kustomization back from the cluster.
func (e *fluxDeliveryE2E) assertConverged(digest string) {
	e.t.Helper()
	var list struct {
		Items []struct {
			Metadata struct{ Name string } `json:"metadata"`
			Status   struct {
				LastAppliedRevision string `json:"lastAppliedRevision"`
				Conditions          []struct{ Type, Status string }
			} `json:"status"`
		} `json:"items"`
	}
	raw := e.kubectl("get", "kustomizations.kustomize.toolkit.fluxcd.io", "-n", e.deliveryNS, "-o", "json")
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		e.t.Fatal(err)
	}
	if len(list.Items) == 0 {
		e.t.Fatalf("no Kustomization in %s", e.deliveryNS)
	}
	for _, k := range list.Items {
		if revisionDigestOf(k.Status.LastAppliedRevision) != digest {
			e.t.Errorf("Kustomization %s applied %q, want %s", k.Metadata.Name, k.Status.LastAppliedRevision, digest)
		}
		ready := false
		for _, c := range k.Status.Conditions {
			ready = ready || (c.Type == "Ready" && c.Status == "True")
		}
		if !ready {
			e.t.Errorf("Kustomization %s is not Ready", k.Metadata.Name)
		}
	}
}

func (e *fluxDeliveryE2E) assertDeploymentAvailable(namespace, name string) {
	e.t.Helper()
	e.kubectl("wait", "--for=condition=Available", "--timeout=180s", "-n", namespace, "deployment/"+name)
}

// pointerOnlyKubeconfig is a kubeconfig for a ServiceAccount that may write the
// pointer and read what the deploy reads, in the delivery namespace only.
func (e *fluxDeliveryE2E) pointerOnlyKubeconfig() string {
	e.t.Helper()
	rbac := fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata: {name: deployer, namespace: %[1]s}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: deployer, namespace: %[1]s}
rules:
- apiGroups: [fluxcd.controlplane.io]
  resources: [resourcesetinputproviders]
  verbs: [get, list, watch, create, patch, update]
- apiGroups: [fluxcd.controlplane.io]
  resources: [resourcesets]
  verbs: [get, list, watch]
- apiGroups: [source.toolkit.fluxcd.io]
  resources: [ocirepositories]
  verbs: [get, list, watch]
- apiGroups: [kustomize.toolkit.fluxcd.io]
  resources: [kustomizations]
  verbs: [get, list, watch]
- apiGroups: [""]
  resources: [configmaps]
  resourceNames: [%[2]s]
  verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: deployer, namespace: %[1]s}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: deployer}
subjects: [{kind: ServiceAccount, name: deployer, namespace: %[1]s}]
`, e.deliveryNS, fluxdelivery.GrantsRecord)
	path := filepath.Join(e.t.TempDir(), "rbac.yaml")
	writeFluxE2EFile(e.t, path, rbac)
	e.kubectl("apply", "-f", path)
	token := strings.TrimSpace(e.kubectl("create", "token", "deployer", "-n", e.deliveryNS, "--duration", "2h"))

	var cfg struct {
		Clusters []struct {
			Cluster struct {
				Server string `json:"server"`
				CA     string `json:"certificate-authority-data"`
			} `json:"cluster"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal([]byte(e.kubectl("config", "view", "--raw", "--minify", "--flatten", "-o", "json")), &cfg); err != nil || len(cfg.Clusters) == 0 {
		e.t.Fatalf("read the admin kubeconfig's cluster: %v", err)
	}
	c := cfg.Clusters[0].Cluster
	pointerOnly := filepath.Join(e.t.TempDir(), "kubeconfig-pointer-only")
	writeFluxE2EFile(e.t, pointerOnly, fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: %[1]s
  cluster: {server: %[2]q, certificate-authority-data: %[3]q}
users:
- name: deployer
  user: {token: %[4]q}
contexts:
- name: %[1]s
  context: {cluster: %[1]s, user: deployer}
current-context: %[1]s
`, e.kctx, c.Server, c.CA, token))
	return pointerOnly
}

// ledgerLines counts the env's lines in one of the file ledger's records.
func (e *fluxDeliveryE2E) ledgerLines(kind string) int {
	e.t.Helper()
	n := 0
	for _, f := range e.ledgerFiles(kind) {
		raw, err := os.ReadFile(f)
		if err != nil {
			e.t.Fatal(err)
		}
		n += len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
	}
	return n
}

func (e *fluxDeliveryE2E) ledgerFiles(kind string) []string {
	var out []string
	_ = filepath.WalkDir(e.ledgerHome, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(p) == fluxDeliveryE2EEnv+".jsonl" && filepath.Base(filepath.Dir(p)) == kind {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// newestBundleDigest is the digest of the env's newest recorded bundle (the
// file ledger keeps every env's bundles in one bundles.jsonl).
func (e *fluxDeliveryE2E) newestBundleDigest() string {
	e.t.Helper()
	var files []string
	_ = filepath.WalkDir(e.ledgerHome, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(p) == "bundles.jsonl" {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 1 {
		e.t.Fatalf("want one bundles.jsonl under the ledger, found %v", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		e.t.Fatal(err)
	}
	newest := ""
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row struct {
			Env    string `json:"env"`
			Digest string `json:"digest"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			e.t.Fatalf("read a bundle row: %v\n%s", err, line)
		}
		if row.Env == fluxDeliveryE2EEnv {
			newest = row.Digest
		}
	}
	if newest == "" {
		e.t.Fatalf("no bundle recorded for %s", fluxDeliveryE2EEnv)
	}
	return newest
}

// registryHas reports whether the env's bundle repository holds digest.
func (e *fluxDeliveryE2E) registryHas(digest string) bool {
	e.t.Helper()
	url := fmt.Sprintf("http://localhost:%d/v2/fluxapp/bundle.v1/%s/manifests/%s", e.registryPort, fluxDeliveryE2EEnv, digest)
	req, _ := http.NewRequest(http.MethodHead, url, nil)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("ask the registry for %s: %v", digest, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// revokeWidgetGrant takes the Widget rule out of the pre identity's Role in the
// app namespace, behind forge's back.
func (e *fluxDeliveryE2E) revokeWidgetGrant() {
	e.t.Helper()
	name := e.deliveryNS + "-pre"
	var role map[string]any
	if err := json.Unmarshal([]byte(e.kubectl("get", "role", name, "-n", "app", "-o", "json")), &role); err != nil {
		e.t.Fatal(err)
	}
	rules, _ := role["rules"].([]any)
	var kept []any
	for _, r := range rules {
		groups, _ := r.(map[string]any)["apiGroups"].([]any)
		if len(groups) == 1 && groups[0] == "e2e.forge.dev" {
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) == len(rules) {
		e.t.Fatalf("the pre identity's Role in app holds no e2e.forge.dev rule to revoke: %v", rules)
	}
	role["rules"] = kept
	raw, _ := json.Marshal(role)
	path := filepath.Join(e.t.TempDir(), "role.json")
	writeFluxE2EFile(e.t, path, string(raw))
	e.kubectl("replace", "-f", path)
}

// kickPreTree asks kustomize-controller to reconcile the pre tree now, and
// returns only once it has HANDLED that request: status.lastHandledReconcileAt
// is set to the request's value when the reconciliation it started finishes.
// Without the wait, the next read could still see the previous
// reconciliation's verdict (Ready before a revocation, Forbidden before a
// heal) and pass or fail on it.
func (e *fluxDeliveryE2E) kickPreTree() {
	e.t.Helper()
	names := strings.Fields(e.kubectl("get", "kustomizations.kustomize.toolkit.fluxcd.io", "-n", e.deliveryNS,
		"-l", "forge.dev/phase=pre", "-o", "jsonpath={.items[*].metadata.name}"))
	if len(names) == 0 {
		e.t.Fatal("no pre Kustomization to reconcile")
	}
	e.kicks++
	token := fmt.Sprintf("forge-e2e-kick-%d", e.kicks)
	for _, n := range names {
		e.kubectl("annotate", "--overwrite", "kustomizations.kustomize.toolkit.fluxcd.io", n, "-n", e.deliveryNS,
			"reconcile.fluxcd.io/requestedAt="+token)
	}
	for _, n := range names {
		e.waitFor(4*time.Minute, "Kustomization "+n+" to handle reconcile request "+token, func() (bool, string) {
			out, _ := e.kubectlErr("get", "kustomizations.kustomize.toolkit.fluxcd.io", n, "-n", e.deliveryNS,
				"-o", "jsonpath={.status.lastHandledReconcileAt}")
			return strings.TrimSpace(out) == token, out
		})
	}
}

// waitFor polls done until it holds, failing the test when budget runs out.
func (e *fluxDeliveryE2E) waitFor(budget time.Duration, what string, done func() (bool, string)) {
	e.t.Helper()
	deadline := time.Now().Add(budget)
	for {
		ok, last := done()
		if ok {
			return
		}
		if !time.Now().Before(deadline) {
			e.t.Fatalf("waited %s for %s; last read: %s", budget, what, last)
		}
		time.Sleep(time.Second)
	}
}

// waitRendererEmpty waits for the delivery ResourceSet to reconcile its empty
// input set (its inventory empties: orphaned, not deleted).
func (e *fluxDeliveryE2E) waitRendererEmpty() {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		out, _ := e.kubectlErr("get", "resourcesets.fluxcd.controlplane.io", "delivery."+fluxDeliveryE2EEnv, "-n", e.deliveryNS,
			"-o", "jsonpath={.status.inventory.entries}")
		if strings.TrimSpace(out) == "" || strings.TrimSpace(out) == "[]" {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("the delivery ResourceSet still holds an inventory 3m after its pointer was deleted: %s", out)
		}
		time.Sleep(3 * time.Second)
	}
}

// teardown deletes the cluster, its registry and its network by exact name,
// with the test's own kubeconfig.
func (e *fluxDeliveryE2E) teardown() {
	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Env = e.environ(e.adminConfig)
		if out, err := cmd.CombinedOutput(); err != nil {
			e.t.Logf("teardown: %s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
	}
	run("k3d", "cluster", "delete", e.clusterName)
	run("k3d", "registry", "delete", "k3d-"+e.registryName)
	if exec.Command("docker", "network", "inspect", "k3d-"+e.clusterName).Run() == nil {
		run("docker", "network", "rm", "k3d-"+e.clusterName)
	}
}

// defaultKubeconfigContexts lists the contexts of the machine's kubeconfig,
// as the process environment resolves it.
func defaultKubeconfigContexts(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("kubectl", "config", "get-contexts", "-o", "name").Output()
	if err != nil {
		t.Logf("kubectl config get-contexts: %v (no machine kubeconfig to check)", err)
		return nil
	}
	return strings.Fields(string(out))
}
