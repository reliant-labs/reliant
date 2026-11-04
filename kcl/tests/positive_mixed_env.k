"""
Positive test — a MIXED env: host, compose, cluster and hosted workloads in
ONE Bundle (ADR 0002 §2).

Hosting used to be env-wide (`control_plane` set ⇒ render_manifests
returned [] and every tier went to the platform). Real envs mix runtimes per
workload — control-plane's dev runs air processes, compose infra, k3d pods
and build-only images side by side — so each workload's own runtime decides:

  * host / compose / build-only → output.workloads only; no record;
  * cluster → output.workloads AND a Workload record in output.manifests;
  * hosted  → output.workloads only (published as a CR by the hosted
    provider); still no record, even though the SAME env applies cluster
    records.

`control_plane` and `cluster_target` coexist (the old
"not control_plane or not cluster_target" rule is gone).
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-hounders", namespace = "hounders-dev"}
_pinned = "ghcr.io/acme/billing@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

output = forge.render(forge.Bundle {
    project = "hounders"
    env = "dev"
    control_plane = forge.ControlPlane {endpoint = "http://127.0.0.1:8090"}
    cluster_target = _k3d
    workloads = [
        fw.Workload {
            name = "membership"
            build = forge.GoBuild {cmd = "./cmd/hounders", output_name = "hounders"}
            args = ["membership"]
            runtime = forge.OnHost {runner = "air", air_config = ".air.toml", listen_ports = [8085]}
        }
        fw.Workload {name = "postgres", runtime = forge.OnCompose {service = "postgres"}}
        fw.Workload {
            name = "search"
            build = forge.GoBuild {cmd = "./cmd/hounders", output_name = "hounders"}
            image = "localhost:5050/hounders"
            args = ["search"]
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _k3d}
        }
        fw.Workload {
            name = "billing"
            image = _pinned
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            runtime = forge.OnHosted {}
        }
        fw.Workload {
            name = "cli"
            kind = "tool"
            build = forge.GoBuild {cmd = "./cmd/hounders-cli"}
            runtime = forge.BuildOnly {}
        }
    ]
})

_w = {w.name: w for w in output.workloads}
assert_every_runtime_present = sorted([w.runtime.type for w in output.workloads]) == \
    ["build-only", "cluster", "compose", "host", "hosted"]
assert_all_in_workloads = len(output.workloads) == 5

# Only the cluster workload has a record.
_records = [m for m in output.manifests if m.kind == "Workload"]
assert_one_record = [m.metadata.name for m in _records] == ["search"]
assert_record_on_target = _records[0].metadata.namespace == "hounders-dev" and \
    _records[0].metadata.labels["forge.dev/cluster"] == "k3d-hounders"

# The hosted workload is not a record, yet the env's manifests are NOT
# blanked by the presence of control_plane.
assert_hosted_not_a_record = "billing" not in [m.metadata.name for m in _records]
assert_hosted_spec_kept = _w["billing"].spec.image == _pinned
assert_manifests_not_blanked = len(output.manifests) > 0

# Both env-level declarations survive side by side.
assert_control_plane = output.control_plane.endpoint == "http://127.0.0.1:8090"
assert_cluster_target = output.cluster_target.namespace == "hounders-dev"

# The WorkloadURL / before namespace: names are unique across the env.
assert_names_unique = len({w.name: 0 for w in output.workloads}) == len(output.workloads)
