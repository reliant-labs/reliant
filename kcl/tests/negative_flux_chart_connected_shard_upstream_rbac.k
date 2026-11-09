"""
Negative test — a connected shard on the distribution's own RBAC is refused: that RBAC grants every controller cluster-wide Secret reads and impersonation, which the shard must not have.
"""

# expect: needs rbac

import forge

_c = forge.flux_chart(connected_shard = True, rbac = "upstream", egress = {kustomize = [], kustomize_connected = []})
