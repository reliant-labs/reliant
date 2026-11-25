"""
Negative test — the HOSTED runtime refuses `sidecars` (ADR 0002 §3, Restricted
profile).

A sidecar is a second, unmetered, unvalidated container in the hosted pod — outside the resource band the platform bills and admits.

The refusal is per WORKLOAD: its effective runtime is OnHosted (its own
`runtime`), so the check lives where that is known —
the Bundle — and names the workload, the field and the reason. The KCL check
is the author-time mirror; Go's Workload.Validate(ProfileRestricted) is the
real gate, server-side.
"""

# expect: api
# expect: sidecars

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [_w | {runtime = forge.OnHosted {}} if not _w.runtime else _w for _w in [fw.Workload {
        name = "api"
        kind = "service"
        image = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        sidecars = [fw.Container {name = "proxy", image = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1"}]
    }]]
})
