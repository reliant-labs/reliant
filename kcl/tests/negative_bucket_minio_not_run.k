"""
Negative test — a bucket on a forge.BucketOnMinIO needs the workload that
runs that MinIO (forge.minio_workload).
"""

# expect: are on a forge.BucketOnMinIO that no workload runs

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}

output = forge.render(forge.Bundle {
    project = "acme"
    buckets = [forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {port = 9123}}]
    workloads = [fw.Workload {name = "api", image = _img, runtime = forge.OnCluster {target = _k3d}}]
})
