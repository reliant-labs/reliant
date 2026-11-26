"""
Positive test — Observability.destination.token_from_control_plane = True
declares a ControlPlaneSecret for the node agent on every cluster that runs
one, so a customer writes nothing but the destination.

The agent reads the bare `token` key from Secret `forge-otlp-token` in its own
namespace (the node agent mounts it as a file and re-reads it), so the
ObservabilityAgent record carries tokenSecret and no headersSecret, and the
derived ControlPlaneSecret satisfies the "credential must be supplied" check
without a required_secrets entry.
"""

import forge
import forge.workloads as fw

_a = forge.ClusterTarget {cluster = "gke_acme_a", namespace = "acme-a", connected_cluster = "acme-a", platform = "amd64"}
_b = forge.ClusterTarget {cluster = "gke_acme_b", namespace = "acme-b", connected_cluster = "acme-b", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.reliantapi.com"
            token_from_control_plane = True
        }
    }
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _a}}
        fw.Workload {name = "worker", image = "ghcr.io/acme/worker", runtime = forge.OnCluster {target = _b}}
    ]
})

_cps = {s.cluster: s for s in output.control_plane_secrets}
assert_one_secret_per_agent_cluster = len(_cps) == 2
assert_secret_name = _cps["gke_acme_a"].name == "forge-otlp-token"
assert_secret_lands_in_the_agent_namespace = _cps["gke_acme_a"].namespace == "forge-observability"
assert_secret_is_a_telemetry_token = _cps["gke_acme_b"].kind == "telemetry_token"
assert_each_delivers_through_its_own_connected_cluster = _cps["gke_acme_a"].connected_cluster == "acme-a" and _cps["gke_acme_b"].connected_cluster == "acme-b"

_agents = [m for m in output.manifests if m.kind == "ObservabilityAgent"]
assert_one_agent_per_cluster = len(_agents) == 2
assert_agent_reads_the_token = _agents[0].spec.tokenSecret.name == "forge-otlp-token" and _agents[0].spec.tokenSecret.key == "token"
assert_agent_has_no_headers_secret = "headersSecret" not in _agents[0].spec or _agents[0].spec.headersSecret == None
assert_declaration_says_so = output.observability.destination.token_from_control_plane == True
assert_declaration_token_secret = output.observability.destination.token_secret.name == "forge-otlp-token"

# The workloads are not in the agent's namespace, so they do not read the
# agent's Secret: they export to the agent, which holds the credential.
_by_name = {w.name: w for w in output.workloads}
assert_workloads_get_no_volume = "volumes" not in _by_name["api"].spec
