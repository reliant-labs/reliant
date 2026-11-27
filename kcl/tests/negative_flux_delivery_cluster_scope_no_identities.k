"""
Negative test — `delivery_scope = "cluster"` scopes the derived apply identity,
and `apply_identities = False` renders none.
"""

# expect: apply_identities = False renders none

import forge

_fd = forge.FluxDelivery {apply_identities = False, delivery_scope = "cluster"}
