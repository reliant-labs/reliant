"""
Negative test — a build-only frontend serves no config.js of its own (its
output is assembled into another frontend's site), so fixtures declared on it
would reach no browser.
"""

# expect: fixtures: a build-only frontend serves no config.js of its own

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    frontends = [forge.Frontend {
        name = "docs"
        path = "docs"
        type = "vite"
        runtime = forge.BuildOnly {}
        fixtures = forge.Fixtures {scenario = "shop"}
    }]
})
