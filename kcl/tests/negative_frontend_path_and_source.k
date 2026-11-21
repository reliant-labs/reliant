"""
Negative test — a Frontend must not declare BOTH `path` and `source`.

With both set there are two answers to "where is this frontend's code",
and whichever forge silently preferred, the other would be a lie that
reads as truth in review. The supported way to build a local working copy
of a source-declared frontend is a machine-local override in
.forge/source-overrides.yaml — which cannot be committed, so it can never
un-pin CI.

Expected: `kcl run` FAILS on the Frontend check.
"""

# expect: Frontend declares both `path` and `source`

import forge

_fe = forge.Frontend {
    name = "reliant-web"
    type = "vite"
    path = "../reliant/web"
    source = forge.GitSource {
        repo = "github.com/reliant-labs/reliant"
        ref = "v1.6.3"
        subdir = "web"
    }
}

output = forge.render(forge.Bundle { project = "fixture", frontends = [_fe] })
