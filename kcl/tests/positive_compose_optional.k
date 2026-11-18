"""
Positive test — an optional compose service, and a workload that requires it.

`OnCompose.optional` declares a service the app does not need in order to run;
`Workload.requires` names the workloads a host process is only useful beside.
`forge env up` reads both to skip them when no Docker daemon answers.

  1. `optional` reaches the render only when set, so an env that never says it
     renders byte-identically to what it did before the field.
  2. `requires` reaches the entity only when set.
"""

import forge
import forge.workloads as fw

_collector = fw.Workload {
    name = "clickstack"
    runtime = forge.OnCompose {service = "clickstack", optional = True}
}
_postgres = fw.Workload {
    name = "postgres"
    runtime = forge.OnCompose {service = "postgres"}
}
_alerts = fw.Workload {
    name = "alerts"
    kind = "worker"
    command = ["forge", "obs", "alerts", "run"]
    requires = ["clickstack"]
    runtime = forge.OnHost {listen_ports = []}
}

output = forge.render(forge.Bundle {project = "demo", env = "dev", workloads = [_collector, _postgres, _alerts]})
_w = {w.name: w for w in output.workloads}

assert_optional_rendered = _w["clickstack"].runtime.optional == True
assert_required_has_no_optional_key = "optional" not in _w["postgres"].runtime
assert_requires_rendered = _w["alerts"].requires == ["clickstack"]
assert_no_requires_key_when_unset = "requires" not in _w["postgres"]
