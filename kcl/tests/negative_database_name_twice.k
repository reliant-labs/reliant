"""
Negative test — a database name is unique across every kind of database.
"""

# expect: a database name is declared twice across databases, external_databases and schema_only_databases

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    databases = [forge.ManagedDatabase {name = "app"}]
    schema_only_databases = [forge.Database {name = "app", migrations = forge.DatabaseMigrations {dir = "x"}}]
})
