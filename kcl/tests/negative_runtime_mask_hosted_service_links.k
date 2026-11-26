"""
Negative test — the HOSTED runtime refuses `enableServiceLinks` (Restricted
profile).

Service links inject an env var per Service in the pod's namespace. A hosted
namespace is platform-owned, so the author has no business reading its
Services. Refused per WORKLOAD, naming the field and the reason; Go's
Workload.Validate(ProfileRestricted) is the real gate, server-side.
"""

# expect: api
# expect: enableServiceLinks

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
        enableServiceLinks = True
    }]]
})
