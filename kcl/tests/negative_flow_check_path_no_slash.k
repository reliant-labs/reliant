"""
Negative test — FlowCheck.path must start with '/'.

The check's URL is resolved from the workload's route or port plus this path,
so a path with no leading slash would produce a malformed URL at smoke time.
"""

# expect: FlowCheck.path must start with '/'

import forge
import forge.workloads as fw

_w = fw.Workload {
    name = "gw"
    build = forge.GoBuild {cmd = "./cmd/gw", output_name = "gw"}
    flow_checks = [forge.FlowCheck {path = "flow-health"}]
}

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [_w | {runtime = forge.OnHost {listen_ports = [8081]}}]
})
