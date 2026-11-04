package cluster

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A chart's rendered stream is self-describing: every namespaced object names
// the namespace the deploy will put it in, and a cluster-scoped object names
// none. Charts do not do this themselves (flux2 stamps 0 of 40 objects), so
// forge does it, and only where the object's scope is provable offline.

// docNamespaces decodes a stream into "Kind/name" -> metadata.namespace.
func docNamespaces(t *testing.T, stream string) map[string]string {
	t.Helper()
	out := map[string]string{}
	dec := yaml.NewDecoder(strings.NewReader(stream))
	for {
		var d struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d.Kind == "" {
			continue
		}
		out[d.Kind+"/"+d.Metadata.Name] = d.Metadata.Namespace
	}
	return out
}

func TestStampChartNamespace_StampsOnlyWhatItCanProveScopeOf(t *testing.T) {
	const widgetCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  scope: Namespaced
  names: {kind: Widget}
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: gadgets.example.com
spec:
  group: example.com
  scope: Cluster
  names: {kind: Gadget}`

	const stream = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: sa
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: keeps-declared
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: role
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: crole
---
apiVersion: v1
kind: Namespace
metadata:
  name: ns
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: hook
---
apiVersion: example.com/v1
kind: Widget
metadata:
  name: w
---
apiVersion: example.com/v1
kind: Gadget
metadata:
  name: g
---
apiVersion: other.example.com/v1
kind: Mystery
metadata:
  name: unknown-scope`

	got := docNamespaces(t, stampChartNamespace(stream, "demo", widgetCRD))
	want := map[string]string{
		"ServiceAccount/sa":                   "demo",
		"Deployment/web":                      "demo",
		"ConfigMap/keeps-declared":            "kube-system", // a declared namespace is never relocated
		"Role/role":                           "demo",
		"ClusterRole/crole":                   "",
		"Namespace/ns":                        "",
		"ValidatingWebhookConfiguration/hook": "",
		"Widget/w":                            "demo", // namespaced per the CRD in hand
		"Gadget/g":                            "",     // cluster-scoped per the CRD in hand
		// No CRD, not built in: scope is unprovable, so nothing is stamped
		// and the deploy's `kubectl apply -n` decides against the apiserver.
		"Mystery/unknown-scope": "",
	}
	for k, w := range want {
		if g, ok := got[k]; !ok || g != w {
			t.Errorf("%s: namespace = %q (present=%v), want %q", k, g, ok, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("stamping changed the object set: got %v", got)
	}
}

func TestStampChartNamespace_EmptyNamespaceIsANoOp(t *testing.T) {
	const stream = "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: sa"
	if got := stampChartNamespace(stream, "", ""); got != stream {
		t.Errorf("no namespace to stamp must return the stream untouched, got:\n%s", got)
	}
}

// fakeHelmByChart installs a `helm` whose output depends on the release name
// (argv[2]) and on --include-crds, so one test can render two charts.
func fakeHelmByChart(t *testing.T, templates, crds map[string]string) {
	t.Helper()
	requirePOSIXFake(t, "helm")
	dir := t.TempDir()
	var script strings.Builder
	script.WriteString("#!/bin/sh\nname=\"$2\"\ncase \" $* \" in\n*' --include-crds '*) kind=crds ;;\n*) kind=templates ;;\nesac\ncat \"" + dir + "/$kind.$name.yaml\" 2>/dev/null\nexit 0\n")
	write := func(kind string, m map[string]string) {
		for name, body := range m {
			if err := os.WriteFile(filepath.Join(dir, kind+"."+name+".yaml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("templates", templates)
	write("crds", crds)
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(script.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const (
	fluxNamespaceLess = fluxShapedRender + `
---
apiVersion: v1
kind: Service
metadata:
  name: already-stamped
  namespace: flux-system
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-webhooks`

	podinfoRender = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: podinfo
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: podinfo
---
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: podinfo-hook
---
apiVersion: example.com/v1
kind: Widget
metadata:
  name: podinfo-widget
---
apiVersion: example.com/v1
kind: Gadget
metadata:
  name: podinfo-gadget`

	podinfoCRDs = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  scope: Namespaced
  names: {kind: Widget}
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: gadgets.example.com
spec:
  group: example.com
  scope: Cluster
  names: {kind: Gadget}`
)

// RenderChartStreams is what `forge env render` prints. Every namespaced
// object in it must carry the namespace its chart deploys into — the chart's
// own, per chart, not one shared value — and cluster-scoped ones none.
func TestRenderChartStreams_NamespacedObjectsCarryTheirChartNamespace(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake helm through shell subprocesses; runs in task test")
	}
	fakeHelmByChart(t,
		map[string]string{"flux": fluxNamespaceLess, "podinfo": podinfoRender},
		map[string]string{"podinfo": podinfoCRDs},
	)
	streams, err := RenderChartStreams(context.Background(), []HelmChartSpec{
		{Name: "flux", OCI: "oci://example/flux", Version: "1", Namespace: "flux-system", Owner: chartOwnerFixture, Prune: true},
		{Name: "podinfo", OCI: "oci://example/podinfo", Version: "1", Namespace: "podinfo-system", Owner: chartOwnerFixture, Prune: true},
	})
	if err != nil {
		t.Fatalf("RenderChartStreams: %v", err)
	}
	want := map[string]map[string]string{
		"flux": {
			"Namespace/flux-system": "",
			"CustomResourceDefinition/kustomizations.kustomize.toolkit.fluxcd.io": "",
			"ServiceAccount/source-controller":                                    "flux-system",
			"ConfigMap/flux-config":                                               "flux-system",
			"Service/source-controller":                                           "flux-system",
			"Service/already-stamped":                                             "flux-system",
			"Deployment/source-controller":                                        "flux-system",
			"NetworkPolicy/allow-webhooks":                                        "flux-system",
			"ClusterRole/crd-controller":                                          "",
			"ClusterRoleBinding/cluster-reconciler":                               "",
		},
		"podinfo": {
			"Namespace/podinfo-system":                     "",
			"CustomResourceDefinition/widgets.example.com": "",
			"CustomResourceDefinition/gadgets.example.com": "",
			"Deployment/podinfo":                           "podinfo-system",
			"HorizontalPodAutoscaler/podinfo":              "podinfo-system",
			"MutatingWebhookConfiguration/podinfo-hook":    "",
			"Widget/podinfo-widget":                        "podinfo-system",
			"Gadget/podinfo-gadget":                        "",
		},
	}
	if len(streams) != len(want) {
		t.Fatalf("rendered %d charts, want %d", len(streams), len(want))
	}
	for _, cs := range streams {
		got := docNamespaces(t, cs.Stream)
		for k, w := range want[cs.Name] {
			if g, ok := got[k]; !ok || g != w {
				t.Errorf("chart %s: %s namespace = %q (present=%v), want %q", cs.Name, k, g, ok, w)
			}
		}
		if len(got) != len(want[cs.Name]) {
			t.Errorf("chart %s: object set = %v", cs.Name, got)
		}
	}
}

// A consumer-declared manifest riding the chart is applied with the chart's
// `-n`, so it is stamped like the chart's own output.
func TestRenderChartStreams_RidingManifestsAreStamped(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake helm through shell subprocesses; runs in task test")
	}
	fakeHelmByChart(t, map[string]string{"podinfo": podinfoRender}, nil)
	streams, err := RenderChartStreams(context.Background(), []HelmChartSpec{{
		Name: "podinfo", OCI: "oci://example/podinfo", Version: "1", Namespace: "podinfo-system", Owner: chartOwnerFixture,
		Manifests: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: riding\n---\napiVersion: cert-manager.io/v1\nkind: ClusterIssuer\nmetadata:\n  name: issuer\n",
	}})
	if err != nil {
		t.Fatalf("RenderChartStreams: %v", err)
	}
	got := docNamespaces(t, streams[0].Stream)
	if got["ConfigMap/riding"] != "podinfo-system" {
		t.Errorf("riding ConfigMap namespace = %q, want podinfo-system", got["ConfigMap/riding"])
	}
	if got["ClusterIssuer/issuer"] != "" {
		t.Errorf("riding ClusterIssuer is cluster-scoped, got namespace %q", got["ClusterIssuer/issuer"])
	}
}

// The deploy applies the bytes render shows, and those bytes now stand on
// their own: the chart lands in its namespace through the deploy's `-n`, and
// the same stream applied by hand with NO namespace flag lands there too.
func TestApplyRenderedCharts_StampedStreamLandsInTheChartNamespace(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake helm and kubectl through shell subprocesses; runs in task test")
	}
	cl := newFakeCluster(t)
	fakeHelmByChart(t, map[string]string{"flux": fluxNamespaceLess}, nil)

	spec := HelmChartSpec{Name: "flux", OCI: "oci://example/flux", Version: "1", Namespace: "flux-system"}
	charts, err := renderSelectedCharts(context.Background(), []HelmChartSpec{spec})
	if err != nil {
		t.Fatalf("renderSelectedCharts: %v", err)
	}
	if err := applyRenderedCharts(context.Background(), "k3d-test", charts, true); err != nil {
		t.Fatalf("applyRenderedCharts: %v", err)
	}
	for _, o := range [][3]string{
		{"Deployment", "flux-system", "source-controller"},
		{"ConfigMap", "flux-system", "flux-config"},
		{"NetworkPolicy", "flux-system", "allow-webhooks"},
		{"ClusterRole", "", "crd-controller"},
	} {
		if !cl.has(o[0], o[1], o[2]) {
			t.Errorf("deploy did not land %s/%s in namespace %q", o[0], o[2], o[1])
		}
	}
	if cl.has("Deployment", "default", "source-controller") {
		t.Errorf("source-controller landed in the default namespace")
	}

	// By hand, no -n: the case the rendered stream has to survive.
	byHand := newFakeCluster(t)
	if err := KubectlApply(context.Background(), "k3d-test", charts[0].manifests); err != nil {
		t.Fatalf("KubectlApply: %v", err)
	}
	if !byHand.has("Deployment", "flux-system", "source-controller") || byHand.has("Deployment", "default", "source-controller") {
		t.Errorf("a hand apply with no -n must land source-controller in flux-system, state: %+v", byHand.state())
	}
	if !byHand.has("ClusterRole", "", "crd-controller") {
		t.Errorf("a hand apply must leave ClusterRole cluster-scoped")
	}
}

// A bundle-delivery chart rides the env's stream and no `-n` is applied to
// it later, so its namespace-less objects must be stamped at render time.
func TestBundledChart_NamespacedObjectsAreStamped(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake helm through shell subprocesses; runs in task test")
	}
	fakeHelm(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n---\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: crole\n")
	raw := byoOutput(t, map[string]any{"bundled_charts": []any{map[string]any{
		"name": "shop", "chart": "shop", "repo": "https://charts.example", "version": "1.2.3", "namespace": "acme-shop",
	}}})
	stream, err := extract(t, "", raw)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	got := docNamespaces(t, stream)
	if got["Deployment/web"] != "acme-shop" {
		t.Errorf("bundled chart Deployment namespace = %q, want acme-shop (the chart's, not the env's)", got["Deployment/web"])
	}
	if got["ClusterRole/crole"] != "" {
		t.Errorf("bundled chart ClusterRole must stay cluster-scoped, got %q", got["ClusterRole/crole"])
	}
}
