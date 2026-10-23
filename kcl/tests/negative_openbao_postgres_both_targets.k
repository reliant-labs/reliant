"""
Negative test — Postgres with both a Cloud SQL instance and a host is refused.
"""

# expect: exactly one of cloudsql_instance

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        storage = forge.OpenBaoPostgres {password = forge.BootstrapSecret {name = "p"}, cloudsql_instance = "a:b:c", gcp_service_account = "x@y.iam.gserviceaccount.com", host = "pg"}
    }

})
