"""
Positive — Bundle.overrides: the typed override surface, carried out of KCL
for the Go half to apply.

What this pins is the ERGONOMICS and the plumbing, not the merge: the patch
is applied in Go, after forge expands its Workload records into Deployments
(which is why `Deployment/api` is a legal key here even though this render
contains no Deployment).

Three key shapes and the two patch spellings:

  * `Kind/name` — the common case, in a single-namespace env.
  * `Kind/namespace/name` — qualified, when one kind+name is rendered twice.
  * `...@cluster` — qualified by cluster, for a multi-cluster env.
  * `{spec.replicas = 10}` — KCL's nested selector: a deep field in one line.
  * a map literal where a segment is not an identifier (every label key is).
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-cp"
    namespace = "acme-dev"
}

output = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnCluster {target = _target}
    }]
    overrides = {
        # One deep field, one line.
        "Deployment/api" = {spec.replicas = 10}
        # A label key is not an identifier, so the map literal stops at the
        # level that holds it. This is the exact override cp uses to relax
        # forge's `restricted` Namespace to `baseline`.
        "Namespace/acme-dev" = {metadata.labels = {
            "pod-security.kubernetes.io/enforce" = "baseline"
        }}
        # Container resources, keyed by container NAME: the strategic merge
        # patch lands on that container instead of replacing the list.
        "Deployment/acme-dev/api" = {spec.template.spec.containers = [{
            name = "api"
            resources.limits.cpu = "500m"
        }]}
        # Qualified by cluster, for an env that renders to several.
        "Service/api@k3d-cp" = {spec.sessionAffinity = "ClientIP"}
    }
})

# Carried on the output document, verbatim, for internal/cluster to apply
# against the EXPANDED stream.
assert_carried = len(output.overrides) == 4
assert_nested_selector = output.overrides["Deployment/api"] == {"spec": {"replicas": 10}}
assert_label_patch = output.overrides["Namespace/acme-dev"] == {
    "metadata": {"labels": {"pod-security.kubernetes.io/enforce": "baseline"}}
}
assert_container_patch = output.overrides["Deployment/acme-dev/api"] == {
    "spec": {"template": {"spec": {"containers": [
        {"name": "api", "resources": {"limits": {"cpu": "500m"}}}
    ]}}}
}
assert_cluster_qualified = output.overrides["Service/api@k3d-cp"] == {
    "spec": {"sessionAffinity": "ClientIP"}
}

# An env that declares none renders an empty map, not a missing key: the Go
# consumer sees a stable shape.
_plain = forge.render(forge.Bundle {project = "fixture"})
assert_default_empty = _plain.overrides == {}
