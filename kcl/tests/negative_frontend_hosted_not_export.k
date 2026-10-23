"""
Negative test — a `next start` frontend bound to forge.OnHosted.

Platform static hosting is still static hosting: the control plane publishes
the release's files and runs no Node process for a frontend. The roofers
dogfood scaffold shipped exactly this binding (a `_hosted_frontend` helper
over a standalone frontend) and nothing objected.
"""

# kcl-args: env="staging" frontend_outputs="{\"admin\":\"server\",\"web\":\"static\"}"
# expect: frontend 'admin' cannot run on the hosted runtime
# expect: its next.config builds `output: server` for frontend 'admin'
# expect: forge.OnHosted serves only the files a static export writes to public_dir 'out'
# expect: in env 'staging'

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    frontends = [
        forge.Frontend {name = "web", image = "ghcr.io/acme/web", path = "frontends/web", runtime = forge.OnHosted {}}
        forge.Frontend {name = "admin", image = "ghcr.io/acme/admin", path = "frontends/admin", runtime = forge.OnHosted {}}
    ]
})
