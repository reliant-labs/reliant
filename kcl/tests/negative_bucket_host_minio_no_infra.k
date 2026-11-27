"""
Negative test — a BucketOnMinIO {infra} must name a forge.HostInfra with engine
"minio" that the env declares.
"""

# expect: names no forge.HostInfra with engine 'minio'

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = [forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {infra = "minio"}}]
    infra = [forge.HostInfra {name = "minio", port = 5432}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
