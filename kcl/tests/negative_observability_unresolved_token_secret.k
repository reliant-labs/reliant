"""
Negative test — a token_secret nothing supplies.

The agent mounts the token Secret; the deploy can only check it exists if
somebody declares who provides it. The refusal names token_secret, not
headers_secret.
"""

# expect: token_secret 'otlp-token'
# expect: forge-observability

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.example.com"
            token_secret = forge.ObservabilitySecretRef {name = "otlp-token", key = "token"}
        }
    }
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})
