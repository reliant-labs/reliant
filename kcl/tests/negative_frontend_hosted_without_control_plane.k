"""
Negative test — a frontend on forge.OnHosted in an env with no control_plane.

Hosted is per frontend, as it is per workload, and it still needs somewhere
to publish to.
"""

# expect: web
# expect: control_plane

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {name = "web", image = "ghcr.io/acme/web", path = "web", type = "vite", runtime = forge.OnHosted {}}]
})
