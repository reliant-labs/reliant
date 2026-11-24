"""
Negative test — fixtures and the build-time mock switch are not combined on
one frontend: config.mock bakes mock mode into the build, fixtures switch the
same build per env, and declaring both leaves which one answers to the
transport's precedence.
"""

# expect: declares both fixtures and config.mock = "true"

import forge

_fe = forge.Frontend {
    name = "web"
    path = "web"
    type = "nextjs"
    config = forge.FrontendConfig {mock = "true"}
    fixtures = forge.Fixtures {scenario = "shop"}
}
