"""
Negative test — a dialect forge knows but does not support yet is refused as such.
"""

# expect: dialect 'mysql' is not supported yet

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [forge.Database {name = "app", dialect = "mysql", migrations = forge.DatabaseMigrations {}}]
})
