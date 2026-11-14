"""
Negative test — a schema-only database name is an RFC-1123 label.
"""

# expect: a database name is an RFC-1123 label of at most 63 characters

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [forge.Database {name = "App_DB", migrations = forge.DatabaseMigrations {}}]
})
