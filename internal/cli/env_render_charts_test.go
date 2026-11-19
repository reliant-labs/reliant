package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/kclplugin"
)

// fakeHelmOnPath installs a `helm` that renders a fixed chart: a Deployment
// (and a post-install hook Job, which deploy drops) for `template
// --skip-crds`, and one chart-owned CRD for `template --include-crds`. It
// records its argv so a test can assert the declared values reached it.
// Returns the argv log path.
func fakeHelmOnPath(t *testing.T) string {
	t.Helper()
	requirePOSIXFake(t, "helm")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "helm.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + logPath + `
case " $* " in
*' --include-crds '*)
cat <<'EOF'
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: gitrepositories.source.toolkit.fluxcd.io
spec:
  group: source.toolkit.fluxcd.io
EOF
;;
*)
cat <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: source-controller
  namespace: flux-system
---
apiVersion: batch/v1
kind: Job
metadata:
  name: post-install-check
  namespace: flux-system
  annotations:
    helm.sh/hook: post-install
EOF
;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// writeChartRenderProject writes a prod env with one app workload and one
// declared forge.HelmChart (flux), the shape of control-plane's prod.
func writeChartRenderProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("forge.yaml", "name: charttest\nmodule_path: github.com/example/charttest\nversion: \"0.1.0\"\n")
	write("deploy/kcl/kcl.mod", "[package]\nname = \"charttest-deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n")
	write("deploy/kcl/prod/main.k", `import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "gke_example_us-central1_prod"
    namespace = "charttest-prod"
    platform = "amd64"
}

_bundle = forge.Bundle {
    project = "charttest"
    cluster_target = _target
    workloads = [fw.Workload {
        name = "api"
        image = "reg.example.com/charttest"
        runtime = forge.OnCluster {target = _target}
    }]
    helm_charts = [forge.HelmChart {
        name = "flux"
        oci = "oci://ghcr.io/fluxcd-community/charts/flux2"
        version = "2.14.1"
        namespace = "flux-system"
        values = {sourceController.create = True}
        namespace_labels = {"pod-security.kubernetes.io/enforce" = "restricted"}
    }]
}

output = forge.render(_bundle)
`)
	return dir
}

// TestEnvRender_IncludesHelmChartObjects is the F5 regression: `forge env
// render` printed 0 objects for every declared HelmChart while `forge env
// deploy` applied them, so render was not a preview of deploy.
//
// It drives the real command against a project declaring a chart, with a fake
// helm, and asserts the chart's objects are in the stream — labelled as chart
// output, attributed to the env's cluster, after the SAME post-hook drop and
// CRD merge a deploy performs (proof that it went through the deploy's render
// function rather than a second model of it).
//
// Mutation that fails it: drop the renderEnvCharts call in renderEnvTo.
func TestEnvRender_IncludesHelmChartObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and drives a fake helm through shell subprocesses; runs in task test")
	}
	kclplugin.Register()
	helmLog := fakeHelmOnPath(t)
	dir := writeChartRenderProject(t)

	stdout, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod")
	if err != nil {
		t.Fatalf("forge env render prod: %v\nstderr:\n%s", err, stderr)
	}
	docs := assertYAMLManifestStream(t, stdout)

	have := map[string]bool{}
	for _, d := range docs {
		have[d.Kind+"/"+d.Metadata.Name] = true
	}
	for _, want := range []string{
		"Deployment/api",               // the app, from KCL
		"Deployment/source-controller", // the chart's templated output
		"Namespace/flux-system",        // synthesized, as deploy does
		"CustomResourceDefinition/gitrepositories.source.toolkit.fluxcd.io", // chart-owned CRD
	} {
		if !have[want] {
			t.Errorf("render stream is missing %s — a deploy applies it. kinds/names: %v", want, have)
		}
	}
	// Deploy drops post-install hooks; a render that kept them would not be
	// the deploy's stream.
	if have["Job/post-install-check"] {
		t.Errorf("render kept a post-install hook Job that deploy drops — it is not rendering through the deploy's chart path")
	}

	// Labelled and attributed.
	if !strings.Contains(stdout, "# source: helm chart flux") {
		t.Errorf("chart documents must be labelled `# source: helm chart flux`:\n%s", stdout)
	}
	idx := strings.Index(stdout, "name: source-controller")
	head := stdout[:idx]
	if last := strings.LastIndex(head, "# cluster: "); last < 0 || !strings.HasPrefix(head[last:], "# cluster: gke_example_us-central1_prod") {
		t.Errorf("the chart Deployment must be attributed to the env's cluster:\n%s", stdout)
	}
	if !strings.Contains(stderr, "chart:     flux") {
		t.Errorf("summary should report the rendered chart, got stderr:\n%s", stderr)
	}

	// The declared values reached helm.
	argv, rerr := os.ReadFile(helmLog)
	if rerr != nil {
		t.Fatalf("fake helm never ran: %v", rerr)
	}
	if !strings.Contains(string(argv), "--values") || !strings.Contains(string(argv), "--version 2.14.1") {
		t.Errorf("helm was not invoked with the declared version and values:\n%s", argv)
	}
}

