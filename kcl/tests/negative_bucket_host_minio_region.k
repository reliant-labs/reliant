"""
Negative test — a bucket's region must be its host MinIO's, the one clients sign
for.
"""

# expect: must be its host MinIO's

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = [forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {infra = "minio", region = "eu-west-1"}}]
    infra = [forge.HostInfra {name = "minio", engine = "minio", port = 9200}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
