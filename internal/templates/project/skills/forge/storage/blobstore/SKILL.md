---
name: storage/blobstore
description: File uploads and downloads (images, documents, attachments) — declare a forge.Bucket per env, hand a workload its connection with forge.bucket_env, presign an upload in a handler with forge/pkg/blobstore, PUT it straight from the browser, confirm it with Stat. Dev runs MinIO; Reliant hosting provisions the bucket (forge.OnHosted); anywhere else use any S3-compatible bucket (S3, R2, GCS).
---

# File uploads: forge.Bucket + forge/pkg/blobstore

The bytes of an upload never pass through your API. The browser asks the API
for a **presigned URL**, PUTs the file straight to object storage with it, and
tells the API it is done; the API **confirms what landed** and records it.
Downloads are presigned GET URLs the same way. One protocol everywhere: the
S3 API, which MinIO speaks in dev and AWS S3, Cloudflare R2 and Google Cloud
Storage (through its S3 interoperability, with an HMAC key) speak in prod.

```
browser ──CreateUpload(name, type, size)──▶ API ──PresignPut──▶ {PUT url, headers}
browser ──PUT bytes (exact size + type)──▶ bucket
browser ──CompleteUpload(id)──▶ API ──Stat──▶ size/type match? record the row
browser ──GET presigned url──▶ bucket          (API: PresignGet)
```

## 1. Declare the bucket, per env

Buckets are a `Bundle.buckets` entry, like a `forge.ManagedDatabase`: the
**env** says where each one lives. The scaffolded `deploy/kcl/dev/main.k`
carries the dev half switched off; turn it on and name the bucket:

```kcl
_object_storage = True
_minio = forge.BucketOnMinIO {
    port = plugin.resolve_port("acme-dev-minio", 9000)
} if _object_storage else None
_buckets: [forge.Bucket] = [forge.Bucket {name = "uploads", runtime = _minio}]
```

`forge env up dev` then starts docker-compose.yml's `minio` service (the
`forge.minio_workload` the template adds), creates every declared bucket, and
reports it healthy only once they exist. The data persists in the
`minio_data` volume. A project scaffolded before this existed has no `minio`
service: copy it, and its `minio_data` volume, from a fresh
`forge project new` scaffold's docker-compose.yml.

`forge.Bucket` fields:

| field | meaning |
|---|---|
| `name` | how `forge.BucketRef` names it, and the real bucket name unless the runtime says otherwise (3..63 of `a-z0-9-`) |
| `public_read` | every object readable without a signature (avatars, a public gallery). Off for anything private: hand out `PresignGet` URLs |
| `cors_origins` | the browser origins that upload directly. The dev MinIO applies them (none declared = any origin). On `forge.OnHosted` the platform adds the env's own hosted frontends and custom domains, so list only OTHER origins. On `forge.BucketOnS3` forge does not manage the bucket: set its CORS where it lives, allowing `PUT` and `GET` and the `Content-Type` header |
| `deletion_policy` | `"retain"` (default) or `"delete"`: what removing the declaration does to a bucket forge manages (`forge.OnHosted`). Retain leaves the bucket and every object; delete is irreversible and only happens because someone typed it |
| `runtime` | where it lives — required, no default |

Runtimes:

- **`forge.BucketOnMinIO {port}`**: the env's MinIO (dev, e2e). Credentials
  are the dev MinIO's own (`minioadmin`), committed on purpose like the dev
  postgres password: it binds loopback.
- **`forge.BucketOnS3 {...}`**: a bucket you operate or buy. forge renders
  the connection and never creates, configures or deletes the bucket.

  ```kcl
  forge.Bucket {name = "uploads", runtime = forge.BucketOnS3 {
      endpoint = "https://<account>.r2.cloudflarestorage.com"   # S3: https://s3.<region>.amazonaws.com
      region = "auto"                                            # R2 "auto"; GCS "us-east-1" works
      bucket = "acme-prod-uploads"                               # the real name; default: forge.Bucket.name
      access_key_id = forge.ManagedSecret {name = "UPLOADS_ACCESS_KEY_ID"}
      secret_access_key = forge.ManagedSecret {name = "UPLOADS_SECRET_ACCESS_KEY"}
  }}
  ```

  The two credentials are references, never literals: a `forge.ManagedSecret`
  (the env's secret store, `forge secret set --env prod UPLOADS_SECRET_ACCESS_KEY`
  with the value on stdin; what a hosted workload reads) or a `forge.SecretRef`
  (a Kubernetes Secret on a cluster you operate). GCS: endpoint
  `https://storage.googleapis.com` and an HMAC key of a service account that
  holds only `roles/storage.objectAdmin` on that one bucket.
