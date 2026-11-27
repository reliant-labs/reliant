"""
Negative test — a host MinIO NAMED `minio` and the compose MinIO
(forge.minio_workload) in one env. The compose MinIO reads its root user from
MINIO_ROOT_USER, the same store key that host MinIO is generated under, so
the pair is refused. (Another name, e.g. `blobs`, is accepted: see
positive_bucket_host_minio.k.)
"""

# expect: their root-credential keys collide

import forge
import forge.workloads as fw

_compose = forge.BucketOnMinIO {port = 9123, service = "s3"}
_buckets = [
    forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {infra = "minio"}}
    forge.Bucket {name = "legacy", runtime = _compose}
]

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = _buckets
    infra = [forge.HostInfra {name = "minio", engine = "minio", port = 9200}]
    workloads = [forge.minio_workload(_compose, _buckets)]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
