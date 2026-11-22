"""
Negative test — two databases never share a migrations directory.
"""

# expect: two databases declare the same migrations dir

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    databases = [forge.ManagedDatabase {name = "app", migrations = forge.DatabaseMigrations {dir = "./db/migrations/"}}]
    schema_only_databases = [forge.Database {name = "analytics", migrations = forge.DatabaseMigrations {}}]
})
