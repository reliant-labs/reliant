"""
Positive test — a bucket on a HOST MinIO (forge.BucketOnMinIO {infra}), the
container-free dev shape:

  1. a host-process referrer's BucketRef lowers to localhost on the
     HostInfra's port, its region, path-style, and the two credentials as
     forge.ManagedSecret references to the env's secret store
     (<INFRA>_ROOT_USER / <INFRA>_ROOT_PASSWORD), never literals;
  2. the rendered infra entry carries the MinIO's buckets (only those on it),
     their public_read, the union of their CORS origins, the console port,
     the region, and the store KEYS of its root credentials;
  3. the rendered FileSecrets `generate`s those keys (hex), merged into the
     declared provider: an author's own spec for one of them wins, and every
     other declared entry and field renders as declared;
  4. a compose MinIO beside it is accepted, because a host MinIO named
     `blobs` has keys of its own (BLOBS_ROOT_*, not the compose MinIO's
     MINIO_ROOT_*): the compose one keeps its own buckets and literals, and
     the host MinIO's buckets are not handed to it.
"""

import forge
import forge.workloads as fw

_host = forge.BucketOnMinIO {infra = "blobs"}
_compose = forge.BucketOnMinIO {port = 9123}
_buckets = [
    forge.Bucket {name = "gtm-assets", runtime = _host, cors_origins = ["http://localhost:5173"]}
    forge.Bucket {name = "avatars", runtime = _host, public_read = True, cors_origins = ["http://localhost:3000", "http://localhost:5173"]}
    forge.Bucket {name = "legacy", runtime = _compose}
]

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    buckets = _buckets
    infra = [
        forge.HostInfra {name = "postgres", port = 5432}
        forge.HostInfra {name = "blobs", engine = "minio", port = 9200, minio_console_port = 9201}
    ]
    workloads = [
        fw.Workload {
            name = "api"
            command = ["./bin/api"]
            env = forge.bucket_env("GTM_ASSETS", "gtm-assets")
            runtime = forge.OnHost {}
        }
        forge.minio_workload(_compose, _buckets)
    ]
    secret_provider = forge.FileSecrets {
        path = "secrets/dev.yaml"
        generate = {
            "AUTH_API_KEY" = forge.GeneratedSecret {bytes = 24, encoding = "hex"}
            "BLOBS_ROOT_USER" = forge.GeneratedSecret {bytes = 12, encoding = "hex", prefix = "dev"}
        }
    }
})

_env = {e.name: e for e in [w for w in output.workloads if w.name == "api"][0].spec.env}
_infra = {i.name: i for i in output.infra}
_blobs = _infra["blobs"]
_gen = output.secret_provider.generate

# 1. The host referrer.
assert_endpoint = _env["GTM_ASSETS_ENDPOINT"].value == "http://localhost:9200" and _env["GTM_ASSETS_PUBLIC_ENDPOINT"].value == "http://localhost:9200"
assert_bucket = _env["GTM_ASSETS_BUCKET"].value == "gtm-assets" and _env["GTM_ASSETS_REGION"].value == "us-east-1" and _env["GTM_ASSETS_PATH_STYLE"].value == "true"
assert_credentials_are_store_refs = _env["GTM_ASSETS_ACCESS_KEY_ID"].managedSecret == {name = "BLOBS_ROOT_USER"} and _env["GTM_ASSETS_SECRET_ACCESS_KEY"].managedSecret == {name = "BLOBS_ROOT_PASSWORD"}
assert_no_literal_credential = not _env["GTM_ASSETS_ACCESS_KEY_ID"].value and not _env["GTM_ASSETS_SECRET_ACCESS_KEY"].value

# 2. The infra entry.
assert_infra_engine = _blobs.engine == "minio" and _blobs.port == 9200 and _blobs.minio_console_port == 9201 and _blobs.minio_region == "us-east-1"
assert_infra_buckets = _blobs.minio_buckets == [{name = "gtm-assets", public_read = False}, {name = "avatars", public_read = True}]
assert_infra_cors = _blobs.minio_cors_allow_origin == "http://localhost:3000,http://localhost:5173"
assert_infra_secret_keys = _blobs.minio_root_user_secret == "BLOBS_ROOT_USER" and _blobs.minio_root_password_secret == "BLOBS_ROOT_PASSWORD"
# Every other engine renders exactly as before: no minio keys.
assert_postgres_unchanged = "minio_buckets" not in _infra["postgres"] and "minio_root_user_secret" not in _infra["postgres"]

# 3. The generated credentials, merged into the declared provider.
assert_generates_password = _gen["BLOBS_ROOT_PASSWORD"] == {bytes = 32, encoding = "hex"}
# The author's BLOBS_ROOT_USER spec is the one rendered, whole: forge's
# 16-byte default neither replaces nor merges into it.
assert_author_spec_wins = _gen["BLOBS_ROOT_USER"].bytes == 12 and _gen["BLOBS_ROOT_USER"].prefix == "dev" and _gen["BLOBS_ROOT_USER"].encoding == "hex"
assert_keeps_author_generate = _gen["AUTH_API_KEY"].bytes == 24 and len(_gen) == 3
assert_keeps_provider_fields = output.secret_provider.type == "file" and output.secret_provider.path == "secrets/dev.yaml"

# 4. The compose MinIO beside it.
_mc = [w for w in output.workloads if w.name == "minio"][0].runtime
assert_compose_keeps_its_own = _mc.env.MINIO_BUCKETS == "legacy" and _mc.env.MINIO_PUBLIC_BUCKETS == ""
assert_compose_keeps_literals = _mc.env.MINIO_ROOT_USER == "minioadmin" and _mc.env.MINIO_ROOT_PASSWORD == "minioadmin" and _mc.env.MINIO_PORT == "9123"
