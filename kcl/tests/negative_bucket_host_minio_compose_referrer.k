"""
Negative test — a host MinIO binds 127.0.0.1 of this machine, so a compose container,
whose localhost is itself, cannot reach its bucket.
"""

# expect: so a workload on the compose runtime cannot reach it

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = [forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {infra = "minio"}}]
    infra = [forge.HostInfra {name = "minio", engine = "minio", port = 9200}]
    workloads = [fw.Workload {name = "indexer", kind = "worker", env = forge.bucket_env("UPLOADS", "uploads"), runtime = forge.OnCompose {}}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
