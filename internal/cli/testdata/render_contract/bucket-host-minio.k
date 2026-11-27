"""
render contract — forge.Bucket on a HOST MinIO: a `forge.HostInfra {engine =
"minio"}` serving two buckets (one public_read, CORS origins on both) through
`forge.BucketOnMinIO {infra}`, read by a host process via forge.bucket_env.
The infra entry carries the buckets, the CORS origins, the console port, the
region and the secret-store KEYS of the root credentials; the workload reads
those keys as managedSecret; the FileSecrets provider `generate`s them.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

_build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
_minio = forge.BucketOnMinIO {infra = "minio"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    lifecycle = "local"
    buckets = [
        forge.Bucket {name = "uploads", runtime = _minio, cors_origins = ["http://localhost:5173"]}
        forge.Bucket {name = "avatars", runtime = _minio, public_read = True}
    ]
    workloads = [
        fw.Workload {
            name = "api"
            build = _build
            args = ["api"]
            env = forge.bucket_env("UPLOADS", "uploads")
            runtime = forge.OnHost {}
        }
    ]
    infra = [forge.HostInfra {name = "minio", engine = "minio", port = 9200, minio_console_port = 9201}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
