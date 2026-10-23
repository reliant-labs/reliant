package cluster

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/deploy/eso"
)

// These tests render forge.ExternalSecretsOperator THE WAY A DEPLOY DOES — KCL,
// the KCL contract, helm template of the real chart, forge's stamping — and
// assert on the objects that would reach the cluster. The KCL fixtures pin the
// declaration; only this layer sees the chart rename a value, an object or a
// template condition so that a default silently stops doing anything.
//
// They pull the chart (OCI, ghcr.io): skipped under -short and without helm.

const esoHardeningMain = `import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "fixture"
    env = "prod"
    cluster_target = _hub
    external_secrets_operators = [forge.ExternalSecretsOperator {
        cluster = _hub
        %s
    }]
})
`

type esoRender struct {
	docs   []map[string]any
	stream string
}

func renderESOThroughHelm(t *testing.T, operatorArgs string) esoRender {
	t.Helper()
	if testing.Short() {
		t.Skip("pulls the external-secrets chart over the network; runs in task test")
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"kcl.mod": "[package]\nname = \"esohardening\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k":  strings.Replace(esoHardeningMain, "%s", operatorArgs, 1),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var contract struct {
		Output struct {
			HelmCharts []struct {
				Name            string            `json:"name"`
				OCI             string            `json:"oci"`
				Version         string            `json:"version"`
				Namespace       string            `json:"namespace"`
				CRDs            string            `json:"crds"`
				Cluster         string            `json:"cluster"`
				ClusterTarget   bool              `json:"cluster_target"`
				Values          map[string]any    `json:"values"`
				NamespaceLabels map[string]string `json:"namespace_labels"`
			} `json:"helm_charts"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &contract); err != nil || len(contract.Output.HelmCharts) != 1 {
		t.Fatalf("contract: %v (%d charts)\n%.600s", err, len(contract.Output.HelmCharts), out)
	}
	c := contract.Output.HelmCharts[0]
	if c.CRDs != "external-secrets" || c.Cluster != "gke_acme_prod" || !c.ClusterTarget {
		t.Fatalf("chart entity = crds %q cluster %q target %v", c.CRDs, c.Cluster, c.ClusterTarget)
	}
	crds, err := eso.CRDs(c.Version, "fixture.prod")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := RenderHelmChart(context.Background(), HelmChartSpec{
		Name: c.Name, OCI: c.OCI, Version: c.Version, Namespace: c.Namespace, Values: c.Values,
		NamespaceLabels: c.NamespaceLabels, Owner: "fixture.prod", Prune: true,
	})
	if err != nil {
		t.Fatalf("RenderHelmChart: %v", err)
	}
	r := esoRender{stream: stream}
	for _, s := range []string{crds, stream} {
		dec := yaml.NewDecoder(strings.NewReader(s))
		for {
			var d map[string]any
			if err := dec.Decode(&d); err != nil {
				break
			}
			if d != nil {
				r.docs = append(r.docs, d)
			}
		}
	}
	return r
}

func (r esoRender) kinds(kind string) []map[string]any {
	var out []map[string]any
	for _, d := range r.docs {
		if d["kind"] == kind {
			out = append(out, d)
		}
	}
	return out
}

func nameOf(d map[string]any) string {
	md, _ := d["metadata"].(map[string]any)
	n, _ := md["name"].(string)
	return n
}

func labelsOf(d map[string]any) map[string]any {
	md, _ := d["metadata"].(map[string]any)
	l, _ := md["labels"].(map[string]any)
	return l
}

// rules flattens every RBAC rule of every Role and ClusterRole.
func (r esoRender) rules() []map[string]any {
	var out []map[string]any
	for _, kind := range []string{"Role", "ClusterRole"} {
		for _, d := range r.kinds(kind) {
			rs, _ := d["rules"].([]any)
			for _, x := range rs {
				if m, ok := x.(map[string]any); ok {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

func strs(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want || s == "*" {
			return true
		}
	}
	return false
}

// The RBAC invariants of the design (§3.2 "Who may drive it"). Anything that
// widens the grant the install makes must fail one of these.
func TestESOChart_RBACInvariants(t *testing.T) {
	r := renderESOThroughHelm(t, "")

	for _, rule := range r.rules() {
		res, verbs, groups := strs(rule["resources"]), strs(rule["verbs"]), strs(rule["apiGroups"])
		// No grant of pods/exec (or pods/attach, pods/portforward) to anyone.
		for _, sub := range []string{"pods/exec", "pods/attach", "pods/portforward", "pods/*"} {
			if has(res, sub) && sub != "*" {
				t.Errorf("rule grants %s: %v", sub, rule)
			}
		}
		for _, x := range res {
			if x == "*" || x == "pods/exec" {
				t.Errorf("rule names resource %q: %v", x, rule)
			}
		}
		// serviceaccounts/token create is not granted by the chart: SF5 renders
		// per-ServiceAccount Roles with resourceNames instead.
		if has(res, "serviceaccounts/token") {
			t.Errorf("chart grants serviceaccounts/token (rbac.serviceAccountTokenCreate must be off): %v", rule)
		}
		// Nothing impersonates, escalates or binds.
		for _, v := range verbs {
			if v == "impersonate" || v == "escalate" || v == "bind" || v == "*" {
				t.Errorf("rule grants verb %q: %v", v, rule)
			}
		}
		// No rule reaches a kind the install does not process.
		if has(groups, "external-secrets.io") {
			for _, k := range []string{"clustersecretstores", "clusterexternalsecrets", "pushsecrets", "clusterpushsecrets"} {
				if has(res, k) {
					t.Errorf("rule grants %s although that kind is not processed: %v", k, rule)
				}
			}
		}
		if has(groups, "generators.external-secrets.io") {
			for _, k := range []string{"clustergenerators"} {
				if has(res, k) {
					t.Errorf("rule grants %s: %v", k, rule)
				}
			}
		}
	}

	// No aggregation into view/edit/admin, on any ClusterRole.
	for _, d := range r.kinds("ClusterRole") {
		for k := range labelsOf(d) {
			if strings.HasPrefix(k, "rbac.authorization.k8s.io/aggregate-to-") {
				t.Errorf("ClusterRole %s aggregates into a default role (%s): namespace admins would inherit access to ExternalSecrets and SecretStores", nameOf(d), k)
			}
		}
		if n := nameOf(d); strings.HasSuffix(n, "-servicebindings") {
			t.Errorf("ClusterRole %s is rendered; rbac.servicebindings.create must be off", n)
		}
	}

	// No binding hands the operator's ServiceAccount (or anyone) a built-in
	// powerful role, and no subject outside the operator namespace is bound to
	// an ESO role.
	for _, kind := range []string{"RoleBinding", "ClusterRoleBinding"} {
		for _, b := range r.kinds(kind) {
			ref, _ := b["roleRef"].(map[string]any)
			name, _ := ref["name"].(string)
			if name == "cluster-admin" || name == "admin" || name == "edit" || name == "view" {
				t.Errorf("%s %s binds the built-in role %s", kind, nameOf(b), name)
			}
			subs, _ := b["subjects"].([]any)
			for _, s := range subs {
				m, _ := s.(map[string]any)
				if m["kind"] != "ServiceAccount" || m["namespace"] != eso.Namespace {
					t.Errorf("%s %s binds a subject outside the operator's ServiceAccount: %v", kind, nameOf(b), m)
				}
			}
		}
	}
}

// The webhook and the cert controller are not installed (P1), and no CRD of an
// unused kind is applied.
func TestESOChart_ExactObjectSet(t *testing.T) {
	r := renderESOThroughHelm(t, "")

	for _, kind := range []string{"ValidatingWebhookConfiguration", "MutatingWebhookConfiguration", "Service", "Certificate", "Secret", "ServiceMonitor", "ConfigMap"} {
		if got := r.kinds(kind); len(got) != 0 {
			t.Errorf("%d %s rendered; the hardened install has none (webhook, cert controller and metrics service are off)", len(got), kind)
		}
	}
	deps := r.kinds("Deployment")
	if len(deps) != 1 || nameOf(deps[0]) != "external-secrets" {
		t.Fatalf("Deployments = %v, want exactly external-secrets (no webhook or cert-controller)", deps)
	}
	var crds []string
	for _, c := range r.kinds("CustomResourceDefinition") {
		crds = append(crds, nameOf(c))
	}
	if strings.Join(crds, ",") != strings.Join(eso.CRDNames, ",") {
		t.Errorf("CRDs = %v, want exactly %v (the chart's own CRD templates must stay off)", crds, eso.CRDNames)
	}
	var vaps, bindings []string
	for _, p := range r.kinds("ValidatingAdmissionPolicy") {
		vaps = append(vaps, nameOf(p))
	}
	for _, p := range r.kinds("ValidatingAdmissionPolicyBinding") {
		bindings = append(bindings, nameOf(p))
	}
	sort.Strings(vaps)
	sort.Strings(bindings)
	want := "eso-es-policy,eso-store-pin,eso-writers"
	if strings.Join(vaps, ",") != want || strings.Join(bindings, ",") != want {
		t.Errorf("policies %v bindings %v, want %s", vaps, bindings, want)
	}
	// Every object forge renders carries the owner stamp, so the single-owner
	// check and a prune can find it.
	for _, d := range r.docs {
		if d["kind"] == "Namespace" {
			continue
		}
		if labelsOf(d)[eso.ComponentOwnerLabel] == nil {
			t.Errorf("%s/%s carries no %s label", d["kind"], nameOf(d), eso.ComponentOwnerLabel)
		}
	}
}

func TestESOChart_PodIsRestrictedAndPinned(t *testing.T) {
	r := renderESOThroughHelm(t, "")
	dep := r.kinds("Deployment")[0]
	spec := dep["spec"].(map[string]any)
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	c := pod["containers"].([]any)[0].(map[string]any)

	image, _ := c["image"].(string)
	if !strings.Contains(image, "@sha256:") || !strings.HasPrefix(image, eso.ImageRepository+":"+eso.ImageTag+"@sha256:") {
		t.Errorf("image %q is not the pinned digest", image)
	}
	sc := c["securityContext"].(map[string]any)
	if sc["allowPrivilegeEscalation"] != false || sc["readOnlyRootFilesystem"] != true || sc["runAsNonRoot"] != true {
		t.Errorf("container securityContext is not restricted: %v", sc)
	}
	if caps, _ := sc["capabilities"].(map[string]any); strs(caps["drop"])[0] != "ALL" {
		t.Errorf("capabilities not dropped: %v", sc)
	}
	if seccomp, _ := sc["seccompProfile"].(map[string]any); seccomp["type"] != "RuntimeDefault" {
		t.Errorf("seccompProfile = %v", seccomp)
	}
	if pod["hostNetwork"] != false {
		t.Errorf("hostNetwork = %v", pod["hostNetwork"])
	}
	args := strings.Join(strs(c["args"]), " ")
	for _, want := range []string{
		"--enable-cluster-store-reconciler=false", "--enable-cluster-external-secret-reconciler=false",
		"--enable-cluster-push-secret-reconciler=false", "--enable-push-secret-reconciler=false",
		"--enable-secrets-caching=false", "--enable-configmaps-caching=false", "--enable-managed-secrets-caching=true",
		"--enable-flood-gate=true", "--unsafe-allow-generic-targets=false", "--enable-leader-election=true",
		"--tls-min-version=1.2",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("controller args lack %s: %s", want, args)
		}
	}
	if spec["replicas"] != 2 {
		t.Errorf("replicas = %v, want 2", spec["replicas"])
	}
	// Off the kata pool.
	if !strings.Contains(mustJSON(pod["affinity"]), `"reliant.dev/runtime"`) {
		t.Errorf("pod is not kept off the kata pool: %v", pod["affinity"])
	}
	// The ServiceAccount is the one the writers policy names.
	if pod["serviceAccountName"] != "external-secrets" {
		t.Errorf("serviceAccountName = %v", pod["serviceAccountName"])
	}
}

func TestESOChart_NetworkPolicyDeniesByDefault(t *testing.T) {
	r := renderESOThroughHelm(t, `admission = forge.ESOAdmission {egress_cidrs = ["10.0.0.0/8"], scrape_namespaces = ["monitoring"]}`)
	nps := r.kinds("NetworkPolicy")
	if len(nps) != 1 {
		t.Fatalf("NetworkPolicies = %d, want 1", len(nps))
	}
	spec := nps[0]["spec"].(map[string]any)
	if got := strings.Join(strs(spec["policyTypes"]), ","); got != "Egress,Ingress" {
		t.Errorf("policyTypes = %s: both directions must be restricted", got)
	}
	egress := mustJSON(spec["egress"])
	if !strings.Contains(egress, `"10.0.0.0/8"`) || strings.Contains(egress, "0.0.0.0/0") {
		t.Errorf("egress is not bounded by egress_cidrs: %s", egress)
	}
	ing := mustJSON(spec["ingress"])
	if !strings.Contains(ing, `"monitoring"`) || !strings.Contains(ing, "8080") {
		t.Errorf("ingress is not metrics-from-monitoring only: %s", ing)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
