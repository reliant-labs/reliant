"""
Negative test — a base_path that does not start with / is refused.
"""

# expect: API 'billing'.base_path 'billing' must start with

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    apis = [forge.API {name = "billing", kind = "openapi", spec = "openapi.yaml", base_path = "billing"}]
})
