"""
Negative test — a forge.BucketRef key is one of the connection facts
forge/pkg/blobstore reads.
"""

# expect: BucketRef.key 'password' must be one of

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_minio = forge.BucketOnMinIO {port = 9123}
_buckets = [forge.Bucket {name = "uploads", runtime = _minio}]

output = forge.render(forge.Bundle {
    project = "acme"
    buckets = _buckets
    workloads = [
        fw.Workload {name = "api", image = _img, env = {X = forge.BucketRef {name = "uploads", key = "password"}}, runtime = forge.OnCluster {target = _k3d}}
        forge.minio_workload(_minio, _buckets)
    ]
})
