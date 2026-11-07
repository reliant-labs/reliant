"""
Negative test — a misspelled placement key is refused instead of silently placing nothing.
"""

# expect: placement keys must be

import forge

_c = forge.flux_chart(placement = {nodeselector = {a = "b"}})
