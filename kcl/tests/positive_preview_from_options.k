"""
Positive test — the preview marker forge binds on an instance's render
(`-D forge_preview_*`) becomes Bundle.preview, so the instance's main.k is a
verbatim copy of its template env's.
"""

# kcl-args: env=preview-pr-7 forge_preview_of=preview forge_preview_expires_at="2026-11-15T12:00:00Z" forge_preview_source=pr-7

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-" + option("env")}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_ns = [m for m in output.manifests if m.kind == "Namespace"]
assert_preview_bound = output.preview.of == "preview" and output.preview.expires_at == "2026-11-15T12:00:00Z" and output.preview.source == "pr-7"
assert_one_namespace = len(_ns) == 1
assert_namespace_per_preview = _ns[0].metadata.name == "demo-preview-pr-7"
assert_namespace_labelled = _ns[0].metadata.labels["forge.dev/preview"] == "preview-pr-7"
