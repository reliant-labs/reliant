"""
Negative test — `allowedSourceCIDRs` on a forge.OnCluster port is REFUSED.

forge renders a Gateway and an HTTPRoute for a cluster workload but sets no
client-address detection, and a NetworkPolicy sees the gateway pod rather than
the client. Nothing forge renders can turn a client away by source address, so
accepting the field would serve every client under a declaration that reads
"restricted". Only forge.OnHosted enforces it.
"""

# expect: workload 'feeds' cannot run on the cluster runtime
# expect: port http: allowedSourceCIDRs
# expect: 188.34.190.188/32
# expect: Only forge.OnHosted enforces a per-port source allowlist

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {
        name = "feeds"
        image = "ghcr.io/acme/feeds:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True, allowedSourceCIDRs = ["188.34.190.188/32"]}]
        runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-prod"}}
    }]
})
