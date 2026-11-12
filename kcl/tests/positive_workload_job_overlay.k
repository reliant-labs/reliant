"""
Positive test — `before` ordering survives a per-env OVERLAY applied to
workloads loaded as plain data.

A project can load its workload list from generated data (the codegen
stanza, a JSON document) and overlay per-env ordering with `|`. The overlay
is what declares that `provision-idp` gates `api`; this pins that the merged
declaration — not the base — is what reaches the record Go expands, so the
gate (an initContainer on `api`, pkg/deploy TestBefore) is actually rendered.
"""

import forge
import forge.workloads as fw

_loaded = [
    {name = "api", kind = "service", image = "localhost:5050/demo"}
    {name = "provision-idp", kind = "job", image = "localhost:5050/demo", args = ["provision-idp"]}
]

_workloads = [
    fw.Workload {**c} | ({before = ["api"]} if c.name == "provision-idp" else {})
    for c in _loaded
]

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
output = forge.render(forge.Bundle {project = "demo", workloads = [w | {runtime = forge.OnCluster {target = _k3d}} for w in _workloads]})
_rec = {m.metadata.name: m.spec for m in output.manifests if m.kind == "Workload"}

assert_overlay_ordering_applied = _rec["provision-idp"].before == ["api"]
assert_base_has_no_ordering = "before" not in _rec["api"]
assert_job_argv_kept = _rec["provision-idp"].args == ["provision-idp"]
# Both are records: the gate is expanded in Go from the WHOLE set.
assert_both_records = sorted([k for k in _rec]) == ["api", "provision-idp"]
