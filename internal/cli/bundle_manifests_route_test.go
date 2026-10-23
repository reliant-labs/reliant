package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
)

// bundleManifestsMain is a two-cluster env whose raw objects come from
// Bundle.manifests: one unnamed group on the primary cluster, one NAMED group
// on the secondary (the kata pre-pull DaemonSet shape) and one named group on
// the primary. Each group's placement is DECLARED on the forge.Manifests
// entry; nothing is inferred from which workload happens to sit where.
const bundleManifestsMain = `
import forge
import forge.workloads as fw

_primary = forge.ClusterTarget {cluster = "k3d-cp", namespace = "cp-dev"}
_daemon = forge.ClusterTarget {cluster = "k3d-cp-daemon", namespace = "cp-dev"}
_build = forge.GoBuild {cmd = "./cmd/cp", output_name = "cp"}

output = forge.render(forge.Bundle {
    project = "cp"
    env = "dev"
    cluster_target = _primary
    workloads = [_w | {runtime = forge.OnCluster {target = _primary}} if not _w.runtime else _w for _w in [
        fw.Workload {name = "api", image = "localhost:5050/cp", build = _build, args = ["api"], ports = [fw.Port {name = "http", port = 8080}]}
        fw.Workload {name = "proxy", image = "localhost:5050/cp", build = _build, args = ["proxy"], ports = [fw.Port {name = "http", port = 8081}], runtime = forge.OnCluster {target = _daemon}}
    ]]
    manifests = [
        forge.Manifests {objects = [
            {apiVersion = "v1", kind = "ConfigMap", metadata.name = "shared-settings", data = {a = "b"}}
        ]}
        forge.Manifests {
            name = "kata-prepull"
            cluster = "k3d-cp-daemon"
            namespace = "kube-system"
            objects = [{
                apiVersion = "apps/v1", kind = "DaemonSet"
                metadata = {name = "workspace-base-prepull", labels = {"app.kubernetes.io/name" = "prepull"}}
                spec = {selector.matchLabels = {"app.kubernetes.io/name" = "prepull"}, template = {metadata.labels = {"app.kubernetes.io/name" = "prepull"}, spec.containers = [{name = "p", image = "localhost:5050/prepull:1"}]}}
            }]
        }
        forge.Manifests {
            name = "issuers"
            objects = [{apiVersion = "cert-manager.io/v1", kind = "ClusterIssuer", metadata.name = "letsencrypt", spec = {}}]
        }
    ]
})
`

// Bundle.manifests is the one home for raw objects, and its entries route by
// what they DECLARE:
//
//   - each object lands on exactly its group's cluster (forge.dev/cluster), in
//     its group's namespace when it names none, and a two-cluster env never
//     replicates a primary-cluster group onto the secondary;
//   - a NAMED group is its own `--target` (forge.dev/workload=<name>) even when
//     its objects carry their own app.kubernetes.io/name for a selector, and
//     the targeted deploy applies it to its declared cluster only.
func TestBundleManifests_RouteByClusterAndTargetByName(t *testing.T) {
	out := renderKCLProject(t, writeKCLProject(t, bundleManifestsMain), "env=dev")
	entities, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	stream, err := cluster.ExtractManifests(out)
	if err != nil {
		t.Fatalf("ExtractManifests: %v", err)
	}
	groups, err := buildDeployGroups("dev", entities, "cp-dev")
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	objects, clusters := attributeRenderedObjects(stream, groups, entities)
	if strings.Join(clusters, ",") != "k3d-cp,k3d-cp-daemon" {
		t.Fatalf("env clusters = %v, want [k3d-cp k3d-cp-daemon]", clusters)
	}

	want := map[string]struct {
		cluster, namespace, group string
	}{
		"ConfigMap/shared-settings":        {"k3d-cp", "cp-dev", ""},
		"DaemonSet/workspace-base-prepull": {"k3d-cp-daemon", "kube-system", "kata-prepull"},
		"ClusterIssuer/letsencrypt":        {"k3d-cp", "", "issuers"},
	}
	for _, o := range objects {
		w, ok := want[o.Kind+"/"+o.Name]
		if !ok {
			continue
		}
		delete(want, o.Kind+"/"+o.Name)
		if len(o.Clusters) != 1 || o.Clusters[0] != w.cluster {
			t.Errorf("%s/%s lands on %v, want only [%s]", o.Kind, o.Name, o.Clusters, w.cluster)
		}
		if o.Namespace != w.namespace {
			t.Errorf("%s/%s namespace = %q, want %q", o.Kind, o.Name, o.Namespace, w.namespace)
		}
		if o.App != w.group {
			t.Errorf("%s/%s deploy group = %q, want %q", o.Kind, o.Name, o.App, w.group)
		}
	}
	for k := range want {
		t.Errorf("%s missing from the applied stream", k)
	}

	// A named group is a --target, and the targeted deploy writes only the
	// cluster that group declares.
	for target, wantCluster := range map[string]string{"kata-prepull": "k3d-cp-daemon", "issuers": "k3d-cp"} {
		if err := validateTargetsAgainstRender(entities, []string{target}, stream); err != nil {
			t.Errorf("--target %s refused: %v", target, err)
			continue
		}
		selected := cluster.SelectManifestsByGroup(stream, []string{target})
		if strings.TrimSpace(selected) == "" {
			t.Errorf("--target %s selects nothing", target)
			continue
		}
		targeted, err := buildDeployGroups("dev", filterEntitiesByTarget(entities, []string{target}), "cp-dev")
		if err != nil {
			t.Fatalf("targeted groups: %v", err)
		}
		var got []string
		for _, g := range targetedK8sGroups(selected, groups, targeted, entities) {
			got = append(got, g.Cluster)
		}
		if strings.Join(got, ",") != wantCluster {
			t.Errorf("--target %s applies to %v, want [%s]", target, got, wantCluster)
		}
	}
}

