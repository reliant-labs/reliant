"""
Negative test — a dialect forge does not know is refused.
"""

# expect: dialect 'postgress' is not one forge knows

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [forge.Database {name = "app", dialect = "postgress", migrations = forge.DatabaseMigrations {}}]
})
