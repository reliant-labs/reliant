"""
Negative test — a managed database does not belong on schema_only_databases.
"""

# expect: are not bare forge.Database declarations

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [forge.ManagedDatabase {name = "app", migrations = forge.DatabaseMigrations {}}]
})
