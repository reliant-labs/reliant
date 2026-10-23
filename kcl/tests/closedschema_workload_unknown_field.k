"""
Closed-schema test — fw.Workload refuses a field it does not declare.

fw.Workload is generated from pkg/deploy/v1alpha1.WorkloadSpec (plus the
authoring fields name / build / runtime / manifests). The spec is an
ALLOWLIST by construction (ADR 0002 §3): a Kubernetes PodSpec field forge has
not modelled is unreachable, so a hosted user cannot author it on the hosted
runtime and a cluster author cannot smuggle it past the Go renderer.

`hostNetwork` is the canonical case — a real PodSpec field a reader might
reasonably try, and one the hosted runtime must never honour. Refused at
compile time by KCL, naming the member and the schema.

Replaces closedschema_simple_backend_unknown_field.k (SimpleBackend is gone).
"""

# expect: hostNetwork
# expect: Workload

import forge.workloads as fw

_w = fw.Workload {
    name = "api"
    hostNetwork = True
}
