"""
Negative test — a HOSTED workload's forge.WorkloadURL naming a HOST workload.

A hosted workload's references are resolved by the control plane, against
workloads IT runs. A host workload is a process on the author's machine: the
platform cannot reach it and cannot resolve its URL, so the reference would
wait forever (or resolve to localhost inside the hosted pod — the platform's
own loopback). Refused at render, naming both workloads.

The legal cross-runtime directions are host/cluster -> anything forge can
resolve at render (positive_workload_url_cross_runtime.k) and
hosted -> hosted.
"""

# expect: CORS_ORIGINS
# expect: devbox

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [
        fw.Workload {
            name = "devbox"
            build = forge.GoBuild {cmd = "./cmd/acme"}
            args = ["devbox"]
            runtime = forge.OnHost {listen_ports = [8085]}
        }
        fw.Workload {
            name = "api"
            image = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            env = {CORS_ORIGINS = forge.WorkloadURL {workload = "devbox"}}
            runtime = forge.OnHosted {}
        }
    ]
})
