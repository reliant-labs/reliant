"""
Negative test — an API's database must be one the env declares.
"""

# expect: name a database the env does not declare

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    apis = [forge.API {name = "billing", kind = "openapi", spec = "openapi.yaml", database = "ledger"}]
})
