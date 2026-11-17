"""
Negative test — ExternalDatabase.reach is an enum: a value that is neither
port_forward nor direct is refused, naming both.
"""

# expect: ExternalDatabase 'ledger'.reach 'tunnel' is not one of
# expect: port_forward
# expect: direct

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
        reach = "tunnel"
    }]
})
