---
name: storage/blobstore
description: File uploads and downloads (images, documents, attachments) — declare a forge.Bucket per env, hand a workload its connection with forge.bucket_env, presign an upload in a handler with forge/pkg/blobstore, PUT it straight from the browser, confirm it with Stat. Dev runs MinIO (a host process, or a compose service); Reliant hosting provisions the bucket (forge.OnHosted); anywhere else use any S3-compatible bucket (S3, R2, GCS).
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

### No docker: MinIO as a host process

A dev env that runs its infra as host processes (`forge.HostInfra`, no
container runtime) runs MinIO the same way. Declare the server in
`Bundle.infra` and point the buckets at it by name:

```kcl
_buckets: [forge.Bucket] = [
    forge.Bucket {name = "uploads", runtime = forge.BucketOnMinIO {infra = "minio"}}
]
_bundle = forge.Bundle {
    ...
    buckets = _buckets
    infra = [
        forge.HostInfra {name = "postgres", port = _postgres_port}
        forge.HostInfra {
            name = "minio"
            engine = "minio"
            port = plugin.resolve_port("acme-dev-minio", 9000)
            # minio_console_port = plugin.resolve_port("acme-dev-minio-console", 9001)
        }
    ]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
}
```

`forge env up dev` fetches a pinned, checksum-verified MinIO binary once
(cached under the user cache dir, ~110 MB), starts it on 127.0.0.1 with its
objects in the HostInfra's data directory (`data_dir`, gitignored, kept across
runs like the dev postgres's), creates every bucket that names
it, reconciles each one's anonymous access (download for `public_read`, none
otherwise) and applies the buckets' `cors_origins` (any origin when none is
declared). `forge env down dev` stops it; the objects stay.

The root credentials are never in KCL. forge generates them into the env's
secret file on the first `forge env up` (`MINIO_ROOT_USER` and
`MINIO_ROOT_PASSWORD`, named for the HostInfra: `blobs` would be
`BLOBS_ROOT_USER`), and the workload's `forge.bucket_env` reads them from the
store as `forge.ManagedSecret`s. So the env must use `forge.FileSecrets`.
`port`, `console_port`, `access_key` and `secret_key` stay unset on a
`BucketOnMinIO {infra}`: the HostInfra owns them.

It binds loopback, so only a `forge.OnHost` workload can read its buckets; a
pod or a compose container is refused at render (use the compose MinIO for
those). A running MinIO cannot change its CORS origins, region or console
port: change one and `forge env up` says so, with the restart that applies it
(`forge env down`, then `forge env up`). The same holds for rotating the root
credentials with `forge secret set`.

The pinned binary is MinIO RELEASE.2025-09-07T16-13-09Z, the last release
upstream published binaries for; later releases are source only. It predates
the RELEASE.2025-10-15T17-29-55Z fix for CVE-2025-62506 (a restricted service
account or STS session escaping its session policy), which cannot reach this
server: its only identity is the generated root user, it binds loopback, and
it holds dev files. The pin can never take a later fix, so a host MinIO is for
dev data only; anything shared or long-lived belongs on `forge.BucketOnS3` or
`forge.OnHosted`.

A host MinIO can sit beside the compose `forge.minio_workload` (the host one
serving host processes, the compose one pods and containers) unless the
HostInfra is named `minio`: its keys would then be `MINIO_ROOT_USER` /
`MINIO_ROOT_PASSWORD`, the names the compose MinIO reads too, so render
refuses that pair. Name it something else (`blobs` gives `BLOBS_ROOT_*`), or
pick one MinIO per env.

`forge.Bucket` fields:

| field | meaning |
|---|---|
| `name` | how `forge.BucketRef` names it, and the real bucket name unless the runtime says otherwise (3..63 of `a-z0-9-`) |
| `public_read` | every object readable without a signature (avatars, a public gallery). Off for anything private: hand out `PresignGet` URLs |
| `cors_origins` | the browser origins that upload directly. The dev MinIO applies them (none declared = any origin). On `forge.OnHosted` the platform adds the env's own hosted frontends and custom domains, so list only OTHER origins. On `forge.BucketOnS3` forge does not manage the bucket: set its CORS where it lives, allowing `PUT` and `GET` and the `Content-Type` header |
| `deletion_policy` | `"retain"` (default) or `"delete"`: what removing the declaration does to a bucket forge manages (`forge.OnHosted`). Retain leaves the bucket and every object; delete is irreversible and only happens because someone typed it |
| `runtime` | where it lives — required, no default |

Runtimes:

- **`forge.BucketOnMinIO {infra}`**: the env's MinIO as a host process (dev),
  a `forge.HostInfra {engine = "minio"}`. Credentials are generated into the
  env's secret store. See "No docker" above.
- **`forge.BucketOnMinIO {port}`**: the env's MinIO as a compose service (dev,
  e2e). Credentials are the dev MinIO's own (`minioadmin`), committed on
  purpose like the dev postgres password: it binds loopback.
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
`blobstore.S3Config`. Each lowers per env and per runtime. For a host MinIO
bucket (`BucketOnMinIO {infra}`) the endpoint is `localhost` on the
HostInfra's port and the two credentials are `managedSecret` references to the
generated store keys, which a host process is handed at launch. For a compose
MinIO bucket they are literals, and the endpoint is `localhost` for a host process,
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
narrower one it declares itself, and never on `*blobstore.S3`. The
server-side byte calls (section 6) are package functions over that same
`Store`: `blobstore.Open` and `blobstore.PutObject`.

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

