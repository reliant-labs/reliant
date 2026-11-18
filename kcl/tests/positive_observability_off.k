"""
Positive test — no destination, no dangling endpoint. An env that declares no
observability, or declares it disabled, injects NO OTLP endpoint on any
runtime, renders no agent, and leaves the derived service identity alone.
(The old forge.OTEL_ENV default pointed at a collector nothing deployed.)
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_go = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
_ws = [
    fw.Workload {name = "api", image = "localhost:5050/demo", build = _go, args = ["api"], runtime = forge.OnCluster {target = _k3d}}
    fw.Workload {name = "local", build = _go, args = ["local"], runtime = forge.OnHost {}}
]

_unset = forge.render(forge.Bundle {project = "demo", workloads = _ws})
_disabled = forge.render(forge.Bundle {project = "demo", workloads = _ws, observability = forge.Observability {enabled = False}})
output = _unset

_otlp = lambda o: any -> [str] {
    [e.name for w in o.workloads for e in w.spec.env if e.name.startswith("OTEL_EXPORTER_OTLP") or e.name.endswith("_EXPORTER")]
}

assert_unset_injects_nothing = len(_otlp(_unset)) == 0
assert_disabled_injects_nothing = len(_otlp(_disabled)) == 0
assert_no_agent_when_unset = len([m for m in _unset.manifests if m.kind == "ObservabilityAgent"]) == 0
assert_no_agent_when_disabled = len([m for m in _disabled.manifests if m.kind == "ObservabilityAgent"]) == 0
assert_service_identity_still_derived = len([e for w in _unset.workloads if w.name == "api" for e in w.spec.env if e.name == "OTEL_SERVICE_NAME" and e.value == "api"]) == 1
