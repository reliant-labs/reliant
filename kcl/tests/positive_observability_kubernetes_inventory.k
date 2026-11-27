"""Kubernetes inventory is opt-in, on the node agent. Unset (the default),
an env renders exactly as before: no `spec.kubernetesInventory` on any agent
record. Declared, every agent record (one per cluster the agent runs on)
carries it, with that record's cluster and the declared or default
intervals; with no destination there is no agent, so nothing renders."""
import json
import forge
import forge.workloads as fw

_gpu = forge.ClusterTarget {cluster = "gpu", namespace = "acme-prod"}
_web = forge.ClusterTarget {cluster = "web", namespace = "acme-prod"}
_bundle = lambda agent: forge.ObservabilityAgent, destination: bool -> any {
    forge.render(forge.Bundle {
        project = "acme"
        env = "prod"
        observability = forge.Observability {
            if destination:
                destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
            agent = agent
        }
        workloads = [
            fw.Workload {name = "trainer", image = "registry.example.com/acme/trainer:1", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _gpu}}
            fw.Workload {name = "api", image = "registry.example.com/acme/api:1", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _web}}
        ]
    })
}
_agents = lambda out: any -> [any] {
    [m for m in out.manifests if m.kind == "ObservabilityAgent"]
}
_agent = lambda out: any, cluster: str -> any {
    [m for m in out.manifests if m.kind == "ObservabilityAgent" and m.metadata.labels["forge.dev/cluster"] == cluster][0]
}

# Unset: two agents, neither asks for the inventory.
_off = _bundle(forge.ObservabilityAgent {}, True)
assert_off_agents = len(_agents(_off)) == 2
_off_agents = _agents(_off)
assert_off_no_inventory = all a in _off_agents { "kubernetesInventory" not in a.spec }

# Declared with defaults: each agent record names its own cluster.
_on = _bundle(forge.ObservabilityAgent {kubernetes_inventory = forge.KubernetesInventory {}}, True)
assert_on_gpu = _agent(_on, "gpu").spec.kubernetesInventory == {"cluster": "gpu", "nodeInterval": "60s", "workloadInterval": "300s", "events": True}
assert_on_web = _agent(_on, "web").spec.kubernetesInventory == {"cluster": "web", "nodeInterval": "60s", "workloadInterval": "300s", "events": True}

# Only the agent records change: everything else renders as before.
_drop_inv = lambda out: any -> any {
    [m | {spec = {k: v for k, v in m.spec if k != "kubernetesInventory"}} if m.kind == "ObservabilityAgent" else m for m in out.manifests]
}
assert_additive = json.encode(_drop_inv(_on)) == json.encode(_off.manifests)

# Tuned.
_tuned = _agent(_bundle(forge.ObservabilityAgent {kubernetes_inventory = forge.KubernetesInventory {node_interval = "30s", workload_interval = "10m", events = False}}, True), "gpu")
assert_tuned = _tuned.spec.kubernetesInventory == {"cluster": "gpu", "nodeInterval": "30s", "workloadInterval": "10m", "events": False}

# No destination: no agent, so no inventory either.
assert_no_destination = len(_agents(_bundle(forge.ObservabilityAgent {kubernetes_inventory = forge.KubernetesInventory {}}, False))) == 0
