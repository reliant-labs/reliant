"""
Closed schema — `forge.Frontend` no longer declares `domains`.

The field existed for exactly one runtime (OnHosted) and was refused on
every other. Hosted domains are now control-plane resources, bound per
environment with `forge domain add` + `forge domain bind`, so the field has
no remaining valid runtime and is DELETED rather than left as a value forge
would have to refuse everywhere it can be written.

KCL refuses the undeclared member by name at compile time. That is the whole
signal a stale `domains = [...]` needs: the name it wrote no longer exists.
"""

# expect: domains
# expect: Frontend

import forge

_fe = forge.Frontend {
    name = "web"
    image = "ghcr.io/acme/web"
    path = "web"
    domains = ["hounders.club"]
    runtime = forge.OnHosted {}
}
