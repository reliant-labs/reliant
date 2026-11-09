"""
Positive test — RuntimeClass declarations render through both the
JSON contract (top-level `runtime_classes` bucket) and the K8s
manifest output (`node.k8s.io/v1 RuntimeClass` objects).

Use case (the bug-11 motivator): cp-forge's WorkspaceConfig defaults
to `runtimeClassName = "gvisor"` because prod GKE has gvisor
installed. The dev k3d cluster doesn't, so workspace Pods stay
Pending until someone manually `kubectl apply`s a stub RuntimeClass
that aliases `gvisor` → the vanilla `runc` handler. Declaring the
alias here lets `forge env deploy dev` own it via the standard
render-and-apply pipeline.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-runtime"
    namespace = "runtime-dev"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/api"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
        }
    ]
    runtime_classes = [
        # Dev alias: pods that select "gvisor" by name get vanilla runc.
        # Same shape as the brief calls out for cp-forge dev-host parity.
        forge.RuntimeClass {
            name = "gvisor"
            handler = "runc"
        }
        # Real handler with overhead + scheduling: pins the optional
        # raw-dict pass-through path (so future K8s API additions
        # don't need a schema bump to flow through).
        forge.RuntimeClass {
            name = "kata-qemu"
            handler = "kata-qemu"
            overhead = {
                podFixed = {
                    memory = "120Mi"
                    cpu = "250m"
                }
            }
            scheduling = {
                nodeSelector = {
                    "node.kubernetes.io/runtime" = "kata"
                }
            }
        }
    ]
}

# ── JSON contract (top-level `output.runtime_classes` bucket) ──────

output = forge.render(_bundle)
assert_rc_bucket_present = "runtime_classes" in output
assert_rc_count = len(output.runtime_classes) == 2
assert_rc_alias_name = output.runtime_classes[0].name == "gvisor"
assert_rc_alias_handler = output.runtime_classes[0].handler == "runc"
assert_rc_alias_no_overhead = output.runtime_classes[0].overhead == None
assert_rc_alias_no_scheduling = output.runtime_classes[0].scheduling == None
assert_rc_kata_overhead_set = output.runtime_classes[1].overhead.podFixed.memory == "120Mi"
assert_rc_kata_scheduling_set = output.runtime_classes[1].scheduling.nodeSelector["node.kubernetes.io/runtime"] == "kata"

# ── K8s manifests (node.k8s.io/v1 RuntimeClass objects) ────────────

manifests = forge.render(_bundle).manifests
_rcs = [m for m in manifests if m.kind == "RuntimeClass"]
assert_rc_manifest_count = len(_rcs) == 2

# Cluster-scoped — no namespace on metadata.
assert_rc_manifest_api = _rcs[0].apiVersion == "node.k8s.io/v1"
assert_rc_manifest_kind = _rcs[0].kind == "RuntimeClass"
assert_rc_manifest_name = _rcs[0].metadata.name == "gvisor"
assert_rc_manifest_handler = _rcs[0].handler == "runc"
assert_rc_manifest_no_namespace = "namespace" not in _rcs[0].metadata

# managed-by label so future cleanup / kubectl filtering can find them.
assert_rc_manifest_managed_label = _rcs[0].metadata.labels["app.kubernetes.io/managed-by"] == "forge"

# Optional fields pass through to the manifest verbatim.
assert_rc_kata_manifest_overhead = _rcs[1].overhead.podFixed.cpu == "250m"
assert_rc_kata_manifest_scheduling = _rcs[1].scheduling.nodeSelector["node.kubernetes.io/runtime"] == "kata"

# Ordering invariant: RuntimeClass manifests come BEFORE the
# Namespace + workloads so kube-apiserver sees them during apply
# before any Pod selects them via runtimeClassName.
_first_rc_idx = [i for i in range(len(manifests)) if manifests[i].kind == "RuntimeClass"][0]
_namespace_idx = [i for i in range(len(manifests)) if manifests[i].kind == "Namespace"][0]
assert_rc_before_namespace = _first_rc_idx < _namespace_idx
