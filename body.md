## review:changes

Lane: **HIGH** (Sol pass + second pass in this run).

HIGH: pipeline (.github/workflows/release.yml, scripts/release-cli.sh)

Reviewed all seven changed-file diffs and performed the HIGH-risk second pass. Publication remains permission-scoped and additive; no tenant, billing, migration or compatibility changes. TestReleaseCLISBOMJob and scripts/test-release-cli.sh cover normal publication and failures, but miss the signed-set recovery case below. RC-07/08/09/10 otherwise reveal no violation. Pinned forge supports the invoked file-SBOM contract (reliant-labs/forge:internal/cli/vuln_sbom.go:92@05bc400d9f0f). Tests were inspected, not executed.

- **blocker** [B1] `.github/workflows/release.yml:2031` — The SBOM job always describes this run's cli-archives artifact, but cli-publish can replace its local archives with a previously signed set (scripts/release-cli.sh:331–345). On a release rerun, rebuilt binaries can differ—for example, setup-go may select a newer toolchain patch—while publication correctly retains the original signed binaries. This job then overwrites the release's SBOMs with descriptions of binaries that were not published. The current tests never exercise recovery followed by SBOM generation. Fix: When cli-publish succeeds, download and verify the release's stored signed archives and generate SBOMs from those exact bytes. Keep using the run artifact for unpublished dry runs. Add a regression test with different run-artifact and recovered archive contents proving the attached SBOM describes the stored release.

**Post-merge verification** (only a live environment shows these; not review blockers, each verified by the plan named):
- SBOM assets on the next actual release. — Confirm CLI SBOMs is green, list six archive .cdx.json assets, and inspect the downloaded linux/amd64 document's metadata.component for the archive and tag.; rollback: Revert this PR; the additive SBOM job is outside the signed set and reverting does not change client verification.

Fix the blockers and push: the push is reviewed again. (pr-fix takes over if nobody does.)

Reviewed `503793acd7914468cdfa7dc3ac6b2280009c4c6a`.
<!-- review-changes sha=503793acd7914468cdfa7dc3ac6b2280009c4c6a -->
<!-- review-round sha=503793acd7914468cdfa7dc3ac6b2280009c4c6a kind=code -->
<!-- review-blockers sha=503793acd7914468cdfa7dc3ac6b2280009c4c6a [{"gap":"defect","kind":"code","line":2031,"path":".github/workflows/release.yml","problem":"The SBOM job always describes this run's cli-archives artifact, but cli-publish can replace its local archives with a previously signed set (scripts/release-cli.sh:331–345). On a release rerun, rebuilt binaries can differ—for example, setup-go may select a newer toolchain patch—while publication correctly retains the original signed binaries. This job then overwrites the release's SBOMs with descriptions of binaries that were not published. The current tests never exercise recovery followed by SBOM generation."}] -->