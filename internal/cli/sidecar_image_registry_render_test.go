package cli

import (
	"testing"
)

// sidecarContainers renders the one-workload project with sidecars through
// the real seam (kclrender.Run -> output.manifests -> ExtractManifests /
// RenderWorkloads) and returns the Deployment's containers by name.
func sidecarContainers(t *testing.T) map[string]map[string]any {
	t.Helper()
	out := renderKCLProject(t, writeKCLProject(t, `import forge
import forge.workloads as fw

_t = forge.ClusterTarget {
    cluster = "test-cluster"
    namespace = "testns"
}

output = forge.render(forge.Bundle {
    project = "proj"
    workloads = [fw.Workload {
        name = "api"
        image = "reg.example.com/proj/api"
        ports = [fw.Port {name = "http", port = 8080}]
        sidecars = [
            fw.Container {
                name = "cloud-sql-proxy"
                image = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1"
                args = ["--address=127.0.0.1", "--port=5433"]
                resources = fw.Resources {cpuRequestMillicores = 200, cpuLimitMillicores = 200, memoryRequestBytes = 268435456, memoryLimitBytes = 268435456}
            }
            fw.Container {name = "port-registry", image = "localhost:5050/vendor/thing:1.0"}
        ]
        runtime = forge.OnCluster {target = _t}
    }]
})
`), `image_tag="v9"`)
	deps := appliedObjects(t, out)["Deployment"]
	if len(deps) != 1 {
		t.Fatalf("Deployments = %d, want 1", len(deps))
	}
	spec, _ := deps[0]["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	podSpec, _ := tmpl["spec"].(map[string]any)
	containers := map[string]map[string]any{}
	list, _ := podSpec["containers"].([]any)
	for _, c := range list {
		cm, _ := c.(map[string]any)
		name, _ := cm["name"].(string)
		containers[name] = cm
	}
	return containers
}

// TestSidecarImageKeepsItsOwnRegistry pins the rule that every container's
// image reference reaches the render VERBATIM: forge composes a tag onto it and
// nothing else, so a sidecar's registry host is whatever its own reference says.
//
// THE BUG THIS CATCHES: forge once prefixed a project-wide registry onto any
// image that carried a tag, without first asking whether the image already
// named a registry. A third-party sidecar — cloud-sql-proxy, an OTel collector
// — is pulled from its vendor's registry, and the prefixing produced:
//
//	us-central1-docker.pkg.dev/proj/repo/gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1
//
// That reference cannot be pulled. It renders and deploys cleanly, then fails
// at runtime as ImagePullBackOff — the expensive place to find out.
//
// The primary container is asserted alongside the sidecars: it carries no tag
// of its own, so it takes the env's resolved TAG while keeping its host — the
// one thing forge does contribute, and the thing a naive fix would break.
func TestSidecarImageKeepsItsOwnRegistry(t *testing.T) {
	containers := sidecarContainers(t)
	for _, tc := range []struct{ container, want, why string }{
		{"api", "reg.example.com/proj/api:v9", "an untagged primary image keeps its host and takes the env's resolved tag"},
		{"cloud-sql-proxy", "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1", "a dotted registry host means the image is third-party"},
		{"port-registry", "localhost:5050/vendor/thing:1.0", "a host:port registry is a registry host too"},
	} {
		c, ok := containers[tc.container]
		if !ok {
			t.Errorf("container %q missing from render (got %v)", tc.container, containers)
			continue
		}
		if got, _ := c["image"].(string); got != tc.want {
			t.Errorf("container %q image =\n  %s\nwant\n  %s\n(%s)", tc.container, got, tc.want, tc.why)
		}
	}

	// A sidecar's args must render as k8s `args`, NEVER folded into `command`.
	// Folding overrides the image's entrypoint, so the flags themselves become
	// the argv and the container dies at startup with
	//   exec: "--address=127.0.0.1": executable file not found in $PATH
	proxy := containers["cloud-sql-proxy"]
	if _, hasCommand := proxy["command"]; hasCommand {
		t.Errorf("sidecar rendered a `command` (%v) — that REPLACES the image entrypoint; args belong in `args`", proxy["command"])
	}
	gotArgs, _ := proxy["args"].([]any)
	if len(gotArgs) != 2 || gotArgs[0] != "--address=127.0.0.1" || gotArgs[1] != "--port=5433" {
		t.Errorf("sidecar args = %v, want [--address=127.0.0.1 --port=5433]", proxy["args"])
	}
}

// TestSidecarRendersResources pins the rule that EVERY container forge renders
// carries cpu/memory requests and limits — sidecars included, whether or not
// the author declared any.
//
// THE BUG THIS CATCHES: a sidecar with no resources block is BestEffort: the
// first thing the kubelet evicts, with no CPU guarantee — and k8s computes
// pod QoS across ALL containers, so ONE request-less sidecar demoted the whole
// pod even though the primary was sized. When the sidecar IS the database
// path (cloud-sql-proxy) the symptom is connection errors inside the app.
//
// Both halves are asserted because they fail independently: a declared
// Resources round-trips to the rendered quantities, and an UNDECLARED one
// falls back to forge's defaults (v1alpha1.Resources.WithDefaults: 250m /
// 1Gi request, limit = request) rather than rendering nothing.
func TestSidecarRendersResources(t *testing.T) {
	containers := sidecarContainers(t)
	for _, tc := range []struct {
		container                      string
		cpuReq, cpuLim, memReq, memLim string
		why                            string
	}{
		{"cloud-sql-proxy", "200m", "200m", "256Mi", "256Mi", "a declared Resources round-trips"},
		{"port-registry", "250m", "250m", "1Gi", "1Gi", "an UNDECLARED sidecar falls back to defaults, never to no block"},
	} {
		ctr := containers[tc.container]
		if ctr == nil {
			t.Errorf("container %q missing from render", tc.container)
			continue
		}
		res, ok := ctr["resources"].(map[string]any)
		if !ok {
			t.Errorf("container %q rendered NO resources block — BestEffort, and it drags the whole pod's QoS down (%s)", tc.container, tc.why)
			continue
		}
		requests, _ := res["requests"].(map[string]any)
		limits, _ := res["limits"].(map[string]any)
		for _, got := range []struct {
			field string
			have  any
			want  string
		}{
			{"requests.cpu", requests["cpu"], tc.cpuReq},
			{"requests.memory", requests["memory"], tc.memReq},
			{"limits.cpu", limits["cpu"], tc.cpuLim},
			{"limits.memory", limits["memory"], tc.memLim},
		} {
			if s, _ := got.have.(string); s != got.want {
				t.Errorf("container %q %s = %v, want %q (%s)", tc.container, got.field, got.have, got.want, tc.why)
			}
		}
	}
}
