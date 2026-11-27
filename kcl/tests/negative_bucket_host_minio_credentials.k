"""
Negative test — a host MinIO's root credentials are generated, never committed, so
the BucketOnMinIO may not state them.
"""

# expect: takes no access_key or secret_key

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = [forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {infra = "minio", secret_key = "committed-password"}}]
    infra = [forge.HostInfra {name = "minio", engine = "minio", port = 9200}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
