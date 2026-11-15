"""
Negative test — a delivery ResourceSet with neither inputs nor inputs_from is
refused: it would render nothing and garbage-collect what it held.
"""

# expect: needs inputs or inputs_from

import forge

_rs = forge.flux_delivery_resourceset("delivery.prod", "flux-acme")
