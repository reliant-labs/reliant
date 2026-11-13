"""
Negative test — a connected shard with the default (open) egress on both controllers is refused: the controller that holds hub impersonation could still reach the internet, so the split would protect nothing.
"""

# expect: needs explicit egress

import forge

_c = forge.flux_chart(connected_shard = True)
