"""
Positive test — forge.OnHosted declares the nodes' arch.

A hosted image's arch is a property of the platform, not of the build machine,
so the runtime carries `platform` (default amd64) in the render for forge's
build to read.
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [
        fw.Workload {
            name = "api"
            image = "ghcr.io/acme/api:v1"
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            runtime = forge.OnHosted {}
        }
        fw.Workload {
            name = "arm"
            image = "ghcr.io/acme/arm:v1"
            ports = [fw.Port {name = "http", port = 8081, expose = True}]
            runtime = forge.OnHosted {platform = "arm64"}
        }
    ]
})

assert_default_amd64 = output.workloads[0].runtime.platform == "amd64"
assert_override = output.workloads[1].runtime.platform == "arm64"
