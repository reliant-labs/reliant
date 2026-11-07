"""
Positive test — a workload's flow_checks project onto its render entry, so
`forge env smoke` can resolve each to a URL in the env being smoked. The URL
itself is never declared: only the path, a description and an optional name.
"""

import forge
import forge.workloads as fw

_gw = fw.Workload {
    name = "daemon-gateway"
    build = forge.GoBuild {cmd = "./cmd/gw", output_name = "gw"}
    flow_checks = [forge.FlowCheck {path = "/flow-health", description = "every Ready daemon is attached"}]
}
_plain = fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/api", output_name = "api"}}

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        _gw | {runtime = forge.OnHost {listen_ports = [8081]}}
        _plain | {runtime = forge.OnHost {listen_ports = [8080]}}
    ]
})

_by_name = {w.name: w for w in output.workloads}
assert_check_projected = _by_name["daemon-gateway"].flow_checks == [{"path": "/flow-health", "description": "every Ready daemon is attached", "name": None}]
assert_no_checks_is_absent = "flow_checks" not in _by_name["api"]
