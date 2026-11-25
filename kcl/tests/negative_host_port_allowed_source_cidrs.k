"""
Negative test — `allowedSourceCIDRs` on a forge.OnHost port is REFUSED.

A local process has no ingress forge controls, so the allowlist could not be
enforced. Only forge.OnHosted enforces it.
"""

# expect: workload 'feeds' cannot run on the host runtime
# expect: port http: allowedSourceCIDRs
# expect: Only forge.OnHosted enforces a per-port source allowlist

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {
        name = "feeds"
        image = "ghcr.io/acme/feeds:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True, allowedSourceCIDRs = ["188.34.190.188/32"]}]
        runtime = forge.OnHost {}
    }]
})
