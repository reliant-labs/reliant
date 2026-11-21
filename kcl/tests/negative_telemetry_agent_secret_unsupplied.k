"""
Negative test — a Telemetry credential nothing supplies to the node agent.

forge's node agent mounts every Secret its targets read, non-optionally, from
its own namespace; one nobody declares would stop the agent, so the render
refuses it and names the service, the Secret, its key and the namespace.
"""

# expect: openbao: secret openbao-metrics key token
# expect: forge-observability

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    required_secrets = [forge.ExternalSecret {name = "otlp-auth", namespace = "forge-observability", keys = ["headers"]}]
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.example.com"
            headers_secret = forge.ObservabilitySecretRef {name = "otlp-auth", key = "headers"}
        }
        telemetry = [forge.Telemetry {
            service_name = "openbao", cluster = "k3d-demo", namespaces = ["openbao"], selector = {"app.kubernetes.io/name" = "openbao"}
            metrics = [forge.MetricsScrape {port = 8200, scheme = "https", bearer_token = forge.SecretRef {name = "openbao-metrics", key = "token"}}]
        }]
    }
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})
