package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// Bundle.overrides, end to end: KCL declaration → forge's Workload-record
// expansion → the patch landing on an object that DOES NOT EXIST in the KCL.
//
// That last part is the whole reason the apply is in Go. The `Deployment/api`
// these fixtures override is produced by pkg/deploy.RenderWorkloads inside
// cluster.ExtractManifests; a patch applied in KCL could not address it at
// all. So a test that overrode only the Namespace would pass against a
// KCL-side implementation and prove nothing about the one that ships.

const overridesMainK = `
import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-ovr"
    namespace = "acme-dev"
}

output = forge.render(forge.Bundle {
    project = "acme"
    cluster_target = _target
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnCluster {target = _target}
    }]
    overrides = {
        "Deployment/api" = {spec.replicas = 10}
        "Namespace/acme-dev" = {metadata.labels = {
            "pod-security.kubernetes.io/enforce" = "baseline"
        }}
    }
})
`

// The same env with no overrides, for the shape comparison.
var plainMainK = strings.Replace(overridesMainK, `    overrides = {
        "Deployment/api" = {spec.replicas = 10}
        "Namespace/acme-dev" = {metadata.labels = {
            "pod-security.kubernetes.io/enforce" = "baseline"
        }}
    }
`, "", 1)

func TestEnvRender_OverrideLandsOnExpandedDeployment(t *testing.T) {
	out := renderKCLProject(t, writeKCLProject(t, overridesMainK))
	stream, applied, err := cluster.ExtractManifestsWithOverrides(out)
	if err != nil {
		t.Fatalf("ExtractManifestsWithOverrides: %v", err)
	}

	byKind := appliedObjects(t, out)
	deps := byKind["Deployment"]
	if len(deps) != 1 {
		t.Fatalf("got %d Deployment(s), want 1 (expanded from the Workload record)", len(deps))
	}
	spec := deps[0]["spec"].(map[string]any)
	if got := spec["replicas"]; got != float64(10) && got != 10.0 {
		t.Errorf("Deployment/api spec.replicas = %#v, want 10 — the override did not land on the EXPANDED object", got)
	}
	// The expansion's own fields survive: the override merged, it did not
	// replace the Deployment.
	if _, ok := spec["selector"]; !ok {
		t.Errorf("Deployment lost spec.selector: the patch replaced the object instead of merging into it")
	}

	nss := byKind["Namespace"]
	if len(nss) != 1 {
		t.Fatalf("got %d Namespace(s), want 1", len(nss))
	}
	labels := nss[0]["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels["pod-security.kubernetes.io/enforce"] != "baseline" {
		t.Errorf("enforce = %#v, want baseline", labels["pod-security.kubernetes.io/enforce"])
	}
	// The sibling PSA labels forge stamps are untouched: a label-map patch
	// merges by key.
	if labels["pod-security.kubernetes.io/audit"] != "restricted" {
		t.Errorf("audit = %#v, want the unpatched restricted", labels["pod-security.kubernetes.io/audit"])
	}

	// The summary reports both, key → resolved object.
	if len(applied) != 2 {
		t.Fatalf("applied = %#v, want 2", applied)
	}
	var buf strings.Builder
	writeOverridesSummary(&buf, applied)
	for _, want := range []string{"overrides applied: 2", "Deployment/api", "Deployment/acme-dev/api", "Namespace/acme-dev"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("summary does not mention %q:\n%s", want, buf.String())
		}
	}
	if strings.Contains(stream, "forge.dev/v1alpha1") {
		t.Errorf("stream still carries an unexpanded record:\n%s", stream)
	}
}

// The SHAPE is post-override. `forge env shape` and the bundle both project
// from this stream, so if the override did not reach it the two would
// describe a different environment than the deploy applies — a release
// sealed against the wrong objects.
func TestEnvShape_ObjectHashMovesWithAnOverride(t *testing.T) {
	shapeOf := func(main string) release.Shape {
		t.Helper()
		out := renderKCLProject(t, writeKCLProject(t, main))
		stream, err := cluster.ExtractManifests(out)
		if err != nil {
			t.Fatalf("ExtractManifests: %v", err)
		}
		shape, err := bundle.ProjectShape(bundle.ShapeInput{
			Kind:      release.EnvSelfManaged,
			Clusters:  []string{"k3d-ovr"},
			Manifests: stream,
		})
		if err != nil {
			t.Fatalf("ProjectShape: %v", err)
		}
		return shape
	}

	plain, overridden := shapeOf(plainMainK), shapeOf(overridesMainK)

	hashes := func(s release.Shape) map[string]string {
		out := map[string]string{}
		for _, o := range s.Objects {
			out[o.Kind+"/"+o.Name] = o.Hash
		}
		return out
	}
	before, after := hashes(plain), hashes(overridden)
	if len(before) == 0 {
		t.Fatal("the un-overridden shape has no objects; the fixture is not exercising the projection")
	}
	for _, key := range []string{"Deployment/api", "Namespace/acme-dev"} {
		b, ok := before[key]
		if !ok {
			t.Fatalf("%s absent from the un-overridden shape (objects: %v)", key, before)
		}
		a, ok := after[key]
		if !ok {
			t.Fatalf("%s absent from the overridden shape (objects: %v)", key, after)
		}
		if a == b {
			t.Errorf("%s hash is unchanged (%s) — the shape did not see the override", key, a)
		}
	}
	// An object no override names keeps its hash: the patch is targeted, not
	// a whole-stream rewrite.
	if b, a := before["Service/api"], after["Service/api"]; b != "" && b != a {
		t.Errorf("Service/api hash moved (%s → %s) with no override naming it", b, a)
	}
}

// A render whose override names nothing FAILS, naming the key — the author
// hears about a typo'd key at render time rather than deploying an env that
// silently ignored it.
func TestEnvRender_RefusesOverrideThatMatchesNothing(t *testing.T) {
	main := strings.Replace(overridesMainK, `"Deployment/api" = {spec.replicas = 10}`, `"Deployment/nope" = {spec.replicas = 10}`, 1)
	out := renderKCLProject(t, writeKCLProject(t, main))
	_, err := cluster.ExtractManifests(out)
	if err == nil {
		t.Fatal("ExtractManifests succeeded; want a refusal naming the override key")
	}
	for _, want := range []string{"Deployment/nope", "matches no rendered object", "Deployment/acme-dev/api"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%v", want, err)
		}
	}
}
