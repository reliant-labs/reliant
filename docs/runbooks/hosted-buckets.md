# Rolling out hosted buckets (`forge.Bucket` on `forge.OnHosted`)

forge's half of a hosted bucket is the declaration and the wire: a
`forge.Bucket` on `forge.OnHosted` renders to `output.buckets[]`, the hosted
provider publishes it as a `forge.dev/v1alpha1` ManagedBucket beside the env's
Workloads, and a hosted referrer's `forge.BucketRef` stays a `bucketRef` that
the platform lowers to a `secretKeyRef` on `forge-bucket-<name>`. The other
half, the control plane's ManagedBucket operator (cloud bucket, per-bucket
service account and HMAC key, the Secret, CORS, admission of the name), lives
in control-plane and ships separately. This runbook is the order the two ship
in, the check that proves the pair works, and how to back out.

## What forge can and cannot see

The control plane keeps no deployment row for a ManagedBucket, so
`forge env deploy` publishes it and reports it `not_waited` (text and
`--json`), never `ready`. A hosted workload that reads the bucket does not
start until its Secret exists, so that workload turning ready proves the
Secret was written, and nothing more: not the IAM scoping, not CORS, not
private access. A bucket-only env is proved by nothing at deploy time. The
check below is what proves it.

## Rollout order

1. **control-plane first.** The ManagedBucket CRD, its admission and the
   operator are deployed to prod, at a control-plane release whose operator
   reads the spec this forge release publishes
   (`pkg/deploy/v1alpha1/managedbucket_types.go`). Until then nothing
   reconciles a published ManagedBucket: no Secret is written, so a workload
   that reads the bucket never starts and its deploy does not report it
   ready. A bucket-only env can deploy with nothing behind it (its bucket
   reported `not_waited`), which is why step 2 comes before any user is told
   the feature exists.
2. **Prove it on a scratch env** (an org of our own, not a customer's) with
   the check below, for one private bucket and one `public_read` bucket, and
   once more for a bucket-only env followed by a deploy of a workload that
   reads it.
3. **Then the forge release** that carries this feature reaches users through
   the normal `rc-train` cut. The `storage/blobstore` skill is what tells an
   agent hosted buckets exist; it must not ship ahead of step 2.

## Post-deploy check

Against the deployed scratch app, through its own handlers (the same steps
are in the `storage/blobstore` skill, "Verify a hosted bucket", for users):

1. Presign a PUT and upload from a browser page on the env's hosted frontend
   origin. A CORS failure means the operator did not apply the env's origins.
2. `Stat` the key; fetch a presigned GET and compare the bytes.
3. Fetch `https://storage.googleapis.com/<UPLOADS_BUCKET>/<key>` with no
   signature: 403 for a private bucket, 200 for a `public_read` one.
4. With the scratch env's HMAC key (read from `forge-bucket-<name>` by an
   operator with access to the env's namespace), try to list a SECOND env's
   bucket: it must be refused. The key is scoped to its own bucket alone.
5. Flip the public bucket to private, deploy, and repeat step 3 on an object
   uploaded while it was public: it must now be refused.

Any failure stops the rollout at the step it is in.

## Backing out

Nothing here deletes data. The default `deletion_policy = "retain"` holds on
every path below; never set `"delete"` to back out, it destroys every object.

- **Before step 3** (the forge release not cut): nothing reached users.
  Fix the operator in control-plane and repeat step 2.
- **After step 3, operator defect:** roll the control-plane operator back to
  (or forward past) its last good release. ManagedBuckets already published
  stay; the operator re-reconciles them when it returns. Cloud buckets and
  their objects are not owned by the operator's pods, so a rollback of the
  operator removes neither. Do not delete `forge-bucket-<name>` Secrets or
  the ManagedBucket resources by hand: the first stops every workload that
  reads them from starting, the second invites a re-provision under a new
  name.
- **After step 3, forge defect:** roll forward with a forge fix (forge has
  no rollback; `deploy` skill, "Verify + recover"). A consumer that must stop
  using hosted buckets meanwhile rebinds its buckets to `forge.BucketOnS3`
  in its env (the retained bucket's name is in the ManagedBucket's
  `status.bucketName`) and deploys; the retained bucket keeps its objects.
- **A leaked credential:** rotate it in the operator (a second key is
  created, the Secret moves to it, then the old key is retired, per
  `ManagedBucketStatus.CredentialID`); do not delete the Secret.
