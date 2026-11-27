"""
Negative test — forge.minio_workload runs the compose MinIO, so handing it a host
MinIO's BucketOnMinIO (infra set) is refused rather than silently starting a
second server.
"""

# expect: forge.minio_workload binds the compose MinIO

import forge
import forge.workloads as fw

_host = forge.BucketOnMinIO {infra = "minio"}
_buckets = [forge.Bucket {name = "uploads", runtime = _host}]

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = _buckets
    infra = [forge.HostInfra {name = "minio", engine = "minio", port = 9200}]
    workloads = [forge.minio_workload(_host, _buckets)]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
