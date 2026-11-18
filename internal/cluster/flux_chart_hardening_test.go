package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// These tests render forge.flux_chart() THE WAY A DEPLOY DOES — KCL, then the
// KCL contract, then helm template of the real Flux Operator chart, then
// forge's own patch step, plus the manifests riding the chart — and assert on
// the objects that would reach the cluster. The KCL fixtures
// (kcl/tests/positive_flux_chart_*.k) pin the declaration; only this layer can
// catch the chart renaming a value, an object, or a template condition so that
// a default silently stops doing anything.
//
// The Flux controllers themselves are rendered in-cluster by the operator from
// the FluxInstance. TestFluxInstance_OperatorBuildIsHardened checks that build
// with the operator's own CLI (`flux-operator build instance`) when one is on
// PATH (or named by FLUX_OPERATOR_CLI); it is AGPL-licensed, so forge runs it
// rather than linking its builder.
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
				Manifests       []map[string]any  `json:"manifests"`
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
	for _, m := range c.Manifests {
		b, err := yaml.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		stream += "\n---\n" + string(b)
	}
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

func TestFluxChart_OperatorRendersHardened(t *testing.T) {
	r := renderFluxThroughHelm(t, "")

	op := r.named("Deployment", "flux-operator")
	if op == nil {
		t.Fatal("Deployment/flux-operator must render")
	}
	spec := fluxPodSpec(r, "flux-operator")
	img := spec["containers"].([]any)[0].(map[string]any)["image"].(string)
	if !strings.HasSuffix(img, "@sha256:1d09a3f96524d0895fb11e8072d84a3dd029cecdec0f869efa371be6b001469c") {
		t.Errorf("the operator image must be pinned by digest, got %q", img)
	}
	if spec["enableServiceLinks"] != false {
		t.Errorf("the operator pod must not get service-link env vars: %v", spec["enableServiceLinks"])
	}
	if !fluxContains(fluxArgs(t, r, "flux-operator"), "--default-service-account=flux-delivery") {
		t.Errorf("a ResourceSet must impersonate a named ServiceAccount by default: %v", fluxArgs(t, r, "flux-operator"))
	}
	for _, c := range spec["containers"].([]any) {
		for _, e := range c.(map[string]any)["env"].([]any) {
			if env := e.(map[string]any); env["name"] == "WEB_SERVER_PORT" && env["value"] != "0" {
				t.Errorf("the operator web UI must be off: %v", env)
			}
		}
	}
	// The web UI's NetworkPolicy admits every namespace; the aggregation
	// hands namespace admins the operator's kinds.
	for _, d := range r.docs {
		md, _ := d["metadata"].(map[string]any)
		name, _ := md["name"].(string)
		if d["kind"] == "NetworkPolicy" && strings.Contains(name, "web") {
			t.Errorf("no web UI policy may render: %s", name)
		}
		if d["kind"] == "ClusterRole" && strings.Contains(fmt.Sprint(md["labels"]), "aggregate-to") {
			t.Errorf("no aggregated ClusterRole may render: %s", name)
		}
	}
	// The operator's CRDs ride the chart's templates, ResourceSets included.
	for _, crd := range []string{"fluxinstances", "fluxreports", "resourcesets", "resourcesetinputproviders"} {
		if !r.has("CustomResourceDefinition", crd+".fluxcd.controlplane.io") {
			t.Errorf("CRD %s.fluxcd.controlplane.io must render", crd)
		}
	}
	// The FluxInstance and forge's own objects ride the chart.
	fi := r.named("FluxInstance", "flux")
	if fi == nil {
		t.Fatal("FluxInstance/flux must ride the chart")
	}
	if a := fi["metadata"].(map[string]any)["annotations"].(map[string]any); a["kustomize.toolkit.fluxcd.io/prune"] != "disabled" {
		t.Errorf("the FluxInstance must carry forge's prune opt-out (deleting it uninstalls Flux): %v", a)
	}
	crd := r.named("ClusterRole", "crd-controller")
	if crd == nil {
		t.Fatal("crd-controller must ride the chart")
	}
	for _, rule := range crd["rules"].([]any) {
		res := strings.Join(fluxToStrings(rule.(map[string]any)["resources"]), ",")
		if strings.Contains(res, "secrets") || strings.Contains(res, "configmaps") {
			t.Errorf("crd-controller must not read Secrets/ConfigMaps cluster-wide: %v", rule)
		}
	}
	for _, name := range []string{"flux-tenant-reconciler", "flux-tenant-source"} {
		if !r.has("ClusterRole", name) {
			t.Errorf("ClusterRole/%s missing", name)
		}
	}
	if !r.has("ValidatingAdmissionPolicy", "flux-controller-bindings") || !r.has("PriorityClass", "forge-delivery") {
		t.Error("the binding guard and the PriorityClass must ride the chart")
	}
	ns := r.named("Namespace", "flux-system")
	if labels := ns["metadata"].(map[string]any)["labels"].(map[string]any); labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Errorf("flux-system Namespace labels = %v", labels)
	}
}

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

