"""
Positive test — the release being deployed rides on every cluster Workload
record as `app.kubernetes.io/version`; pkg/deploy stamps it on the pod
template (never a selector), so a node agent that file-scrapes logs can report
`service.version` for pods whose logs carry no SDK resource.

The label value is Bundle.release, which defaults to `-D release_version=`.
Unbound, no label is emitted and the render is unchanged.
"""

# kcl-args: release_version="20261025"

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "gke-prod", namespace = "shop-prod"}
_wl = fw.Workload {
    name = "api"
    image = "registry.example.com/api"
    ports = [fw.Port {name = "http", port = 8080}]
    runtime = forge.OnCluster {target = _target}
}

_record = lambda b: forge.Bundle -> any {
    [m for m in forge.render(b).manifests if m.kind == "Workload" and m.metadata.name == "api"][0]
}

_bound = _record(forge.Bundle {project = "shop", cluster_target = _target, workloads = [_wl]})
_pinned = _record(forge.Bundle {project = "shop", cluster_target = _target, release = "v-pinned", workloads = [_wl]})
_unset = _record(forge.Bundle {project = "shop", cluster_target = _target, release = "", workloads = [_wl]})

assert_option_is_version_label = _bound.metadata.labels["app.kubernetes.io/version"] == "20261025"
assert_explicit_release_wins = _pinned.metadata.labels["app.kubernetes.io/version"] == "v-pinned"
assert_empty_release_has_no_label = "app.kubernetes.io/version" not in _unset.metadata.labels
