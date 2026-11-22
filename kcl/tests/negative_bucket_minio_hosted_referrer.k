"""
Negative test — a hosted workload cannot reach the env's local MinIO.
"""

# expect: is on forge.BucketOnMinIO, the env's local MinIO, which a hosted workload cannot reach

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_minio = forge.BucketOnMinIO {port = 9123}
_buckets = [forge.Bucket {name = "uploads", runtime = _minio}]

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    buckets = _buckets
    workloads = [
        fw.Workload {name = "web", image = _img, env = forge.bucket_env("UPLOADS", "uploads"), runtime = forge.OnHosted {}}
        forge.minio_workload(_minio, _buckets)
    ]
})
