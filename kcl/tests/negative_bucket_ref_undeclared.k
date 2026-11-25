"""
Negative test — a forge.BucketRef must name a bucket the env declares.
"""

# expect: forge.BucketRef names bucket 'uploads', which this environment does not declare

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {name = "api", image = _img, env = forge.bucket_env("UPLOADS", "uploads"), runtime = forge.OnCluster {target = _k3d}}]
})