// TestEnvRender_NoChartsSaysWhatItLeftOut: --no-charts is the offline escape
// hatch, and a render that silently dropped the charts would recreate the
// defect. The summary must name what was left out, and helm must not run.
func TestEnvRender_NoChartsSaysWhatItLeftOut(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and drives a fake helm through shell subprocesses; runs in task test")
	}
	kclplugin.Register()
	helmLog := fakeHelmOnPath(t)
	dir := writeChartRenderProject(t)

	stdout, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod", "--no-charts")
	if err != nil {
		t.Fatalf("forge env render prod --no-charts: %v\nstderr:\n%s", err, stderr)
	}
	if strings.Contains(stdout, "source-controller") {
		t.Errorf("--no-charts must not render chart objects:\n%s", stdout)
	}
	if !strings.Contains(stderr, "NOT RENDERED (--no-charts): helm chart(s) flux") {
		t.Errorf("summary must name the charts --no-charts left out, got stderr:\n%s", stderr)
	}
	if _, serr := os.Stat(helmLog); serr == nil {
		t.Errorf("--no-charts must not invoke helm")
	}
}

// TestEnvRender_TargetSelectsOnlyThatChart: --target applies the deploy's
// selection rule to charts too — a service target renders no chart, a chart
// target renders only that chart.
func TestEnvRender_TargetSelectsOnlyThatChart(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and drives a fake helm through shell subprocesses; runs in task test")
	}
	kclplugin.Register()
	fakeHelmOnPath(t)
	dir := writeChartRenderProject(t)

	stdout, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod", "--target", "api")
	if err != nil {
		t.Fatalf("render --target api: %v\nstderr:\n%s", err, stderr)
	}
	if strings.Contains(stdout, "source-controller") {
		t.Errorf("--target api must not render the flux chart:\n%s", stdout)
	}

	stdout, stderr, err = runRenderCapturingProcessStdout(t, dir, "prod", "--target", "flux", "--list")
	if err != nil {
		t.Fatalf("render --target flux: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "source-controller") || !strings.Contains(stdout, "(helm chart flux)") {
		t.Errorf("--target flux --list must list the chart's objects, marked as chart output:\n%s", stdout)
	}
	if strings.Contains(stdout, "Deployment  charttest-prod  api") {
		t.Errorf("--target flux must not list the app:\n%s", stdout)
	}
}

// The chart's Namespace in `forge env render` carries the declared labels, and
// every chart document carries the owner stamp the prune keys on: the preview
// shows what the deploy will apply, including the metadata a re-apply
// converges.
//
// Mutation that fails it: stop passing the owner to renderEnvCharts, or drop
// the labels from namespaceManifest.
func TestEnvRender_ChartNamespaceCarriesLabelsAndOwnerStamp(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and drives a fake helm through shell subprocesses; runs in task test")
	}
	kclplugin.Register()
	fakeHelmOnPath(t)
	dir := writeChartRenderProject(t)

	stdout, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod")
	if err != nil {
		t.Fatalf("forge env render prod: %v\nstderr:\n%s", err, stderr)
	}
	assertYAMLManifestStream(t, stdout)

	var sawNS, sawController bool
	dec := yaml.NewDecoder(strings.NewReader(stdout))
	for {
		var d struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name   string            `yaml:"name"`
				Labels map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
		}
		if derr := dec.Decode(&d); derr != nil {
			break
		}
		switch d.Kind + "/" + d.Metadata.Name {
		case "Namespace/flux-system":
			sawNS = true
			if got := d.Metadata.Labels["pod-security.kubernetes.io/enforce"]; got != "restricted" {
				t.Errorf("the chart Namespace is missing its declared PSA label (got %q):\n%s", got, stdout)
			}
			if got := d.Metadata.Labels["forge.dev/chart-owner"]; got != "charttest.prod" {
				t.Errorf("the chart Namespace owner stamp = %q, want charttest.prod", got)
			}
		case "Deployment/source-controller":
			sawController = true
			if got := d.Metadata.Labels["forge.dev/chart-owner"]; got != "charttest.prod" {
				t.Errorf("chart Deployment owner stamp = %q, want charttest.prod", got)
			}
		}
	}
	if !sawNS || !sawController {
		t.Errorf("render is missing the chart Namespace or Deployment: ns=%v controller=%v\n%s", sawNS, sawController, stdout)
	}
}

