package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// renderStatefulSetProject renders a real KCL project through the seam
// `forge env render`, `forge env deploy` and the bundle share (kclrender.Run,
// then cluster.ExtractManifests) and returns the applied stream by kind.
//
// The StatefulSet is a raw forge.Manifests object, which is how control-plane
// declares ClickHouse: the shape the resize Job exists for.
func renderStatefulSetProject(t *testing.T, bundleExtras string) map[string][]map[string]any {
	t.Helper()
	return renderStatefulSetProjectOn(t, bundleExtras, "gke_acme_prod")
}

// renderStatefulSetProjectOn is renderStatefulSetProject against a named
// cluster context.
func renderStatefulSetProjectOn(t *testing.T, bundleExtras, clusterContext string) map[string][]map[string]any {
	t.Helper()
	dir := t.TempDir()
	kclMod := "[package]\nname = \"stsresize\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"
	main := `import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "` + clusterContext + `"
    namespace = "observability-data"
    platform = "amd64"
}

_clickhouse = {
    apiVersion = "apps/v1"
    kind = "StatefulSet"
    metadata = {name = "clickhouse", namespace = "observability-data", labels = {"app.kubernetes.io/name" = "clickhouse"}}
    spec = {
        serviceName = "clickhouse"
        selector.matchLabels = {"app.kubernetes.io/name" = "clickhouse"}
        template = {
            metadata.labels = {"app.kubernetes.io/name" = "clickhouse"}
            spec.containers = [{name = "clickhouse", image = "docker.io/clickhouse/clickhouse-server:26.8"}]
        }
        volumeClaimTemplates = [{
            metadata.name = "data"
            spec = {
                accessModes = ["ReadWriteOnce"]
                storageClassName = "standard-rwo"
                resources.requests.storage = "100Gi"
            }
        }]
    }
}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _prod
` + bundleExtras + `
    manifests = [forge.Manifests {name = "clickhouse", delivery_unit = "obs", objects = [_clickhouse]}]
    delivery_units = [forge.DeliveryUnit {name = "obs"}]
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
`
	for f, c := range map[string]string{"kcl.mod": kclMod, "main.k": main} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return appliedObjects(t, out)
}

func TestStatefulSetResizeJob_ReachesTheAppliedStream(t *testing.T) {
	objs := renderStatefulSetProject(t, "")

	jobs := objs["Job"]
	if len(jobs) != 1 {
		t.Fatalf("Job count = %d, want exactly the resize Job", len(jobs))
	}
	job := jobs[0]
	meta := job["metadata"].(map[string]any)
	name := meta["name"].(string)
	if !strings.HasPrefix(name, "sts-volume-resize-obs-") {
		t.Fatalf("Job name = %q", name)
	}
	labels := meta["labels"].(map[string]any)
	if labels[release.LabelDeliveryUnit] != "obs" {
		t.Fatalf("the Job must be in the StatefulSet's delivery unit: %v", labels)
	}
	if meta["namespace"] != "observability-data" {
		t.Fatalf("namespace = %v", meta["namespace"])
	}
	ann := meta["annotations"].(map[string]any)
	if ann[release.AnnotationDeployPhase] != release.DeployPhasePreRollout {
		t.Fatalf("deploy phase = %v", ann)
	}
	named := func(kind string) map[string]any {
		for _, o := range objs[kind] {
			if o["metadata"].(map[string]any)["name"] == "sts-volume-resize-obs" {
				return o
			}
		}
		t.Fatalf("no %s named sts-volume-resize-obs in %v", kind, objs[kind])
		return nil
	}
	for _, kind := range []string{"ServiceAccount", "Role", "RoleBinding", "NetworkPolicy"} {
		named(kind)
	}
	if len(objs["StatefulSet"]) != 1 {
		t.Fatalf("StatefulSet count = %d", len(objs["StatefulSet"]))
	}

	// The Role is scoped to the one StatefulSet by name; it holds no
	// cluster-scoped permission.
	rules := named("Role")["rules"].([]any)
	stsRule := rules[0].(map[string]any)
	if got := stsRule["resourceNames"].([]any); len(got) != 1 || got[0] != "clickhouse" {
		t.Fatalf("StatefulSet rule is not pinned to the StatefulSet: %v", stsRule)
	}
	if len(objs["ClusterRole"]) != 0 || len(objs["ClusterRoleBinding"]) != 0 {
		t.Fatal("the resize Job needs no cluster-scoped permission")
	}

	// The image is the pinned default, not a floating tag.
	img := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"].(string)
	if !strings.Contains(img, "@sha256:") {
		t.Fatalf("default image %q is not digest-pinned", img)
	}
}

func TestStatefulSetResizeJob_ImageOverrideAndOptOut(t *testing.T) {
	objs := renderStatefulSetProject(t, `    statefulset_resize = forge.StatefulSetResize {image = "mirror.example/k8s:1.34"}`)
	img := objs["Job"][0]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"]
	if img != "mirror.example/k8s:1.34" {
		t.Fatalf("image = %v", img)
	}

	objs = renderStatefulSetProject(t, `    statefulset_resize = forge.StatefulSetResize {disabled = True}`)
	if len(objs["Job"]) != 0 {
		t.Fatalf("disabled still rendered a Job: %v", objs["Job"])
	}
	if len(objs["StatefulSet"]) != 1 {
		t.Fatal("opting out must leave the StatefulSet alone")
	}
}

func TestStatefulSetResizeJob_NotRenderedForALocalEnv(t *testing.T) {
	objs := renderStatefulSetProjectOn(t, `    lifecycle = "local"
    clusters = [forge.Cluster {name = "acme"}]`, "k3d-acme")
	if len(objs["Job"]) != 0 {
		t.Fatalf("a local env's scratch cluster is converged by the direct-apply recovery, not a Job: %v", objs["Job"])
	}
}
