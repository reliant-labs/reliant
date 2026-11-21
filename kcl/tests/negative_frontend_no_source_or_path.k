"""
Negative test — a Frontend must declare a source of code.

`path` became optional so `source` could replace it; that must not turn
into "neither is required". A frontend with no code location is a build
that has nothing to build.

Expected: `kcl run` FAILS on the Frontend check.
"""

# expect: Frontend needs a source of code

import forge

_fe = forge.Frontend {
    name = "orphan-web"
    type = "nextjs"
}

output = forge.render(forge.Bundle { project = "fixture", frontends = [_fe] })
