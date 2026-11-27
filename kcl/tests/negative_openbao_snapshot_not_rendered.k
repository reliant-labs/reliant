"""
Negative test — a declared Raft snapshot that this forge does not render is
refused, naming the escape: a backup that is declared and does not run is worse
than none.
"""

# expect: snapshot CronJob is not rendered yet
# expect: OpenBaoPostgres storage

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo"}
    openbao = forge.OpenBao {
        storage = forge.OpenBaoRaft {snapshot = forge.OpenBaoSnapshot {bucket = "gs://acme-backups"}}
    }
})
