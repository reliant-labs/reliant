"""
Negative test — an egress key that names no flux-system controller is refused, so a typo cannot leave a controller open.
"""

# expect: egress keys must be

import forge

_c = forge.flux_chart(egress = {bogus = []})
