"""hounders prod — a HOSTED env: forge.ControlPlane carried the registry.

Every workload binds forge.OnHosted, and none declares an image. The registry
lives on the ControlPlane declaration, as a literal.

Two shapes here are copied from the real tree because they are what defeated
the reader, and a fixture without them is not evidence:

  * membership is bound through an env-local REFINEMENT (`_membership =
    wl.membership | {…}`, then `_hosted(_membership)`). prod gives it its CORS
    origin this way. A reader that matches only `wl.<name>` next to a binder
    sees no binding for membership, concludes nothing here pulls its image, and
    places no registry on it — while still removing the registry.
  * the frontend is declared INLINE in the Bundle's `frontends` list, which is
    what forge scaffolds. A reader that requires a top-level `web = ...`
    assignment finds no frontend at all, so the hosted site keeps no reference
    and prod refuses at render with "image is REQUIRED on forge.OnHosted".
"""

import forge
import forge.workloads as fw
import ..workloads as wl

control_plane = forge.ControlPlane {
    registry = "ghcr.io/reliant-01"
}

_hosted = lambda w: fw.Workload -> fw.Workload {
    w | {runtime = forge.OnHosted {}}
}

# The env's refinement of a shared workload: prod alone knows the site origin
# that may call this API.
_membership = wl.membership | {
    env = wl.membership.env | {CORS_ORIGINS = "https://hounders.club"}
}

_bundle = forge.Bundle {
    project = "hounders"
    control_plane = control_plane
    frontends = [forge.Frontend {
        name = "web"
        path = "frontends/web"
        public_dir = "out"
        runtime = forge.OnHosted {}
    }]
    workloads = [
        wl.migrate | {runtime = forge.OnHosted {}}
        _hosted(_membership)
    ]
}

output = forge.render(_bundle)
