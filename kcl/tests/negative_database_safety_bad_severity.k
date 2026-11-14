"""
Negative test — a migration-safety dial outside error|warn|off is refused.
"""

# expect: DatabaseMigrationSafety: unsafe_add_column, destructive_change and volatile_default are each one of

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [forge.Database {name = "app", migrations = forge.DatabaseMigrations {}, migration_safety = forge.DatabaseMigrationSafety {destructive_change = "fatal"}}]
})
