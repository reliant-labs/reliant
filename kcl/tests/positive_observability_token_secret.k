"""
Positive test — a destination authenticated by a renewable bearer token.

token_secret names a Secret key holding the bare token. The node agent mounts
it as a file (Go side) and re-reads it on change, so the record carries
tokenSecret and no headersSecret, and the supply check resolves the token's
Secret through required_secrets exactly like headers_secret does.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "staging"
    required_secrets = [forge.ExternalSecret {name = "otlp-token", namespace = "forge-observability", keys = ["token"]}]
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.example.com"
            token_secret = forge.ObservabilitySecretRef {name = "otlp-token", key = "token"}
        }
    }
    workloads = [fw.Workload {name = "api", image = "localhost:5050/demo", runtime = forge.OnCluster {target = _k3d}}]
})

_agents = [m for m in output.manifests if m.kind == "ObservabilityAgent"]

assert_one_agent_record = len(_agents) == 1
assert_record_carries_the_token_secret = _agents[0].spec.tokenSecret.name == "otlp-token" and _agents[0].spec.tokenSecret.key == "token"
assert_record_carries_no_headers_secret = "headersSecret" not in _agents[0].spec or _agents[0].spec.headersSecret == None
assert_declaration_projected = output.observability.destination.token_secret.name == "otlp-token"
