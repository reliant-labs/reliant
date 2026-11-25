"""
Closed-signature test — the delivery template is not a knob.

The template's safety stamps (the prune opt-out, deletionPolicy Orphan, the
connected-shard label) are why it exists; an argument that substituted another
template would be a way to render without them. flux_delivery_resourceset
declares no such argument, so KCL refuses one at compile time.
"""

# expect: unexpected keyword argument 'template'

import forge

_rs = forge.flux_delivery_resourceset("delivery.prod", "flux-acme", inputs = [{env = "prod"}], template = "---\n")
