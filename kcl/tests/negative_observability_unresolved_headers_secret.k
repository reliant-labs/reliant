"""
Negative test — a destination whose headers secret nothing supplies.

The node agent reads the headers through a secretKeyRef in its own namespace;
a Secret nobody provides leaves the DaemonSet in CreateContainerConfigError.
Declaring who supplies it (a required_secrets entry in the agent's namespace)
is what makes the deploy check it.
"""

# expect: headers_secret 'otlp-auth'
# expect: forge-observability

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.example.com"
            headers_secret = forge.ObservabilitySecretRef {name = "otlp-auth", key = "headers"}
        }
    }
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})
