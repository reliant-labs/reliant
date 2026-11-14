"""
Negative test — a schema-only database must declare migrations.
"""

# expect: declare no migrations

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [forge.Database {name = "app"}]
})
