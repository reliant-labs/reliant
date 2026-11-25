"""
Positive test — the `forge.dev/env` ownership stamp on everything
forge.render emits into output.manifests.

Two envs can resolve to the SAME namespace; a deploy of one then silently
replaces the other's objects. With the stamp that is one selector
(`kubectl get all -n <ns> -l 'forge.dev/env!=dev'`).

Pinned here (the KCL half):

  1. The DRILL-IN: `Bundle.env` states the tag; every Workload record, the
     Namespace, ConfigMaps and raw pass-through manifests carry it. The
     Kubernetes objects the records expand into are stamped by Go from
     Context.Env, on objects and pod templates and NEVER on a selector
     (pkg/deploy TestEnvStamp).
  2. A hand-written label on a raw manifest WINS: the stamp merges UNDER
     existing labels.
  3. No binding, no drill-in ⇒ no env label at all: a raw manifest with no
     labels gains only its group's cluster routing label (forge.dev/cluster,
     positive_service_owned_manifests.k), nothing env-related.

The `-D env=` DEFAULT path is positive_env_option.k.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "k3d-myapp", namespace = "myapp-shared"}

_raw = [
    {apiVersion = "cert-manager.io/v1", kind = "ClusterIssuer", metadata = {name = "letsencrypt"}, spec = {acme = {server = "https://example.invalid"}}}
    {apiVersion = "v1", kind = "ConfigMap", metadata = {name = "pinned", labels = {"forge.dev/env" = "hand-written"}}, data = {a = "b"}}
]

_workloads = [
    fw.Workload {name = "api", image = "localhost:5050/myapp", ports = [fw.Port {name = "http", port = 8080}]}
    fw.Workload {name = "nightly", kind = "cron", image = "localhost:5050/myapp", args = ["nightly"], schedule = "0 3 * * *"}
]

_KEY = "forge.dev/env"
_labels_of = lambda m: any -> {str:str} {
    (m.metadata.labels if m.metadata.labels else {}) if m.metadata else {}
}

manifests = forge.render(forge.Bundle {
    project = "myapp"
    env = "dev-k8s"
    cluster_target = _target
    workloads = [_w | {runtime = forge.OnCluster {target = _target}} if not _w.runtime else _w for _w in _workloads]
    manifests = [forge.Manifests {objects = _raw}]
}).manifests

plain_manifests = forge.render(forge.Bundle {
    project = "myapp"
    cluster_target = _target
    workloads = [_w | {runtime = forge.OnCluster {target = _target}} if not _w.runtime else _w for _w in _workloads]
    manifests = [forge.Manifests {objects = _raw}]
}).manifests

assert_every_object_stamped = len(manifests) > 3 and all m in manifests {
    _labels_of(m)[_KEY] == "dev-k8s" or m.metadata.name == "pinned"
}
assert_records_stamped = all m in [x for x in manifests if x.kind == "Workload"] {
    _labels_of(m)[_KEY] == "dev-k8s"
}
assert_raw_manifest_stamped = _labels_of([m for m in manifests if m.kind == "ClusterIssuer"][0])[_KEY] == "dev-k8s"
assert_namespace_stamped = _labels_of([m for m in manifests if m.kind == "Namespace"][0])[_KEY] == "dev-k8s"
assert_hand_written_label_wins = _labels_of([m for m in manifests if m.metadata.name == "pinned"][0])[_KEY] == "hand-written"

assert_no_env_no_label = all m in plain_manifests {
    _KEY not in _labels_of(m) or m.metadata.name == "pinned"
}
assert_no_env_only_cluster_label = _labels_of([m for m in plain_manifests if m.kind == "ClusterIssuer"][0]) == {"forge.dev/cluster" = "k3d-myapp"}
