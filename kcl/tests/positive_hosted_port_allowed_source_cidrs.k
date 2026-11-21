"""
Positive test — `Port.allowedSourceCIDRs` on a forge.OnHosted workload.

Hosted is the one runtime that enforces a per-port source allowlist (the
platform proxy checks the client address and answers 403 for any other), so
the field rides the Workload record untouched: the control plane reads it
from the spec, and forge normalizes the spelling when it publishes.
"""

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/feeds@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [fw.Workload {
        name = "feeds"
        image = _img
        ports = [fw.Port {
            name = "http"
            port = 8080
            expose = True
            allowedSourceCIDRs = ["188.34.190.188/32", "65.108.48.77/32", "37.24.226.100/32", "89.246.73.244/32", "85.115.25.67/32"]
        }]
        runtime = forge.OnHosted {}
    }]
})

_ports = output.workloads[0].spec.ports
assert_cidrs_pass_through = _ports[0].allowedSourceCIDRs == ["188.34.190.188/32", "65.108.48.77/32", "37.24.226.100/32", "89.246.73.244/32", "85.115.25.67/32"]
assert_other_port_fields_unchanged = _ports[0].name == "http" and _ports[0].expose == True
