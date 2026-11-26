"""
Positive test — an env that declares no forge.Telemetry renders exactly as it
did before Telemetry existed: no `telemetry` key, no `temporal_metrics_port`
on its infra, and the explicit empty defaults (`Manifests.telemetry = []`,
`Observability.telemetry = []`) render byte-identically to leaving them out.
(The render-contract goldens in internal/cli/testdata/render_contract, which
predate Telemetry and are unchanged, prove the same for every runtime.)
"""

import json
import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

_base = lambda tel: bool -> any {
    forge.Bundle {
        project = "demo"
        env = "dev"
        observability = forge.Observability {local_port = 4318, telemetry = []} if tel else forge.Observability {local_port = 4318}
        workloads = [
            fw.Workload {name = "api", image = "docker.io/example/api:1", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _k3d}}
            fw.Workload {name = "web", image = "docker.io/example/web:1", command = ["/usr/bin/web"], runtime = forge.OnHost {}}
        ]
        infra = [forge.HostInfra {name = "temporal", engine = "temporal", port = 7233}]
        manifests = [forge.Manifests {name = "extra", telemetry = []} if tel else forge.Manifests {name = "extra"}]
    }
}

_without = forge.render(_base(False))
_explicit = forge.render(_base(True))

assert_identical = json.encode(_without) == json.encode(_explicit)
assert_no_telemetry_key = "telemetry" not in _without
assert_no_metrics_port_key = all i in _without.infra { "temporal_metrics_port" not in i }
assert_standard_service_name = [e.value for e in [w for w in _without.workloads if w.name == "web"][0].spec.env if e.name == "OTEL_SERVICE_NAME"] == ["web"]
