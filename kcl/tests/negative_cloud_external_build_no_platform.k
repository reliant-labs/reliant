"""
Negative test — a workload whose IMAGE names a CLOUD registry, bound to a
cluster and shipping a REAL external image build, MUST have a `platform` on its
target. The local-vs-cloud signal is the image's registry host.

This is the arm64-image-on-amd64-node footgun: without `platform`, the
external build pipeline falls through to the dev HOST's arch, so a `forge
build` from an arm64 Mac pushes arm64 images that exec-format-error on amd64
cloud nodes. The check is per workload now (the effective runtime decides
which target's platform applies), and it names `platform`.
"""

# expect: platform is required

import forge
import forge.workloads as fw

# A cloud target with NO platform stated. The image is what makes it cloud.
_prod = forge.ClusterTarget {
    cluster = "gke_proj_us-central1_prod"
    namespace = "app-prod"
}

output = forge.render(forge.Bundle {
    project = "app"
    workloads = [fw.Workload {
        name = "sibling"
        image = "ghcr.io/org/app/sibling"
        build = forge.DockerBuild {dockerfile = "docker/Dockerfile.sibling"}
        runtime = forge.OnCluster {target = _prod}
    }]
})
