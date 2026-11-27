---
name: vuln/registries
description: Where release SBOMs live — digest-verified retrieval, OCI referrers, registry support and tag fallbacks, copying images with their SBOMs, and cleanup-policy caveats.
---

# Where the SBOM lives, and registries

The release record is the index. `forge vuln sbom` reads the machine ledger copy by
digest and verifies the bytes against it; a missing copy is read back from the
referrer the release names, or discovered among the image's CycloneDX referrers.
A referrer is followed only once it proves to be the subject's SBOM: in the
subject's own repository, an OCI manifest of type `application/vnd.cyclonedx+json`
whose `subject` is that image's digest, with a layer of the recorded SBOM digest. A
recorded referrer that fails the check is never read, and bytes from any source are
verified against the release's digest.

The referrer is a plain OCI 1.1 manifest: `artifactType application/vnd.cyclonedx+json`,
one layer holding the SBOM, `subject` = the artifact's manifest. It is attached only
to artifacts **this project pushed** in this cut (a pinned third-party image, or a
prebuilt image another pipeline pushed, gets none, and no warning). A failed attach
warns and leaves the ledger copy; it never fails the cut. `vuln.attach_sbom: false` in
forge.yaml turns attaching off (default on).
Referrers travel with the image through `oras cp -r` (tested between GAR, a tag-fallback registry and zot, in every direction).

forge never names a registry: it probes the referrers API and falls back to the OCI
tag schema, so **every registry that accepts an OCI image manifest with a `subject`
works** for writing; they differ on read:

| Registry | Referrers API | What forge does |
|---|---|---|
| GAR / gcr.io, Docker Hub, quay.io, Amazon ECR, MCR, zot | yes | listed through the API |
| GHCR, `distribution` v2/v3, Reliant's hosted registry | no | forge maintains the `sha256-<hex>` fallback tag index itself |
| `bundle.v1` repositories on the hosted registry | refused (a tenant token may not push a manifest with `subject` there) | no referrer; the bundle carries no SBOM, the ledger copy covers the release |

(Probed in the design's phase 0 for GAR, the hosted registry, distribution and zot;
the others are the API's published support and untested for writes.) Tag-fallback
registries lose concurrent writes to the index; forge serializes attaches per subject
and re-checks after each, and its orphaned old indexes are left for the registry's GC
(a tenant token cannot delete).

**Warning, not tested: a "delete untagged" cleanup policy.** An OCI referrer is an
untagged manifest. On Google Artifact Registry a cleanup policy that deletes
untagged versions may delete SBOM referrers (and the image signatures that ride the
same mechanism) after the policy's age. Exempt referrers from such a policy, or set
`vuln.attach_sbom: false` so nothing depends on them: the release and the machine
ledger keep the SBOM either way.

For the release defaults, commands and vulnerability policy, load `vuln`.
