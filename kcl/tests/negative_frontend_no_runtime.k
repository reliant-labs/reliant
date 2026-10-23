"""
Negative test — a frontend with no runtime.

Every frontend states where it runs, exactly like a workload (ADR 0002 §6).
Omitting it used to mean "dev server" in one place and "build-only" in
another; now it is refused, naming the frontend.
"""

# expect: bind no runtime
# expect: orphan-web

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [
        forge.Frontend {name = "web", path = "web", runtime = forge.OnHost {}}
        forge.Frontend {name = "orphan-web", path = "orphan"}
    ]
})
