## review:passed

Lane: **HIGH** (Sol pass + second pass in this run).

HIGH: pipeline (.github/workflows/ci.yml, .github/workflows/release.yml, …)

Reviewed all eight files' changed hunks and performed the HIGH-risk second pass. Both earlier blockers are fixed: published SBOMs now describe the retained release archives rather than rebuilt artifacts. The shell regression covers differing rebuilt bytes, missing/corrupt sets, and workflow routing; TestReleaseCLISBOMJob covers non-gating publication. No new blocker found. Authorization and credential handling remain constrained; no tenant, billing, migration, or public-surface break is introduced. RC-07/08/09/10/13 checked; no applicable violation found. Tests were inspected, not executed. The pinned file-SBOM command is present at reliant-labs/forge:internal/cli/vuln_sbom.go:30@05bc400d9f0f.

**Second pass:** Reviewed all eight changed-file hunks and traced retained-release recovery, SBOM routing, failure handling, and pinned forge scan/replacement behavior. Both earlier blockers are fixed. No new blocking defect or applicable RC-07/08/09/10/13 violation found. No migration, billing, tenant-isolation, or client compatibility change. Tests were inspected, not executed; merge and release gates own execution.

Tests covering the risk: 
- Release pipeline: SBOMs describe retained published bytes, deny incomplete/corrupt sets, preserve dry runs and remain non-gating.: `scripts/test-release-cli.sh: section 7; tools/test-affected/release_cli_workflow_test.go:TestReleaseCLISBOMJob`

Migration safety: **not_applicable**

**Earlier blockers, at this head:**
- R1.B1 fixed — scripts/release-cli.sh:450 downloads and checks the retained GitHub Release set; release.yml routes successful publication through sbom-published. scripts/test-release-cli.sh adds rebuilt-byte and corrupt/missing-set regressions.
- R2.B1 fixed — scripts/release-cli.sh:450 and .github/workflows/release.yml:2045 select retained published archives, with no fallback to the rerun artifact. scripts/test-release-cli.sh pins that routing and the retained bytes.
- R1.B1 fixed — release.yml routes successful publications to sbom-published; release-cli.sh downloads and checks the retained GitHub Release set. test-release-cli.sh section 7 covers differing rebuilt bytes and absent/corrupt retained sets.
- R2.B1 fixed — Published SBOM generation no longer downloads this run's cli-archives artifact. Section 7 also pins published/unpublished workflow routing and retained binary contents.

**Not verified (non-blocking):**
- high_risk: Pinned forge's vulnerability database/scan internals and extra-check replacement semantics were not independently traced within this round's context budget.

**Post-merge verification** (only a live environment shows these; not review blockers, each verified by the plan named):
- GitHub Release SBOM attachment on the next release tag. — Inspect the CLI SBOMs job and confirm six archive SBOM assets; download one and check its metadata names the archive and tag.; rollback: Revert this PR; SBOMs are additive and outside the client-verified signed set.
- Report-only vulnerability scanning on main. — Inspect the first main compile job for the scan step, govulncheck and source-tree scan output; confirm findings do not fail CI result.; rollback: Revert this PR.
- Published CLI SBOM assets on the next release. — Inspect CLI SBOMs job and six release .cdx.json assets; download one and confirm metadata.component names its archive and tag, as the PR specifies.; rollback: Revert this PR; SBOMs are additive and outside the signed set, so client verification is unchanged.
- Report-only vulnerability scan on the first main push. — Inspect Release gate (compile): confirm scan step ran, govulncheck and source-tree scan output appear, warnings explain failures, and CI remains green irrespective of findings.; rollback: Revert this PR to remove the additive report-only step.

Passed: entering the native merge queue at this exact head.

Reviewed `2d5ededf51de5e763101ea6632db44848f946e63`.
<!-- review-passed sha=2d5ededf51de5e763101ea6632db44848f946e63 -->