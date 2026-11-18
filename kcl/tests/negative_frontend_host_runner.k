"""
Negative test — a workload-only OnHost field on a frontend.

forge.OnHost is the workload schema, reused so a frontend binds the same
name. Its workload-only fields mean nothing to a dev server
(`<dev_runner> dev` on Frontend.port), so a declared one is REFUSED, naming
it, instead of being silently ignored.
"""

# expect: frontend 'web' cannot run on the host runtime
# expect: runner
# expect: listen_ports

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {name = "web", path = "web", runtime = forge.OnHost {runner = "air", listen_ports = [3000]}}]
})
