"""
Positive test — `app.kubernetes.io/part-of` names the PROJECT on every
object forge.render emits into output.manifests.

A CD pipeline's rollout gate selects one release's objects with
`-l app.kubernetes.io/part-of=<project>`. When objects labelled themselves,
control-plane's prod render matched 5 of 13 Deployments and the rollout wait
passed vacuously on the rest. The Kubernetes objects are Go's now
(RenderWorkloads stamps them from Context.PartOf — pkg/deploy TestEnvStamp /
goldens), so the KCL half of the property is: every object forge.render
emits — Workload records, raw Bundle.manifests groups, ConfigMaps,
RuntimeClasses — names the project, and `app.kubernetes.io/name` stays the
WORKLOAD (the --target group).
"""

import forge
import forge.workloads as fw

_project = "demo-project"
_target = forge.ClusterTarget {cluster = "k3d-example", namespace = "example-prod"}

_bundle = forge.Bundle {
    project = _project
    cluster_target = _target
    config_maps = [forge.ConfigMap {name = "app-config", data = {"KEY" = "value"}}]
    runtime_classes = [forge.RuntimeClass {name = "gvisor", handler = "runc"}]
    # A named raw-manifest group (its own --target), stamped on the way out.
    manifests = [forge.Manifests {
        name = "infra"
        objects = [{apiVersion = "v1", kind = "Secret", metadata = {name = "bootstrap", namespace = "example-prod"}}]
    }]
    workloads = [_w | {runtime = forge.OnCluster {target = _target}} if not _w.runtime else _w for _w in [
        fw.Workload {name = "api", image = "ghcr.io/reliant/example/api-img", ports = [fw.Port {name = "http", port = 8080}]}
        fw.Workload {name = "controller", kind = "operator", image = "ghcr.io/reliant/example/controller-img", crds = ["Widget"]}
        fw.Workload {name = "cleanup", kind = "cron", image = "ghcr.io/reliant/example/cleanup-img", args = ["cleanup"], schedule = "0 * * * *"}
        fw.Workload {name = "migrate", kind = "job", image = "ghcr.io/reliant/example/migrate-img", args = ["migrate"]}
    ]]
}

_manifests = forge.render(_bundle).manifests

_records = [m for m in _manifests if m.kind == "Workload"]
# Guard against a vacuous pass over an empty set.
assert_records_exist = len(_records) == 4
assert_every_record_part_of = all m in _records {
    m.metadata.labels["app.kubernetes.io/part-of"] == _project
}
# `name` stays the workload: part-of and name must not collapse.
assert_record_name_is_workload = all m in _records {
    m.metadata.labels["app.kubernetes.io/name"] == m.metadata.name
}

_labelled = [m for m in _manifests if m.metadata.labels]
assert_no_object_labels_itself = len(_labelled) >= 7 and all m in _labelled {
    "app.kubernetes.io/part-of" not in m.metadata.labels or m.metadata.labels["app.kubernetes.io/part-of"] == _project
}

_bootstrap = [m for m in _manifests if m.kind == "Secret" and m.metadata.name == "bootstrap"][0]
assert_owned_manifest_part_of = _bootstrap.metadata.labels["app.kubernetes.io/part-of"] == _project
assert_config_map_part_of = [m for m in _manifests if m.kind == "ConfigMap"][0].metadata.labels["app.kubernetes.io/part-of"] == _project
assert_runtime_class_part_of = [m for m in _manifests if m.kind == "RuntimeClass"][0].metadata.labels["app.kubernetes.io/part-of"] == _project
