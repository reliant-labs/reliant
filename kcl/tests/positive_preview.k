"""
Positive test — a preview instance records what it copies and when it
expires, and labels its namespaces so `forge env gc` finds it on the cluster.
An env that declares no preview carries none of it.
"""

# kcl-args: env=preview-pr-12

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-preview-pr-12"}

output = forge.render(forge.Bundle {
    project = "demo"
    preview = forge.Preview {of = "preview", expires_at = "2026-11-15T12:00:00Z", source = "pr-12"}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_plain = forge.render(forge.Bundle {
    project = "demo"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_ns = [m for m in output.manifests if m.kind == "Namespace"]
_plain_ns = [m for m in _plain.manifests if m.kind == "Namespace"]
assert_preview_recorded = output.preview.of == "preview" and output.preview.expires_at == "2026-11-15T12:00:00Z" and output.preview.source == "pr-12"
assert_absent_when_undeclared = "preview" not in _plain or not _plain.preview
assert_namespace_labelled = len(_ns) == 1 and _ns[0].metadata.labels["forge.dev/preview-of"] == "preview" and _ns[0].metadata.annotations["forge.dev/expires-at"] == "2026-11-15T12:00:00Z"
assert_plain_namespace_unlabelled = len(_plain_ns) == 1 and "forge.dev/preview-of" not in _plain_ns[0].metadata.labels
