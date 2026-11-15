"""
Positive test — a workload bound to forge.OnHosted may declare a bare image
NAME, with no registry host (ADR-0003 F1).

This was a NEGATIVE fixture: forge refused it and told the author to declare
the full reference. The registry does live on the image for every runtime
whose registry the author chooses — but forge.OnHosted is the one case where
they do not choose it. The control plane admits images from exactly one
subtree, `<registry_base>/<org>`, and refuses every other, so a hosted author
writing a host was transcribing a value the platform already knew, and getting
it wrong produced a publish-time refusal.

The render keeps the bare name VERBATIM and resolves nothing: KCL has no
network, and a render must stay an offline, reproducible projection. forge
composes `<image_push_base>/<name>` where the push base is known — in the
build's push plan and the deploy's pinned spec, both of which read the base
from the EnsureEnvironment they already make.

A cluster workload's bare image is still refused
(negative_cluster_image_no_registry.k): no platform owns that registry.
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "app"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example.com"}
    workloads = [fw.Workload {
        name = "api"
        image = "api"
        ports = [fw.Port {name = "http", port = 8080}]
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {
        name = "site"
        image = "site"
        path = "site"
        type = "vite"
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
})

_w = output.workloads[0]
# The bare name survives the render untouched. forge appends the platform's
# base later, where it is known; a host invented here would be a guess baked
# into a document the author diffs against the cluster.
assert_bare_image_kept = _w.spec.image == "api"
assert_runtime_hosted = _w.runtime.type == "hosted"
# A hosted FRONTEND takes the same bare name, for the same reason. forge
# appends `static.v1` to the resolved reference, not to this one.
assert_bare_frontend_image_kept = output.frontends[0].image == "site"
