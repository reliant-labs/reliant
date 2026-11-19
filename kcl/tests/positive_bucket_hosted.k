"""
Positive test — a forge.Bucket on forge.OnHosted.

  1. it renders as an env-shape `buckets` entry: the ManagedBucket spec the
     hosted provider publishes (deletionPolicy retain by default, the
     declared CORS origins, publicRead only when set);
  2. a hosted referrer KEEPS the BucketRef: every one of forge.bucket_env's
     seven keys rides the wire as bucketRef {name, key}, which the renderer
     turns into a secretKeyRef on the Secret the platform writes;
  3. a bucket forge does not manage (MinIO, BucketOnS3) is not an entry.
"""

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_r2 = forge.BucketOnS3 {
    endpoint = "https://acct.r2.cloudflarestorage.com"
    access_key_id = forge.ManagedSecret {name = "MEDIA_ACCESS_KEY_ID"}
    secret_access_key = forge.ManagedSecret {name = "MEDIA_SECRET_ACCESS_KEY"}
}

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    buckets = [
        forge.Bucket {name = "uploads", runtime = forge.OnHosted {}, cors_origins = ["https://partner.example.com"]}
        forge.Bucket {name = "avatars", runtime = forge.OnHosted {}, public_read = True, deletion_policy = "delete"}
        forge.Bucket {name = "media", runtime = _r2}
    ]
    workloads = [fw.Workload {
        name = "web"
        image = _img
        env = forge.bucket_env("UPLOADS", "uploads") | {AVATARS_BUCKET = forge.BucketRef {name = "avatars"}, MEDIA_BUCKET = forge.BucketRef {name = "media"}}
        runtime = forge.OnHosted {}
    }]
})

_b = {b.name: b for b in output.buckets}
_env = {e.name: e for e in output.workloads[0].spec.env}

# 1. The env-shape entries.
assert_bucket_names = [b.name for b in output.buckets] == ["uploads", "avatars"]
assert_bucket_runtime = _b.uploads.runtime == "hosted"
assert_bucket_spec_default = _b.uploads.spec == {"corsOrigins": ["https://partner.example.com"], "deletionPolicy": "retain"}
assert_bucket_spec_public = _b.avatars.spec == {"publicRead": True, "deletionPolicy": "delete"}

# 2. A hosted referrer keeps the reference, one key per variable.
assert_bucket_ref_kept = all k in forge.BUCKET_REF_KEYS { _env["UPLOADS_${k.upper()}"].bucketRef == {name = "uploads", key = k} }
assert_bucket_ref_no_literal = all k in forge.BUCKET_REF_KEYS { not _env["UPLOADS_${k.upper()}"].value }
assert_bucket_ref_default_key = _env.AVATARS_BUCKET.bucketRef == {name = "avatars", key = "bucket"}

# 3. A bucket forge does not manage still lowers to its declared values.
assert_s3_unchanged = _env.MEDIA_BUCKET.value == "media" and not _env.MEDIA_BUCKET.bucketRef
