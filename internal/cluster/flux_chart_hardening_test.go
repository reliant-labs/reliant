package cluster

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// These tests render forge.flux_chart() THE WAY A DEPLOY DOES — KCL, then the
// KCL contract, then helm template of the real community chart, then forge's
// own patch step — and assert on the objects that would reach the cluster.
// The KCL fixtures (kcl/tests/positive_flux_chart_*.k) pin the declaration;
// only this layer can catch the chart renaming a value, an object, or a
// template condition so that a default silently stops doing anything.
//
// They pull the chart (OCI, from ghcr.io), so they skip under -short and when
// helm is absent.

const fluxHardeningMain = `import forge

_bundle = forge.Bundle {
    project = "fixture"
    env = "dev"
    helm_charts = [forge.flux_chart(%s)]
}

output = forge.render(_bundle)
`

type renderedFlux struct {
	byKey map[string]map[string]any // "Kind/namespace/name" (namespace "" for cluster-scoped)
	docs  []map[string]any
}

func renderFluxThroughHelm(t *testing.T, chartArgs string) renderedFlux {
	t.Helper()
	if testing.Short() {
		t.Skip("pulls the flux2 chart over the network; runs in task test")
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"kcl.mod": "[package]\nname = \"fluxhardening\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k":  strings.Replace(fluxHardeningMain, "%s", chartArgs, 1),
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
	// The same projection internal/cli reads (HelmChartEntity), minus the
	// fields this render does not use.
	var contract struct {
		Output struct {
			HelmCharts []struct {
				Name            string            `json:"name"`
				OCI             string            `json:"oci"`
				Version         string            `json:"version"`
				Namespace       string            `json:"namespace"`
				Values          map[string]any    `json:"values"`
				NamespaceLabels map[string]string `json:"namespace_labels"`
				Patches         map[string]any    `json:"patches"`
			} `json:"helm_charts"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &contract); err != nil || len(contract.Output.HelmCharts) != 1 {
		t.Fatalf("contract: %v (%d charts)\n%.600s", err, len(contract.Output.HelmCharts), out)
	}
	c := contract.Output.HelmCharts[0]
	stream, err := RenderHelmChart(context.Background(), HelmChartSpec{
		Name: c.Name, OCI: c.OCI, Version: c.Version, Namespace: c.Namespace, Values: c.Values,
		NamespaceLabels: c.NamespaceLabels, Patches: c.Patches, Owner: "fixture.dev", Prune: true,
	})
	if err != nil {
		t.Fatalf("RenderHelmChart: %v", err)
	}
	r := renderedFlux{byKey: map[string]map[string]any{}}
	dec := yaml.NewDecoder(strings.NewReader(stream))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d == nil {
			continue
		}
		md, _ := d["metadata"].(map[string]any)
		ns, _ := md["namespace"].(string)
		name, _ := md["name"].(string)
		kind, _ := d["kind"].(string)
		r.byKey[kind+"/"+ns+"/"+name] = d
		r.docs = append(r.docs, d)
	}
	return r
}

func (r renderedFlux) has(kind, name string) bool {
	for _, d := range r.docs {
		md, _ := d["metadata"].(map[string]any)
		if d["kind"] == kind && md["name"] == name {
			return true
		}
	}
	return false
}

func (r renderedFlux) named(kind, name string) map[string]any {
	for _, d := range r.docs {
		md, _ := d["metadata"].(map[string]any)
		if d["kind"] == kind && md["name"] == name {
			return d
		}
	}
	return nil
}

func fluxArgs(t *testing.T, r renderedFlux, deploy string) []string {
	t.Helper()
	d := r.named("Deployment", deploy)
	if d == nil {
		t.Fatalf("no Deployment/%s rendered", deploy)
	}
	var out []string
	for _, c := range d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any) {
		for _, a := range c.(map[string]any)["args"].([]any) {
			out = append(out, a.(string))
		}
	}
	return out
}

func fluxPodSpec(r renderedFlux, deploy string) map[string]any {
	return r.named("Deployment", deploy)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
}

func TestFluxChart_HardenedDefaultsReachTheRenderedObjects(t *testing.T) {
	r := renderFluxThroughHelm(t, "")

	// The chart's over-broad RBAC is gone; ours replaces it.
	if r.has("ClusterRole", "cluster-reconciler-impersonator") {
		t.Error("the chart's cluster-wide impersonator ClusterRole must not render")
	}
	if r.has("ClusterRoleBinding", "cluster-reconciler") {
		t.Error("the controllers must hold no cluster-admin binding")
	}
	crd := r.named("ClusterRole", "crd-controller")
	if crd == nil {
		t.Fatal("crd-controller must still exist (narrowed in place)")
	}
	for _, rule := range crd["rules"].([]any) {
		res := strings.Join(fluxToStrings(rule.(map[string]any)["resources"]), ",")
		if strings.Contains(res, "secrets") || strings.Contains(res, "configmaps") {
			t.Errorf("crd-controller must not read Secrets/ConfigMaps cluster-wide: %v", rule)
		}
	}
	for _, role := range []string{"flux-tenant-reconciler", "flux-tenant-source"} {
		if !r.has("ClusterRole", role) {
			t.Errorf("ClusterRole/%s missing", role)
		}
	}

	// Sizing and gates reach the container args, verbatim.
	kc := fluxArgs(t, r, "kustomize-controller")
	for _, want := range []string{
		"--concurrent=16", "--concurrent-ssa=8", "--requeue-dependency=10s",
		"--no-remote-bases=true", "--no-cross-namespace-refs=true",
		"--default-service-account=reliant-deploy-tenant",
		"--feature-gates=CancelHealthCheckOnNewRevision=true,DisableConfigWatchers=true",
	} {
		if !fluxContains(kc, want) {
			t.Errorf("kustomize-controller args missing %q: %v", want, kc)
		}
	}
	if !fluxContains(fluxArgs(t, r, "source-controller"), "--concurrent=8") {
		t.Errorf("source-controller must run --concurrent=8: %v", fluxArgs(t, r, "source-controller"))
	}
	if ps := fluxPodSpec(r, "kustomize-controller")["priorityClassName"]; ps != "forge-delivery" {
		t.Errorf("priorityClassName = %v", ps)
	}
	if !r.has("PriorityClass", "forge-delivery") {
		t.Error("the PriorityClass the pods name must be declared too: admission rejects a pod naming one that does not exist")
	}
	// No placement unless asked for.
	if _, ok := fluxPodSpec(r, "kustomize-controller")["runtimeClassName"]; ok {
		t.Error("runtimeClassName must not be set by default")
	}
	if _, ok := fluxPodSpec(r, "kustomize-controller")["nodeSelector"]; ok {
		t.Error("nodeSelector must not be set by default")
	}

	// Namespace PSA.
	ns := r.named("Namespace", "flux-system")
	labels := ns["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Errorf("flux-system Namespace labels = %v", labels)
	}
}

// The invariant: ingress to every flux-system pod only from flux-system pods.
func TestFluxChart_AllowEgressAdmitsIngressOnlyFromFluxSystem(t *testing.T) {
	for name, chartArgs := range map[string]string{
		"default":                 "",
		"with scrape namespaces":  `scrape_namespaces = ["observability", "monitoring"]`,
		"with delivery placement": `placement = forge.flux_placement_delivery_v1(runtime_class_name = "gvisor")`,
	} {
		t.Run(name, func(t *testing.T) {
			r := renderFluxThroughHelm(t, chartArgs)
			np := r.named("NetworkPolicy", "allow-egress")
			if np == nil {
				t.Fatal("NetworkPolicy/allow-egress must render")
			}
			spec := np["spec"].(map[string]any)
			if sel := spec["podSelector"].(map[string]any); len(sel) != 0 {
				t.Errorf("must select every flux-system pod, selects %v", sel)
			}
			ing := spec["ingress"].([]any)
			if len(ing) != 1 {
				t.Fatalf("exactly one ingress rule expected, got %v", ing)
			}
			from := ing[0].(map[string]any)["from"].([]any)
			if len(from) != 1 {
				t.Fatalf("exactly one ingress peer expected, got %v", from)
			}
			peer := from[0].(map[string]any)
			if _, ok := peer["namespaceSelector"]; ok {
				t.Errorf("the peer must be a podSelector in the policy's own namespace, not a namespaceSelector: %v", peer)
			}
			if _, ok := peer["ipBlock"]; ok {
				t.Errorf("the peer must not be an ipBlock: %v", peer)
			}
			if ps, ok := peer["podSelector"].(map[string]any); !ok || len(ps) != 0 {
				t.Errorf("the peer must be an empty podSelector, got %v", peer)
			}
			if _, ok := ing[0].(map[string]any)["ports"]; ok {
				t.Errorf("the ingress rule must not narrow to ports (that is allow-scraping's job): %v", ing[0])
			}
			if md := np["metadata"].(map[string]any); md["namespace"] != "flux-system" {
				t.Errorf("namespace = %v", md["namespace"])
			}
		})
	}
}

func TestFluxChart_NetworkPoliciesNegative(t *testing.T) {
	r := renderFluxThroughHelm(t, "")
	if r.has("NetworkPolicy", "allow-webhooks") {
		t.Error("allow-webhooks admits every namespace to a controller this posture does not install; it must not render")
	}
	if r.has("NetworkPolicy", "allow-scraping") {
		t.Error("allow-scraping must not render unless a namespace is named: the chart's own admits every namespace")
	}

	r = renderFluxThroughHelm(t, `scrape_namespaces = ["observability"]`)
	sc := r.named("NetworkPolicy", "allow-scraping")
	if sc == nil {
		t.Fatal("allow-scraping must render when a namespace is named")
	}
	b, _ := json.Marshal(sc["spec"])
	if strings.Contains(string(b), `"namespaceSelector":{}`) {
		t.Errorf("allow-scraping must never select every namespace: %s", b)
	}
	if !strings.Contains(string(b), `"observability"`) || !strings.Contains(string(b), "8080") {
		t.Errorf("allow-scraping must admit the named namespace on 8080: %s", b)
	}
}

func TestFluxChart_PlacementReachesThePods(t *testing.T) {
	r := renderFluxThroughHelm(t, `placement = forge.flux_placement_delivery_v1(runtime_class_name = forge.FLUX_RUNTIME_CLASS_GVISOR)`)
	for _, d := range []string{"kustomize-controller", "source-controller"} {
		ps := fluxPodSpec(r, d)
		if ps["runtimeClassName"] != "gvisor" {
			t.Errorf("%s runtimeClassName = %v", d, ps["runtimeClassName"])
		}
		if sel, _ := ps["nodeSelector"].(map[string]any); sel["reliant.dev/pool"] != "delivery-v1" {
			t.Errorf("%s nodeSelector = %v", d, ps["nodeSelector"])
		}
		tol, _ := ps["tolerations"].([]any)
		if len(tol) != 1 || tol[0].(map[string]any)["key"] != "reliant.dev/pool" || tol[0].(map[string]any)["effect"] != "NoSchedule" {
			t.Errorf("%s tolerations = %v", d, ps["tolerations"])
		}
		// A patch must not drop what the chart rendered: the containers stay.
		if cs, _ := ps["containers"].([]any); len(cs) != 1 {
			t.Errorf("%s containers = %v", d, cs)
		}
	}
	// And only those two pods: nothing else is a Pod template here.
	if sa := r.named("ServiceAccount", "kustomize-controller"); sa == nil {
		t.Error("ServiceAccount/kustomize-controller missing")
	}
}

func TestFluxChart_ChartRBACModeRestoresTheChartsOwn(t *testing.T) {
	r := renderFluxThroughHelm(t, `rbac = "chart", network_policies = "chart", pod_security = "", priority_class_name = "", priority_class_create = False`)
	for _, want := range [][2]string{{"ClusterRole", "crd-controller"}, {"ClusterRoleBinding", "crd-controller"}, {"ClusterRoleBinding", "cluster-reconciler"}} {
		if !r.has(want[0], want[1]) {
			t.Errorf("rbac = \"chart\" must leave the chart's %s/%s", want[0], want[1])
		}
	}
	for _, n := range []string{"allow-egress", "allow-scraping", "allow-webhooks"} {
		if !r.has("NetworkPolicy", n) {
			t.Errorf("network_policies = \"chart\" must leave the chart's %s", n)
		}
	}
	if r.has("ClusterRole", "flux-tenant-reconciler") || r.has("PriorityClass", "forge-delivery") {
		t.Error("opting out must not add forge's own objects")
	}
}

func TestFluxChart_PinnedModePinsImpersonationByName(t *testing.T) {
	r := renderFluxThroughHelm(t, `rbac = "pinned"`)
	imp := r.named("ClusterRole", "cluster-reconciler-impersonator")
	if imp == nil {
		t.Fatal("pinned mode must render the impersonator ClusterRole")
	}
	rule := imp["rules"].([]any)[0].(map[string]any)
	if got := fluxToStrings(rule["resourceNames"]); len(got) != 1 || got[0] != "reliant-deploy-tenant" {
		t.Errorf("impersonation must be pinned by resourceNames, got %v", rule)
	}
	if fluxContains(fluxArgs(t, r, "kustomize-controller"), "--feature-gates=CancelHealthCheckOnNewRevision=true,DisableConfigWatchers=true") {
		t.Error("pinned mode keeps the cluster-wide list, so the config watcher stays on")
	}
}

func fluxToStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func fluxContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
