"""
Negative test — API names are unique.
"""

# expect: an API name is declared twice

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    apis = [
        forge.API {name = "billing", kind = "openapi", spec = "a.yaml"}
        forge.API {name = "billing", kind = "openapi", spec = "b.yaml"}
    ]
})
