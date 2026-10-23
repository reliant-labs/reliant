"""
Negative test — Frontend.routes cannot combine "none" with real slugs.

`["none"]` means "no generated CRUD pages"; beside real slugs it contradicts
the allowlist. forge refuses the mix rather than guess which was meant. (This
rule used to be checked on forge.yaml's `frontends[].routes`; it lives here now
that the frontend is declared in KCL.)
"""

# expect: combines "none" with route slugs

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {name = "web", path = "frontends/web", routes = ["none", "users"], runtime = forge.OnHost {}}]
})
