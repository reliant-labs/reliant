"""
Negative test — fixtures are runtime config, and a React Native frontend has
no runtime config document to carry them.
"""

# expect: fixtures is for a web frontend (nextjs, vite)

import forge

_fe = forge.Frontend {
    name = "mobile"
    path = "mobile"
    type = "rn"
    fixtures = forge.Fixtures {scenario = "shop"}
}
