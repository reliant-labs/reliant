"""
Negative test — Frontend.fixtures publishes FIXTURES_SCENARIO and
FIXTURES_READ_ONLY, so a runtime_config entry for either beside it is
refused rather than left to shadow it (a hand-set FIXTURES_READ_ONLY = "false"
would quietly turn the read-only default off).
"""

# expect: runtime_config sets FIXTURES_SCENARIO or FIXTURES_READ_ONLY

import forge

_fe = forge.Frontend {
    name = "web"
    path = "web"
    type = "vite"
    runtime_config = {FIXTURES_READ_ONLY = "false"}
    fixtures = forge.Fixtures {scenario = "shop"}
}
