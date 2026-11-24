"""
Negative test — a resources key that names no flux-system controller is refused, so a typo cannot silently leave a controller at forge's sizing.
"""

# expect: resources keys must be

import forge

_c = forge.flux_chart(resources = {kustomise = {requests = {cpu = "50m"}}})
