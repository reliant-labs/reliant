"""
Negative test — an API spec outside the repository is refused.
"""

# expect: API 'billing'.spec '/srv/openapi.yaml' is not a path inside the repository

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    apis = [forge.API {name = "billing", kind = "openapi", spec = "/srv/openapi.yaml"}]
})
