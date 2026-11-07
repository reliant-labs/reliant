"""
Negative test — declaring egress for some controllers only is refused: every flux-system pod becomes default-deny, so an unnamed controller would silently lose all egress.
"""

# expect: but not for

import forge

_c = forge.flux_chart(egress = {kustomize = []})
