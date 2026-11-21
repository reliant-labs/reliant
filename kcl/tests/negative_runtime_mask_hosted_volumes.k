"""
Negative test — the HOSTED runtime refuses `volumes` (ADR 0002 §3, Restricted
profile).

Volumes are Full-only pod plumbing; hosted storage is `storageGiB`, which the platform provisions, classes and floors itself.

The refusal is per WORKLOAD: its effective runtime is OnHosted (its own
`runtime`), so the check lives where that is known —
the Bundle — and names the workload, the field and the reason. The KCL check
is the author-time mirror; Go's Workload.Validate(ProfileRestricted) is the
real gate, server-side.
"""

# expect: api
# expect: volumes

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
        volumes = [fw.Volume {name = "cfg", mountPath = "/etc/cfg", source = fw.VolumeSource {emptyDir = fw.EmptyDirVolumeSource {}}}]
    }]]
})