- **`forge.OnHosted {}`**: Reliant hosting provisions it. See "On Reliant
  hosting" below.

### On Reliant hosting

```kcl
# deploy/kcl/prod/main.k
_buckets: [forge.Bucket] = [forge.Bucket {name = "uploads", runtime = forge.OnHosted {}}]
```

The env publishes the bucket to the control plane as a `forge.dev/v1alpha1
ManagedBucket`, next to its Workloads, and the platform:

- creates one cloud bucket for this env (Google Cloud Storage; its real name
  is the platform's, `UPLOADS_BUCKET` carries it), with uniform access and
  public access prevented unless `public_read`;
- creates a service account that can read, write, list and delete objects in
  **that bucket only**, and an HMAC key for it;
- writes the Secret `forge-bucket-uploads` in the env's namespace, one key per
  `BucketRef.key`: `endpoint` and `public_endpoint` are
  `https://storage.googleapis.com`, `region` is `auto`, `path_style` is
  `true`, and the two credentials are the HMAC key;
- sets the bucket's CORS (`PUT`, `GET`, `HEAD`; `Content-Type`) to your
  `cors_origins` plus the env's hosted frontend origins and bound custom
  domains.

A hosted workload's `forge.bucket_env("UPLOADS", "uploads")` then lowers to
`bucketRef` env values, which become `secretKeyRef`s on that Secret. The pod
does not start until the Secret exists, so a deploy that reports the workload
ready also proved the bucket. Nothing changes in your code: the same seven
variables fill `blobstore.S3Config`. The browser talks to GCS directly, so
uploads never touch the cluster.

What is refused, at render:

- a workload on a cluster or host reading a hosted bucket (its credential
  exists only in the control plane's namespace): bind the bucket to
  `forge.BucketOnS3` in that env, or run the workload on `forge.OnHosted`;
- a hosted bucket name over 40 characters (the platform prefixes it);
- a hosted bucket in a Bundle with no `control_plane`.

Removing the declaration leaves the bucket and its objects in place unless
`deletion_policy = "delete"`.

#### Verify a hosted bucket

`forge env deploy` publishes the bucket but cannot observe it: the control
plane reports no status for a ManagedBucket, so the deploy lists it as
`not_waited` (in `--json` too), never as ready. A workload that reads it
turning ready proves only that the credential Secret exists. A bucket no
workload reads (a bucket-only env) is proved by nothing. After the first
deploy of a hosted bucket, and after any change to its `public_read` or
`cors_origins`, check it from the deployed app, through the same handlers your
users hit:

1. **Upload**: from a page served on one of the bucket's origins, presign a
   PUT (section 4) and send it from the browser (section 5). A CORS error in
   the browser console means the origin is missing from `cors_origins` or the
   platform has not applied it yet.
2. **Confirm and download**: `Stat` the key (section 6), then fetch a
   presigned GET and compare the bytes.
3. **Private stays private**: fetch the object's plain URL,
   `https://storage.googleapis.com/<UPLOADS_BUCKET>/<key>`, with no signature.
   It must be refused (403) unless the bucket is `public_read`, and must
   succeed when it is.

For a bucket-only env, run the check once a workload that reads the bucket is
deployed; until then nothing has used the credential.

**Recovering** from a bad bucket change rolls forward, as every `forge env deploy`
does (`deploy/release`, "Verify + recover"): restore the previous declaration (or
drop the bucket's referrers) in the tree and `forge env deploy <env>` again;
binding the env to an older release is the same promote. Either keeps the
bucket, its objects and its credential: a bucket is never release-bound, and
the default `deletion_policy = "retain"` leaves it in place even when the
declaration is removed. Never set `deletion_policy = "delete"` to
undo a change; it destroys every object. Do not delete the `forge-bucket-<name>`
Secret to "reset" a credential: the platform owns it, and the workloads that
read it stop starting until it is rewritten.

Every env a workload runs in must declare each bucket the workload references;
the render names a missing one.

## 2. Hand the workload its connection

In `deploy/kcl/workloads.k`, on the workload that serves the upload RPCs:

```kcl
env = forge.bucket_env("UPLOADS", "uploads")
```

That is seven `forge.BucketRef`s: `UPLOADS_ENDPOINT`, `UPLOADS_PUBLIC_ENDPOINT`,
`UPLOADS_REGION`, `UPLOADS_BUCKET`, `UPLOADS_ACCESS_KEY_ID`,
`UPLOADS_SECRET_ACCESS_KEY`, `UPLOADS_PATH_STYLE`, which are the fields of
`blobstore.S3Config`. Each lowers per env and per runtime. For a dev MinIO
bucket they are literals, and the endpoint is `localhost` for a host process,
the cluster's host gateway for a k3d pod, and `http://minio:9000` (the compose
service, on the compose network) for a container in the MinIO's own compose
file, whose compose process env carries them for the file to pass through
(`UPLOADS_ENDPOINT: ${UPLOADS_ENDPOINT}`). `PUBLIC_ENDPOINT` stays
`localhost`, because the browser is on this machine and the presigned URL has
to be signed for the host the browser actually dials. For a `BucketOnS3`
bucket you get the declared values, and the credentials go through their own
secret channels. One fact on its own is `forge.BucketRef {name = "uploads",
key = "bucket"}`.

Read them through the typed config, so nothing calls `os.Getenv`: a block in
`proto/<project>/config/v1/config.proto`, composed on `AppConfig`, then
`forge generate`:

```protobuf
message UploadsConfig {
  string endpoint = 1 [(forge.v1.config) = {env_var: "UPLOADS_ENDPOINT"}];
  string public_endpoint = 2 [(forge.v1.config) = {env_var: "UPLOADS_PUBLIC_ENDPOINT"}];
  string region = 3 [(forge.v1.config) = {env_var: "UPLOADS_REGION"}];
  string bucket = 4 [(forge.v1.config) = {env_var: "UPLOADS_BUCKET"}];
  string access_key_id = 5 [(forge.v1.config) = {env_var: "UPLOADS_ACCESS_KEY_ID"}];
  string secret_access_key = 6 [(forge.v1.config) = {env_var: "UPLOADS_SECRET_ACCESS_KEY", sensitive: true}];
  bool path_style = 7 [(forge.v1.config) = {env_var: "UPLOADS_PATH_STYLE"}];
}
```

## 3. Build the store once, in the composition root

`internal/app/compose.go` builds the store from that config block and passes
it to the service as an interface:

```go
uploads, err := blobstore.NewS3(blobstore.S3Config{
    Endpoint:        cfg.GetUploads().GetEndpoint(),
    PublicEndpoint:  cfg.GetUploads().GetPublicEndpoint(),
    Region:          cfg.GetUploads().GetRegion(),
    Bucket:          cfg.GetUploads().GetBucket(),
    AccessKeyID:     cfg.GetUploads().GetAccessKeyId(),
    SecretAccessKey: cfg.GetUploads().GetSecretAccessKey(),
    PathStyle:       cfg.GetUploads().GetPathStyle(),
})
```

`NewS3` makes no network call. A wrong key or a missing bucket shows up on the
first `Stat`, `List` or `Delete`, or as a refused upload. Every call gets a
span, the `blobstore.{calls,errors,duration}` metrics and a log record, and
every request to the storage service is a traced client span. A missing object
(`ErrNotFound`) is logged as an expected outcome, not as an error.

The consumer depends on the 5-method interface `blobstore.Store`, or on a
narrower one it declares itself, and never on `*blobstore.S3`.

## 4. Presign the upload in the handler

**Keys are yours, and they are tenant-scoped.** Generate the key on the
server, never from the user's file name: `<tenant>/<kind>/<uuid>`. Wrap the
store in `blobstore.Scoped(store, tenantID)` per request, so the handler
cannot name another tenant's object whatever key it is handed: keys with a
leading `/`, empty segments, `.` or `..` are refused (`ErrInvalidKey`).

```go
func (s *service) CreateUpload(ctx context.Context, in CreateUploadInput) (CreateUploadResult, error) {
    if !allowedTypes[in.ContentType] {
        return CreateUploadResult{}, svcerr.InvalidArgument("unsupported file type")
    }
    store, err := blobstore.Scoped(s.deps.Uploads, in.TenantID)
    if err != nil {
        return CreateUploadResult{}, err
    }
    key := "attachments/" + uuid.NewString()
    req, err := store.PresignPut(ctx, key, blobstore.PutOptions{
        ContentType:   in.ContentType,
        ContentLength: in.Size,     // exact size; signed
        MaxBytes:      25 << 20,    // your limit; ErrTooLarge above it
        TTL:           10 * time.Minute,
    })
    if err != nil {
        return CreateUploadResult{}, err // svcerr-classified: InvalidArgument for a bad request
    }
    // Record a pending row (tenant, key, declared type and size) before you return.
    return CreateUploadResult{Key: key, URL: req.URL, Method: req.Method, Headers: req.Headers}, nil
}
```

**Why the client states the size up front.** The presigned PUT signs the
exact `Content-Length` and the `Content-Type`, so the storage service rejects
any body of a different size or type with a 403. That bounds an upload on
every S3-compatible service. A presigned POST with `content-length-range`
could bound a maximum instead, but Cloudflare R2 doesn't implement it.

## 5. Upload from the browser

```ts
const { key, url, method, headers } = await client.createUpload({
  name: file.name,
  contentType: file.type,
  size: BigInt(file.size),
});
const res = await fetch(url, { method, headers, body: file }); // the browser sets Content-Length from the body
if (!res.ok) throw new Error(`upload failed: ${res.status}`);
await client.completeUpload({ key });
```

Send the `headers` exactly as you got them, and the same `File` whose size you
declared. A 403 means the size, the type or the signature did not match: an
edited file, a wrong `Content-Type`, or an expired URL.

## 6. Confirm, then serve

```go
obj, err := store.Stat(ctx, key)
switch {
case errors.Is(err, blobstore.ErrNotFound):
    return svcerr.FailedPrecondition("the upload has not arrived")
case err != nil:
    return err
case obj.Size != pending.Size || obj.ContentType != pending.ContentType:
    _ = store.Delete(ctx, key)
    return svcerr.InvalidArgument("the upload does not match what was declared")
}
// mark the row complete
```

Downloads: `store.PresignGet(ctx, key, blobstore.GetOptions{TTL: 5 * time.Minute, DownloadName: row.FileName})`.
`DownloadName` makes it an attachment saved under that name. `List(ctx,
prefix, ListOptions{After, Limit})` pages a prefix in key order (1000 max per
page), and `Delete` is idempotent. Clean up pending rows whose upload never
completed with a worker: list or delete their keys.

## 7. Test without a bucket

`blobstore.NewMemory()` is a `Store` with the S3 backend's key and option
validation. Its presigned URLs are `memory://` URLs nothing fetches. Play the
browser with `mem.Put(key, data, contentType)` and assert with `Stat`, `List`
or `mem.Get`. forge's own S3 backend is tested against a real MinIO; your
service tests need only the fake.

## Rules

- The API never proxies upload bytes. Presign and confirm.
- Keys are server-generated and tenant-scoped (`Scoped`). A user's file name
  goes in your row, and in `DownloadName`, never in the key.
- Always `Stat` before you trust an upload, and compare it with what you
  presigned.
- Private by default: `public_read` only for content that is public on
  purpose.
- Credentials are references (`ManagedSecret` / `SecretRef`), never literals,
  outside the dev MinIO.
