"""
Negative test — a delivery ResourceSet with no ServiceAccount is refused. One
that names none runs as flux-operator's --default-service-account, or, in the
operator's own namespace, as the operator itself.
"""

# expect: service_account is required

import forge

_rs = forge.flux_delivery_resourceset("delivery.prod", "flux-acme", inputs = [{env = "prod"}], service_account = "")
