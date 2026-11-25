"""
Negative test — Workload.namespacedRBAC on a GATING job (one with `before`)
is refused.

A gating job renders no Job and no identity: it is an initContainer inside
each dependent's pod, so it runs under the DEPENDENT's ServiceAccount. A Role
granted to the job would bind nothing, and the job would run without the
rules it asked for — so the declaration is an error, not a silent no-op.
(Go's Validate refuses the same thing: "namespacedRBAC is not allowed on a
job with before".)
"""

# expect: namespacedRBAC

import forge.workloads as fw

_j = fw.Workload {
    kind = "job"
    name = "idp-provision"
    args = ["auth", "idp-provision"]
    before = [fw.BEFORE_ALL]
    namespacedRBAC = [
        {apiGroups = [""], resources = ["configmaps"], verbs = ["get", "create", "update", "patch"]}
    ]
}
