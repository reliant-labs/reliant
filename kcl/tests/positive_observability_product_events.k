"""
Positive test — the automatic product events switch (signals.product_events).

  * default (True)  -> no FORGE_PRODUCT_EVENTS: the generated CRUD handlers emit
                       <entity>_created|_updated|_deleted;
  * False           -> FORGE_PRODUCT_EVENTS=off on every workload, and nothing
                       else changes (logs still ship, the SDK stays on);
  * an author's own FORGE_PRODUCT_EVENTS wins.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_dest = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}

_on = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {destination = _dest}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_off = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        destination = _dest
        signals = forge.ObservabilitySignals {product_events = False}
    }
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}
        fw.Workload {name = "own", image = "ghcr.io/acme/own", env = {FORGE_PRODUCT_EVENTS = "on"}, runtime = forge.OnCluster {target = _k3d}}
    ]
})

_env = lambda out: any, name: str -> {str:} {
    {e.name: e.value for w in out.workloads if w.name == name for e in w.spec.env if e.value}
}

assert_default_on = "FORGE_PRODUCT_EVENTS" not in _env(_on, "api")
assert_off_renders_off = _env(_off, "api").FORGE_PRODUCT_EVENTS == "off"
assert_off_keeps_logs = "OTEL_LOGS_EXPORTER" not in _env(_off, "api") and "OTEL_SDK_DISABLED" not in _env(_off, "api")
assert_author_wins = _env(_off, "own").FORGE_PRODUCT_EVENTS == "on"
