"""
Positive test — forge.Bucket and forge.BucketRef.

A BucketRef lowers by where the bucket lives and where the referrer runs:

  1. a bucket on the env's MinIO is a literal: the endpoint through the
     cluster's host gateway for a pod, localhost for a host process, the
     MinIO compose service for a compose container, and
     localhost as the PUBLIC endpoint either way (the browser is on this
     machine), path-style, with the dev MinIO's credentials;
  2. a bucket on forge.BucketOnS3 carries its declared values, and its two
     credentials lower through their own channels: a ManagedSecret stays one
     on a hosted workload, a SecretRef is a secretRef on a pod;
  3. forge.bucket_env emits the seven <PREFIX>_<KEY> names pkg/blobstore's
     S3Config is filled from;
  4. forge.minio_workload hands docker-compose's `minio` the port, the
     buckets to create, the public ones and the CORS origins.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_minio = forge.BucketOnMinIO {port = 9123}
_r2 = forge.BucketOnS3 {
    endpoint = "https://acct.r2.cloudflarestorage.com"
    region = "auto"
    bucket = "acme-prod-media"
    access_key_id = forge.ManagedSecret {name = "MEDIA_ACCESS_KEY_ID"}
    secret_access_key = forge.ManagedSecret {name = "MEDIA_SECRET_ACCESS_KEY"}
}
_own = forge.BucketOnS3 {
    endpoint = "http://minio.storage.svc:9000"
    public_endpoint = "https://files.acme.test"
    path_style = True
    access_key_id = forge.SecretRef {name = "archive-s3", key = "access_key_id"}
    secret_access_key = forge.SecretRef {name = "archive-s3", key = "secret_access_key"}
}
_buckets = [
    forge.Bucket {name = "uploads", runtime = _minio, cors_origins = ["http://localhost:3000"]}
    forge.Bucket {name = "avatars", runtime = _minio, public_read = True, cors_origins = ["http://localhost:3000", "http://localhost:3001"]}
    forge.Bucket {name = "media", runtime = _r2}
    forge.Bucket {name = "archive", runtime = _own}
]

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    buckets = _buckets
    workloads = [
        fw.Workload {
            name = "api"
            command = ["./bin/api"]
            env = forge.bucket_env("UPLOADS", "uploads") | {MEDIA_BUCKET = forge.BucketRef {name = "media"}}
            runtime = forge.OnHost {}
        }
        fw.Workload {
            name = "thumbnailer"
            kind = "worker"
            image = _img
            env = forge.bucket_env("UPLOADS", "uploads") | forge.bucket_env("ARCHIVE", "archive")
            runtime = forge.OnCluster {target = _k3d}
        }
        fw.Workload {
            name = "web"
            image = _img
            env = forge.bucket_env("MEDIA", "media")
            runtime = forge.OnHosted {}
        }
        fw.Workload {
            name = "indexer"
            kind = "worker"
            env = forge.bucket_env("UPLOADS", "uploads")
            runtime = forge.OnCompose {}
        }
        forge.minio_workload(_minio, _buckets)
    ]
})

_w = {w.name: w for w in output.workloads}
_env = lambda name: str -> {str:} {
    {e.name: e for e in _w[name].spec.env}
}
_host = _env("api")
_pod = _env("thumbnailer")
_hosted = _env("web")
_compose = _w["indexer"].runtime.env

# 3. The seven names, in S3Config's order.
assert_bucket_env_names = [e.name for e in _w["api"].spec.env][:7] == ["UPLOADS_ENDPOINT", "UPLOADS_PUBLIC_ENDPOINT", "UPLOADS_REGION", "UPLOADS_BUCKET", "UPLOADS_ACCESS_KEY_ID", "UPLOADS_SECRET_ACCESS_KEY", "UPLOADS_PATH_STYLE"]

# 1. MinIO: a literal per referrer.
assert_minio_host_endpoint = _host["UPLOADS_ENDPOINT"].value == "http://localhost:9123"
assert_minio_pod_endpoint = _pod["UPLOADS_ENDPOINT"].value == "http://host.k3d.internal:9123"
# A compose container's localhost is itself: it dials the MinIO service on
# the compose network, at the container's API port.
assert_minio_compose_endpoint = _compose.UPLOADS_ENDPOINT == "http://minio:9000" and _compose.UPLOADS_PUBLIC_ENDPOINT == "http://localhost:9123"
assert_minio_public_endpoint = _host["UPLOADS_PUBLIC_ENDPOINT"].value == "http://localhost:9123" and _pod["UPLOADS_PUBLIC_ENDPOINT"].value == "http://localhost:9123"
assert_minio_bucket = _host["UPLOADS_BUCKET"].value == "uploads" and _host["UPLOADS_REGION"].value == "us-east-1"
assert_minio_credentials = _pod["UPLOADS_ACCESS_KEY_ID"].value == "minioadmin" and _pod["UPLOADS_SECRET_ACCESS_KEY"].value == "minioadmin"
assert_minio_path_style = _host["UPLOADS_PATH_STYLE"].value == "true"
# `key` defaults to the bucket's name.
assert_default_key = _host["MEDIA_BUCKET"].value == "acme-prod-media"

# 2. S3: declared values; credentials through their own channels.
assert_s3_hosted_endpoint = _hosted["MEDIA_ENDPOINT"].value == "https://acct.r2.cloudflarestorage.com" and _hosted["MEDIA_PUBLIC_ENDPOINT"].value == "https://acct.r2.cloudflarestorage.com"
assert_s3_hosted_region = _hosted["MEDIA_REGION"].value == "auto" and _hosted["MEDIA_PATH_STYLE"].value == "false"
assert_s3_hosted_managed_secret = _hosted["MEDIA_ACCESS_KEY_ID"].managedSecret == {name = "MEDIA_ACCESS_KEY_ID"} and _hosted["MEDIA_SECRET_ACCESS_KEY"].managedSecret == {name = "MEDIA_SECRET_ACCESS_KEY"}
assert_s3_pod_public_endpoint = _pod["ARCHIVE_PUBLIC_ENDPOINT"].value == "https://files.acme.test" and _pod["ARCHIVE_ENDPOINT"].value == "http://minio.storage.svc:9000"
assert_s3_bucket_defaults_to_name = _pod["ARCHIVE_BUCKET"].value == "archive" and _pod["ARCHIVE_PATH_STYLE"].value == "true"
assert_s3_pod_secret_ref = _pod["ARCHIVE_SECRET_ACCESS_KEY"].secretRef == {name = "archive-s3", key = "secret_access_key"}

# 4. The compose MinIO.
_mc = _w["minio"].runtime
assert_minio_compose = _mc.type == "compose" and _mc.service == "minio"
assert_minio_compose_env = _mc.env.MINIO_PORT == "9123" and _mc.env.MINIO_BUCKETS == "uploads avatars" and _mc.env.MINIO_PUBLIC_BUCKETS == "avatars"
assert_minio_cors = _mc.env.MINIO_API_CORS_ALLOW_ORIGIN == "http://localhost:3000,http://localhost:3001"
assert_minio_root = _mc.env.MINIO_ROOT_USER == "minioadmin" and _mc.env.MINIO_CONSOLE_PORT == "0"
