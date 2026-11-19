"""
Negative test — forge's own migrator takes no command.
"""

# expect: but driver is forge

import forge

output = forge.render(forge.Bundle {
    project = "shop"
    cluster_target = forge.ClusterTarget {cluster = "c", namespace = "shop-prod"}
    schema_only_databases = [forge.Database {name = "app", migrations = forge.DatabaseMigrations {apply = ["goose", "up"]}}]
})