// fakeHelmNamespaceLess installs a `helm` that, like flux2 (0 of 40 objects
// stamped), renders namespace-less objects. The output is keyed on the release
// name, so one project can declare two charts.
func fakeHelmNamespaceLess(t *testing.T) {
	t.Helper()
	requirePOSIXFake(t, "helm")
	dir := t.TempDir()
	script := `#!/bin/sh
case " $* " in *' --include-crds '*) exit 0 ;; esac
case "$2" in
flux)
cat <<'EOF2'
apiVersion: v1
kind: ServiceAccount
metadata:
  name: source-controller
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: source-controller
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: crd-controller
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cluster-reconciler
EOF2
;;
*)
cat <<'EOF2'
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-config
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cm-controller
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: cm-webhook
EOF2
;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// `forge env render` output is self-describing: every namespaced chart object
// carries the namespace its deploy puts it in (each chart's own), cluster-scoped
// objects carry none, so the stream can be applied by hand with no `-n`.
//
// Mutation that fails it: drop stampChartNamespace from renderSelectedCharts.
func TestEnvRender_ChartObjectsCarryTheirDeployNamespace(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and drives a fake helm through shell subprocesses; runs in task test")
	}
	kclplugin.Register()
	fakeHelmNamespaceLess(t)
	dir := writeChartRenderProject(t)
	main := filepath.Join(dir, "deploy/kcl/prod/main.k")
	src, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	withSecond := strings.Replace(string(src), "    helm_charts = [forge.HelmChart {", `    helm_charts = [forge.HelmChart {
        name = "cert-manager"
        oci = "oci://example/cert-manager"
        version = "1.0.0"
        namespace = "cert-manager"
    }, forge.HelmChart {`, 1)
	if withSecond == string(src) {
		t.Fatal("fixture edit did not apply")
	}
	if err := os.WriteFile(main, []byte(withSecond), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		target string
		want   map[string]string
	}{
		{"flux", map[string]string{
			"ServiceAccount/source-controller":      "flux-system",
			"Deployment/source-controller":          "flux-system",
			"Namespace/flux-system":                 "",
			"ClusterRole/crd-controller":            "",
			"ClusterRoleBinding/cluster-reconciler": "",
		}},
		{"cert-manager", map[string]string{
			"ConfigMap/cm-config":                       "cert-manager",
			"Deployment/cm-controller":                  "cert-manager",
			"Namespace/cert-manager":                    "",
			"ValidatingWebhookConfiguration/cm-webhook": "",
		}},
	}
	for _, tc := range cases {
		stdout, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod", "--target", tc.target)
		if err != nil {
			t.Fatalf("render --target %s: %v\nstderr:\n%s", tc.target, err, stderr)
		}
		got := map[string]string{}
		dec := yaml.NewDecoder(strings.NewReader(stdout))
		for {
			var d struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name      string `yaml:"name"`
					Namespace string `yaml:"namespace"`
				} `yaml:"metadata"`
			}
			if derr := dec.Decode(&d); derr != nil {
				break
			}
			got[d.Kind+"/"+d.Metadata.Name] = d.Metadata.Namespace
		}
		for k, w := range tc.want {
			if g, ok := got[k]; !ok || g != w {
				t.Errorf("--target %s: %s namespace = %q (present=%v), want %q\n%s", tc.target, k, g, ok, w, stdout)
			}
		}
		if len(got) != len(tc.want) {
			t.Errorf("--target %s: rendered %v", tc.target, got)
		}
	}
}
