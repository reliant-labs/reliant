"""
Negative test — ExternalDatabase.port must be a TCP port.
"""

# expect: ExternalDatabase 'ledger'.port 70000 must be between 1 and 65535

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
    external_databases = [forge.ExternalDatabase {
        name = "ledger"
        service = "pgbouncer"
        database = "ledger"
        username = "ledger_ro"
        password_secret = "ledger-ro"
        port = 70000
    }]
})
