"""
Positive test — `serviceAccountAnnotations` on fw.Workload: validated at
authoring time, carried into the spec, for every RBAC tier.

GKE Workload Identity reads `iam.gke.io/gcp-service-account` off the KSA the
pod runs as, so the annotation goes on the SA forge generates, keeping its
name (control-plane's leader-election / PDP RBAC bind the workload-named
SAs). The Go renderer puts it on the SA and nowhere else (pkg/deploy
TestIdentity); this pins the authoring side: the key grammar, and that the
spec carries the map verbatim for the bare, namespaced and cluster tiers.

The hosted runtime refuses the field (negative_runtime_mask_hosted_sa_annotations.k).
"""

import forge
import forge.workloads as fw
import forge.lib.annotations as annotations_lib

_WI = "iam.gke.io/gcp-service-account"
_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_w | {runtime = forge.OnCluster {target = _k3d}} if not _w.runtime else _w for _w in [
        fw.Workload {name = "api", image = "localhost:5050/demo", serviceAccountAnnotations = {"${_WI}" = "api@p.iam.gserviceaccount.com"}}
        fw.Workload {
            name = "reaper"
            kind = "worker"
            image = "localhost:5050/demo"
            namespacedRBAC = [{apiGroups = ["reliant.dev"], resources = ["workspaces"], verbs = ["list"]}]
            serviceAccountAnnotations = {"${_WI}" = "reaper@p.iam.gserviceaccount.com"}
        }
        fw.Workload {
            name = "ctl"
            kind = "operator"
            image = "localhost:5050/demo"
            crds = ["Workspace"]
            serviceAccountAnnotations = {"${_WI}" = "ctl@p.iam.gserviceaccount.com", "eks.amazonaws.com/role-arn" = "arn:aws:iam::1:role/raw"}
        }
        fw.Workload {name = "bare", image = "localhost:5050/demo"}
    ]]
})
_spec = {m.metadata.name: m.spec for m in output.manifests if m.kind == "Workload"}

assert_bare_tier = _spec["api"].serviceAccountAnnotations == {"${_WI}" = "api@p.iam.gserviceaccount.com"}
assert_namespaced_tier = _spec["reaper"].serviceAccountAnnotations == {"${_WI}" = "reaper@p.iam.gserviceaccount.com"}
assert_cluster_tier = _spec["ctl"].serviceAccountAnnotations["eks.amazonaws.com/role-arn"] == "arn:aws:iam::1:role/raw"
assert_control_absent = "serviceAccountAnnotations" not in _spec["bare"]

# ── key validation accepts the real-world shapes ──────────────────────
assert_valid_gke = annotations_lib.valid_key("iam.gke.io/gcp-service-account")
assert_valid_eks = annotations_lib.valid_key("eks.amazonaws.com/role-arn")
assert_valid_bare = annotations_lib.valid_key("a")
assert_valid_mixed = annotations_lib.valid_key("Some_Key.v1-x")
assert_invalid_two_slashes = not annotations_lib.valid_key("a/b/c")
assert_invalid_empty = not annotations_lib.valid_key("")
assert_invalid_trailing_dash = not annotations_lib.valid_key("name-")
assert_invalid_upper_prefix = not annotations_lib.valid_key("IAM.gke.io/x")
assert_invalid_empty_prefix = not annotations_lib.valid_key("/x")
assert_invalid_space = not annotations_lib.valid_key("has space")
assert_invalid_long_name = not annotations_lib.valid_key("x" * 64)
assert_valid_63_name = annotations_lib.valid_key("x" * 63)
