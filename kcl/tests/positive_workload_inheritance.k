"""
Positive test — DECLARE COMPLETE, REFINE PER ENV.

A fw.Workload literal in deploy/kcl/workloads.k is a COMPLETE workload.
Each env binds and refines it with `|`, and — because `|` preserves the
schema type — the Workload checks re-run on the merged value (the
companion negative is negative_workload_bad_refinement.k).

dev binds the base unchanged to a local cluster; prod refines replicas and
the port. The base is untouched, and every refinement reaches its own env's
record.

The IMAGE is declared once on the base and is the same in both envs, because
the registry is part of the image and neither env has one. What differs per env
is the tag forge resolves, not where the image lives.
"""

import forge
import forge.workloads as fw

api = fw.Workload {
    name = "api"
    kind = "service"
    image = "ghcr.io/acme/demo"
    args = ["api"]
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
}

_dev_t = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_prod_t = forge.ClusterTarget {cluster = "gke-demo", namespace = "demo-prod", platform = "amd64"}

_dev = forge.render(forge.Bundle {project = "demo", workloads = [api | {runtime = forge.OnCluster {target = _dev_t}}]})
_prod = forge.render(forge.Bundle {project = "demo", workloads = [api | {
    replicas = 3
    ports = [fw.Port {name = "http", port = 9090, expose = True}]
    runtime = forge.OnCluster {target = _prod_t}
}]})

_rec = lambda out: any -> any { [m for m in out.manifests if m.kind == "Workload"][0] }

assert_dev_port_inherited = _rec(_dev).spec.ports[0].port == 8080
assert_dev_replicas_untouched = _rec(_dev).spec.replicas == 1
assert_prod_port_overridden = _rec(_prod).spec.ports[0].port == 9090
assert_prod_replicas_overridden = _rec(_prod).spec.replicas == 3
assert_prod_namespace = _rec(_prod).metadata.namespace == "demo-prod"
assert_base_unchanged = api.ports[0].port == 8080 and api.replicas == 1
assert_name_inherited = _rec(_prod).metadata.name == "api"
assert_kind_inherited = _rec(_prod).spec.kind == "service"
assert_build_inherited = _prod.workloads[0].build.cmd == "./cmd/demo"
# Both envs pull the same declared reference; only the tag differs.
assert_dev_image_host = _rec(_dev).spec.image.startswith("ghcr.io/acme/demo:")
assert_prod_image_host = _rec(_prod).spec.image.startswith("ghcr.io/acme/demo:")
assert_refinement_preserves_schema = typeof(api | {replicas = 2}) == "Workload"
