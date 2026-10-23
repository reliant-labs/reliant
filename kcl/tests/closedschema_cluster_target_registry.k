"""
Negative test — `registry` is NOT a field on forge.ClusterTarget. An
environment does not have a registry; a workload does, as part of its image.

ClusterTarget is a closed schema, so a leftover `registry = …` from before the
field was removed is a KCL load error rather than a silently ignored line —
which matters, because ignoring it would leave the author believing their
images were going somewhere they were not.

The companion for the hosted half is negative_control_plane_registry_field.k;
`forge generate` migrates the field automatically (see the migration in
internal/cli/upgrade_migrations*.go).
"""

# expect: registry

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_proj_us-central1_prod"
    namespace = "app-prod"
    registry = "ghcr.io/acme"
    platform = "amd64"
}

output = forge.render(forge.Bundle {
    project = "app"
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api"
        runtime = forge.OnCluster {target = _prod}
    }]
})
