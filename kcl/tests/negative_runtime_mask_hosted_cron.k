"""
Negative test — the HOSTED runtime refuses kind='cron' (ADR 0002 §3:
deferred until metering covers scheduled pods).

Restricted allows kinds service, worker and job. A cron on shared nodes
would run pods the platform does not yet bill, so it is refused at author
time, naming the workload and the kind, rather than published and silently
dropped by the control plane.
"""

# expect: nightly
# expect: cron

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [fw.Workload {
        name = "nightly"
        kind = "cron"
        image = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        args = ["sweep"]
        schedule = "@daily"
        runtime = forge.OnHosted {}
    }]
})