The type the browser declared is only a claim. When it matters (anything you
will preview inline, anything you index by content), read the bytes on the
server: `blobstore.Open` streams the object, or one range of it.

```go
r, obj, err := blobstore.Open(ctx, store, key, blobstore.OpenOptions{Length: 512})
if err != nil {
    return err // ErrNotFound when nothing is there
}
head, err := io.ReadAll(r)
r.Close()
if err != nil {
    return err
}
sniffed := http.DetectContentType(head) // obj.Size is the whole object's size

// A content hash needs every byte: stream the whole object once.
r, _, err = blobstore.Open(ctx, store, key, blobstore.OpenOptions{})
if err != nil {
    return err
}
defer r.Close()
h := sha256.New()
if _, err := io.Copy(h, r); err != nil {
    return err
}
```

`OpenOptions{Offset, Length}`: zero values read everything; a range past the
end stops there, and an offset past the end is `ErrInvalidOptions`. The
stream outlives the `Open` call (its trace span covers only opening it), so
keep `ctx` live until you have read it, and don't give a custom `Chain` a
timeout middleware: it would cancel the stream once `Open` returns. Stat,
List and Open report no `Object.SHA256`: the services do not all keep one, so
hash it yourself as above.

Downloads: `store.PresignGet(ctx, key, blobstore.GetOptions{TTL: 5 * time.Minute, DownloadName: row.FileName})`.
`DownloadName` makes it an attachment saved under that name. `List(ctx,
prefix, ListOptions{After, Limit})` pages a prefix in key order (1000 max per
page), and `Delete` is idempotent. Clean up pending rows whose upload never
completed with a worker: list or delete their keys.

**Inline previews.** A plain `PresignGet` serves the object with the type it
was stored with, which the uploader chose. To show an image or a video in the
page, force the type you sniffed:

```go
if !blobstore.InlineSafe(sniffed) {
    // anything else is served as a download (PresignDownload), never inline
}
req, err := store.PresignGet(ctx, key, blobstore.GetOptions{
    TTL:                 5 * time.Minute,
    Inline:              true,      // Content-Disposition: inline
    ResponseContentType: sniffed,   // forced on the response, whatever was stored
})
```

Only `image/png`, `image/jpeg`, `image/gif`, `image/webp`, `video/mp4` and
`video/webm` are accepted (`blobstore.InlineSafe`); any other type, and
`Inline` without a type, is `ErrInvalidOptions`. So a stored HTML or SVG file
can never render on the bucket's origin through this URL. For "save this
file", use `blobstore.PresignDownload`, which always forces
`application/octet-stream` and an attachment.

**Writing from the server.** Bytes that have no browser behind them (a job
that copies an attachment it already holds, a generated image) go in with
`blobstore.PutObject`:

```go
obj, err := blobstore.PutObject(ctx, store, "generated/"+uuid.NewString(), body, blobstore.PutObjectOptions{
    ContentType:   "image/png",
    ContentLength: n,         // exact size when you know it; 0 reads to the end
    MaxBytes:      25 << 20,  // ErrTooLarge past it
})
// obj.SHA256 is the hex SHA-256 of what was stored, hashed as it streamed
```

The body is held to `ContentLength` and `MaxBytes` while it streams. One that
breaks either fails the call before the service commits anything, so no
partial object is ever stored and an object already at that key is left as it
was. A body past 16 MiB goes up as an S3 multipart upload; when it fails, the
upload is aborted on a best-effort basis, so its parts can be left behind
(invisible, but billed). On a `forge.BucketOnS3` bucket, add a lifecycle rule
that aborts incomplete multipart uploads after a day
(`AbortIncompleteMultipartUpload`).

## 7. Test without a bucket

`blobstore.NewMemory()` is a `Store` with the S3 backend's key and option
validation. Its presigned URLs are `memory://` URLs nothing fetches. Play the
browser with `mem.Put(key, data, contentType)` and assert with `Stat`, `List`
or `mem.Get`. `blobstore.Open` and `blobstore.PutObject` work on it as on the
real stores. forge's own S3 backend is tested against a real MinIO; your
service tests need only the fake.

## Rules

- The API never proxies a browser's upload. Presign and confirm. Read on the
  server (`Open`) to check what landed; write on the server (`PutObject`)
  only bytes the server itself holds.
- Preview inline only with a forced, sniffed type (`Inline` +
  `ResponseContentType`); download everything else with `PresignDownload`.
- Keys are server-generated and tenant-scoped (`Scoped`). A user's file name
  goes in your row, and in `DownloadName`, never in the key.
- Always `Stat` before you trust an upload, and compare it with what you
  presigned.
- Private by default: `public_read` only for content that is public on
  purpose.
- Credentials are references (`ManagedSecret` / `SecretRef`), never literals,
  outside the dev MinIO.
