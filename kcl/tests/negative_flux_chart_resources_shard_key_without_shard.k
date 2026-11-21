"""
Negative test — sizing the connected shard's controller without connected_shard = True is refused: that controller does not exist.
"""

# expect: only exists with connected_shard = True

import forge

_c = forge.flux_chart(resources = {kustomize_connected = {requests = {cpu = "50m"}}})
