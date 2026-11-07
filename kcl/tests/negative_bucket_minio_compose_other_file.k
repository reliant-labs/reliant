"""
Negative test — a compose container reaches the env's MinIO by its compose
service, so only from the MinIO's own compose project: a referrer in another
compose file has no network to it.
"""

# expect: which a compose container reaches only from the same compose project

import forge
import forge.workloads as fw

_minio = forge.BucketOnMinIO {port = 9123}
_buckets = [forge.Bucket {name = "uploads", runtime = _minio}]

output = forge.render(forge.Bundle {
    project = "acme"
    buckets = _buckets
    workloads = [
        fw.Workload {name = "indexer", kind = "worker", env = forge.bucket_env("UPLOADS", "uploads"), runtime = forge.OnCompose {file = "docker-compose.indexer.yml"}}
        forge.minio_workload(_minio, _buckets)
    ]
})
