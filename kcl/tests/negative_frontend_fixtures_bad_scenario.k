"""
Negative test — Fixtures.scenario must be a scenario name, the lowercase
kebab-case `forge scaffold scenario <name>` accepts: anything else names a
scenario no frontend can have.
"""

# expect: Fixtures.scenario 'Shop Demo' must be a scenario name

import forge

_f = forge.Fixtures {scenario = "Shop Demo"}
