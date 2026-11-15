"""
Positive test — ExternalDatabase.reach and port reach the rendered record.

  1. Unset, reach is port_forward and port is absent (the dialer picks 5432
     or the Service's only port).
  2. direct with a pooler port is carried as declared.
"""

import forge

_out = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "demo-prod"}
    external_databases = [
        forge.ExternalDatabase {
            name = "ledger"
            service = "pgbouncer"
            database = "ledger"
            username = "ledger_ro"
            password_secret = "ledger-ro"
        }
        forge.ExternalDatabase {
            name = "platform"
            service = "pgbouncer"
            database = "platform_ro"
            username = "platform_ro"
            password_secret = "platform-ro"
            reach = "direct"
            port = 6432
        }
    ]
})
_x = {x.name: x for x in _out.external_databases}

assert_default_reach = _x["ledger"].reach == "port_forward"
assert_default_port_absent = not _x["ledger"].port
assert_direct = _x["platform"].reach == "direct"
assert_direct_port = _x["platform"].port == 6432
assert_direct_namespace = _x["platform"].namespace == "demo-prod"
