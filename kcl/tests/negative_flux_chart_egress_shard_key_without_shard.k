"""
Negative test — egress for the connected controller without connected_shard is refused: it would select no pod.
"""

# expect: only exists with connected_shard

import forge

_c = forge.flux_chart(egress = {kustomize_connected = []})
