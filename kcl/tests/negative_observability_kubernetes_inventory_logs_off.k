"""Kubernetes inventory ships logs, so it is refused with logs switched off."""
# expect: Observability.agent.kubernetes_inventory ships the cluster's objects as logs
import forge

_obs = forge.Observability {
    destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
    signals = forge.ObservabilitySignals {logs = False}
    agent = forge.ObservabilityAgent {kubernetes_inventory = forge.KubernetesInventory {}}
}
