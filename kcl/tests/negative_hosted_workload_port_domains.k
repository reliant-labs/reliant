"""
Negative test — `domains` on an OnHosted workload's port is REFUSED.

A hosted custom hostname is not spec. The name needs an action at YOUR
registrar, ownership verification, and a certificate — all asynchronous and
human-gated, none of which a deploy converges. It is also per environment,
while one workloads.k renders to many, so a domain in the spec would bind to
every env at once.

So the hosted domain lives in the control plane as its own resource:
`forge domain add`, then `forge domain bind` to the env you want it on.
OnCluster keeps `Port.domains` — there you own the ingress and forge's own
Gateway/HTTPRoute serves the name.
"""

# expect: domains [api.hounders.club]
# expect: a control-plane resource, not spec
# expect: forge domain add api.hounders.club
# expect: forge domain bind

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "hounders"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "membership"
        image = "ghcr.io/hounders/membership:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True, domains = ["api.hounders.club"]}]
        runtime = forge.OnHosted {}
    }]
})
