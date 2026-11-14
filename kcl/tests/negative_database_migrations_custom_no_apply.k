"""
Negative test — a custom driver with no apply argv is refused.
"""

# expect: driver is custom but there is no apply command

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [forge.Database {name = "app", migrations = forge.DatabaseMigrations {driver = "custom"}}]
})
