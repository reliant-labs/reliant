"""
Negative test — inline inputs and an input-provider selector together are
refused: every binding would be rendered from two authorities.
"""

# expect: inputs and inputs_from are exclusive

import forge

_rs = forge.flux_delivery_resourceset("forge-delivery", "flux-system", inputs = [{env = "prod"}], inputs_from = {matchLabels = {"forge.dev/delivery" = "true"}})
