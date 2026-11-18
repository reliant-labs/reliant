"""
Negative test — Cloud SQL without a Workload Identity service account is refused.
"""

# expect: needs gcp_service_account

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        storage = forge.OpenBaoPostgres {password = forge.BootstrapSecret {name = "p"}, cloudsql_instance = "a:b:c"}
    }

})
