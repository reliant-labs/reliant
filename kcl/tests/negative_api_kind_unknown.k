"""
Negative test — an API kind outside proto|openapi is refused.
"""

# expect: API 'billing'.kind 'graphql' is not one of

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    apis = [forge.API {name = "billing", kind = "graphql", spec = "schema.graphql"}]
})