func TestFluxChart_PlacementReachesTheOperator(t *testing.T) {
	r := renderFluxThroughHelm(t, `placement = forge.flux_placement_delivery_v1(runtime_class_name = "gvisor")`)
	spec := fluxPodSpec(r, "flux-operator")
	if spec["runtimeClassName"] != "gvisor" {
		t.Errorf("runtimeClassName = %v", spec["runtimeClassName"])
	}
	if sel := spec["nodeSelector"].(map[string]any); sel["reliant.dev/pool"] != "delivery-v1" {
		t.Errorf("nodeSelector = %v", sel)
	}
}

// TestFluxInstance_OperatorBuildIsHardened builds the rendered FluxInstance
// with the operator's own builder and asserts on the Flux the operator would
// apply: exactly forge's flags, none of the distribution's grants, the shard
// on its own identity, and every CRD kept on an uninstall.
func TestFluxInstance_OperatorBuildIsHardened(t *testing.T) {
	cli := os.Getenv("FLUX_OPERATOR_CLI")
	if cli == "" {
		cli, _ = exec.LookPath("flux-operator")
	}
	if cli == "" {
		t.Skip("no flux-operator CLI (set FLUX_OPERATOR_CLI); see docs/runbooks/flux-patch-and-upgrade.md")
	}
	dns := `{"to" = [{podSelector.matchLabels = {"k8s-app" = "kube-dns"}}]}`
	r := renderFluxThroughHelm(t, `connected_shard = True, egress = {kustomize = [`+dns+`], source = [`+dns+`], kustomize_connected = [`+dns+`], operator = [`+dns+`]}`)
	fi, err := yaml.Marshal(r.named("FluxInstance", "flux"))
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "fluxinstance.yaml")
	if err := os.WriteFile(f, fi, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, cli, "build", "instance", "-f", f).Output()
	if err != nil {
		t.Fatalf("flux-operator build instance: %v", err)
	}
	b := renderedFlux{byKey: map[string]map[string]any{}}
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d != nil {
			b.docs = append(b.docs, d)
		}
	}
	for _, name := range []string{"cluster-reconciler-flux-system", "crd-controller-flux-system"} {
		if b.has("ClusterRoleBinding", name) || b.has("ClusterRole", name) {
			t.Errorf("the distribution's %s must be deleted from the build", name)
		}
	}
	if b.has("Deployment", "source-controller-connected") || b.has("Service", "source-controller-connected") {
		t.Error("the operator's source-controller shard must be deleted from the build")
	}
	for _, d := range b.docs {
		if d["kind"] != "CustomResourceDefinition" {
			continue
		}
		if a, _ := d["metadata"].(map[string]any)["annotations"].(map[string]any); a["fluxcd.controlplane.io/prune"] != "disabled" {
			t.Errorf("CRD %v must survive an uninstall", d["metadata"].(map[string]any)["name"])
		}
	}
	main := fluxArgs(t, b, "kustomize-controller")
	if main[len(main)-1] != "--watch-label-selector=sharding.fluxcd.io/key notin (connected)" || fluxContains(main, "--watch-label-selector=!sharding.fluxcd.io/key") {
		t.Errorf("the main controller must keep shard0 and unlabelled objects: %v", main)
	}
	if !fluxContains(main, "--default-service-account=reliant-deploy-tenant") {
		t.Errorf("main controller args = %v", main)
	}
	if src := fluxArgs(t, b, "source-controller"); strings.Contains(strings.Join(src, " "), "watch-label-selector") {
		t.Errorf("the source-controller must watch every source: %v", src)
	}
	shard := fluxPodSpec(b, "kustomize-controller-connected")
	if shard["serviceAccountName"] != "kustomize-controller-connected" {
		t.Errorf("the shard must run as its own ServiceAccount, got %v", shard["serviceAccountName"])
	}
	if strings.Contains(strings.Join(fluxArgs(t, b, "kustomize-controller-connected"), " "), "default-service-account") {
		t.Error("the shard must not pass --default-service-account")
	}
	for _, d := range []string{"kustomize-controller", "source-controller", "kustomize-controller-connected"} {
		if fluxPodSpec(b, d)["enableServiceLinks"] != false {
			t.Errorf("%s must not get service-link env vars", d)
		}
	}
}

func fluxContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func fluxToStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
