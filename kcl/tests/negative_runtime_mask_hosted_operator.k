"""
Negative test — the HOSTED runtime refuses kind='operator'.

An operator installs CRDs and holds cluster-scoped RBAC — the two things a
hosted user on shared nodes must never own. Restricted allows service, worker and
job only.
"""

# expect: mgr
# expect: operator

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [fw.Workload {
        name = "mgr"
        kind = "operator"
        image = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        crds = ["Widget"]
        clusterRBAC = [{apiGroups = ["example.com"], resources = ["widgets"], verbs = ["list"]}]
        runtime = forge.OnHosted {}
    }]
})
