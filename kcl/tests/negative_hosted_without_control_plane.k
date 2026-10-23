"""
Negative test — a workload bound to OnHosted in a Bundle with no
control_plane.

Hosting is per workload now (ADR 0002 §2), so `control_plane` is required
exactly when SOME workload binds Hosted — there is nowhere to publish it
otherwise. The refusal names the workload.

Replaces negative_hosted_simple_backend_cluster.k: a hosted workload that
names cluster coordinates is unrepresentable (OnHosted has no fields), so the
remaining failure mode is a hosted workload with no platform to host it.
"""

# expect: control_plane
# expect: api

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
})
