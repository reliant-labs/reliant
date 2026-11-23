"""
Positive test — the runtime is chosen PER WORKLOAD, never per env (ADR 0002
§2, the hounders example):

    bundle = forge.Bundle {
        project = "hounders"
        workloads = [
            wl.membership | {runtime = forge.OnHost {runner = "air"}}
            wl.migrate    | {runtime = forge.OnHost {}}
            wl.search     | {runtime = forge.OnCluster {target = _k3d}}
        ]
    }

Pins that each binding is exactly what output.workloads[].runtime carries,
that the base declaration is untouched by it, and that with no
`cluster_target` the env's support objects land on the first
cluster-bound workload's target.

A Bundle has no `runtime` field to set: closedschema_bundle_runtime.k.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-hounders", namespace = "hounders-dev"}

_build = forge.GoBuild {cmd = "./cmd/hounders", output_name = "hounders"}
_membership = fw.Workload {name = "membership", build = _build, args = ["membership"]}
_migrate = fw.Workload {name = "migrate", kind = "job", build = _build, args = ["db", "migrate", "up"], before = [fw.BEFORE_ALL]}
_search = fw.Workload {name = "search", build = _build, image = "localhost:5050/hounders", args = ["search"]}

output = forge.render(forge.Bundle {
    project = "hounders"
    workloads = [
        _membership | {runtime = forge.OnHost {runner = "air", air_config = ".air.toml"}}
        _migrate | {runtime = forge.OnHost {}}
        _search | {runtime = forge.OnCluster {target = _k3d}}
    ]
})
_rt = {w.name: w.runtime for w in output.workloads}

assert_binding_is_carried = _rt["membership"].type == "host" and _rt["membership"].runner == "air"
assert_binding_fields_whole = _rt["membership"].air_config == ".air.toml"
assert_job_binds_its_own = _rt["migrate"].type == "host" and _rt["migrate"].runner == "go-run"
assert_cluster_binding = _rt["search"].type == "cluster" and _rt["search"].namespace == "hounders-dev"
# The base declaration is untouched by the binding.
assert_base_unbound = _search.runtime == Undefined or _search.runtime == None

_records = [m.metadata.name for m in output.manifests if m.kind == "Workload"]
assert_only_cluster_has_record = _records == ["search"]
# No cluster_target declared: the primary target is the cluster-bound
# workload's, so the Namespace lands there.
assert_primary_from_binding = output.cluster_target.namespace == "hounders-dev"