// manifestsOnlyClusterMain puts a forge.Manifests group on a cluster that NO
// workload runs on (ctx-daemon), next to a primary-cluster group. ctx-hub
// carries both a workload and a group, so its group must be JOINED, not
// duplicated.
const manifestsOnlyClusterMain = `
import forge
import forge.workloads as fw

_hub = forge.ClusterTarget {cluster = "ctx-hub", namespace = "app"}

output = forge.render(forge.Bundle {
    project = "repro"
    cluster_target = _hub
    workloads = [fw.Workload {name = "api", image = "docker.io/library/busybox:1", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _hub}}]
    manifests = [
        forge.Manifests {
            name = "daemon-side"
            cluster = "ctx-daemon"
            objects = [{apiVersion = "scheduling.k8s.io/v1", kind = "PriorityClass", metadata.name = "x", value = 10}]
        }
        forge.Manifests {
            name = "hub-side"
            objects = [{apiVersion = "v1", kind = "ConfigMap", metadata.name = "hub-settings", data = {a = "b"}}]
        }
    ]
})
`

// A forge.Manifests group whose cluster no workload runs on is still a deploy
// destination: it creates its own k8s group (keyed by the kubectl context,
// namespace from the primary target), the render attributes its objects to
// that cluster, and `--target <group>` applies there. A cluster that already
// has a workload group is joined, never given a second group.
func TestBundleManifests_ManifestsOnlyClusterIsADeployGroup(t *testing.T) {
	out := renderKCLProject(t, writeKCLProject(t, manifestsOnlyClusterMain), "env=prod")
	entities, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	stream, err := cluster.ExtractManifests(out)
	if err != nil {
		t.Fatalf("ExtractManifests: %v", err)
	}
	groups, err := buildDeployGroups("prod", entities, "app")
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}

	var k8s []string
	for _, g := range groups {
		if g.ProviderID != "k8s-cluster" {
			continue
		}
		var names []string
		for _, s := range g.Services {
			names = append(names, s.Name)
		}
		k8s = append(k8s, g.Cluster+"/"+g.Namespace+"["+strings.Join(names, ",")+"]")
	}
	if got, want := strings.Join(k8s, " "), "ctx-daemon/app[] ctx-hub/app[api]"; got != want {
		t.Fatalf("k8s deploy groups = %q, want %q", got, want)
	}

	objects, clusters := attributeRenderedObjects(stream, groups, entities)
	if got := strings.Join(clusters, ","); got != "ctx-daemon,ctx-hub" {
		t.Fatalf("env clusters = %v, want [ctx-daemon ctx-hub]", clusters)
	}
	want := map[string]string{
		"PriorityClass/x":        "ctx-daemon",
		"ConfigMap/hub-settings": "ctx-hub",
		"Deployment/api":         "ctx-hub",
	}
	for _, o := range objects {
		w, ok := want[o.Kind+"/"+o.Name]
		if !ok {
			continue
		}
		delete(want, o.Kind+"/"+o.Name)
		if strings.Join(o.Clusters, ",") != w {
			t.Errorf("%s/%s lands on %v, want only [%s]", o.Kind, o.Name, o.Clusters, w)
		}
	}
	for k := range want {
		t.Errorf("%s missing from the applied stream", k)
	}

	// The named manifests-only group is its own --target, applied to its
	// declared cluster and nowhere else.
	if err := validateTargetsAgainstRender(entities, []string{"daemon-side"}, stream); err != nil {
		t.Fatalf("--target daemon-side refused: %v", err)
	}
	selected := cluster.SelectManifestsByGroup(stream, []string{"daemon-side"})
	targeted, err := buildDeployGroups("prod", filterEntitiesByTarget(entities, []string{"daemon-side"}), "app")
	if err != nil {
		t.Fatalf("targeted groups: %v", err)
	}
	var got []string
	for _, g := range targetedK8sGroups(selected, groups, targeted, entities) {
		got = append(got, g.Cluster)
	}
	if strings.Join(got, ",") != "ctx-daemon" {
		t.Errorf("--target daemon-side applies to %v, want [ctx-daemon]", got)
	}
	// And a workload target does not drag the manifests-only cluster along.
	targeted, err = buildDeployGroups("prod", filterEntitiesByTarget(entities, []string{"api"}), "app")
	if err != nil {
		t.Fatalf("targeted groups: %v", err)
	}
	got = nil
	for _, g := range targetedK8sGroups(cluster.SelectManifestsByGroup(stream, []string{"api"}), groups, targeted, entities) {
		got = append(got, g.Cluster)
	}
	if strings.Join(got, ",") != "ctx-hub" {
		t.Errorf("--target api applies to %v, want [ctx-hub]", got)
	}

	// The context guard sees the manifests-only cluster: a kubeconfig
	// without it is refused by name.
	if !kclEntitiesHaveK8sCluster(entities) {
		t.Errorf("kclEntitiesHaveK8sCluster = false for an env that applies to two clusters")
	}
	if got := strings.Join(declaredClusterContexts(entities, "ctx-hub", groups), ","); got != "ctx-hub,ctx-daemon" {
		t.Errorf("declaredClusterContexts = %q, want ctx-hub,ctx-daemon", got)
	}
}
