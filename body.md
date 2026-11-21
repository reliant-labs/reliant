## review:changes

Lane: **MEDIUM** (one pass on gpt-6-luna@codex).

MEDIUM: code (docs/docs.json)

Review found the earlier fork-preview cleanup blocker remains: the close workflow excludes fork-origin PRs from cleanup. Other changed hunks reviewed; no additional blocker identified.

- **blocker** [B1] `docs/hosting/preview-environments.mdx:104` — (again: R1.B1) The `down` job still requires `head.repo.full_name == github.repository`. Unlike `up`, close-event cleanup need not execute PR code, so a fork PR preview created by a maintainer or another trusted process cannot be deleted on close; it remains billable until expiry/GC. The prose suggesting `pull_request_target` cleanup does not implement it in the documented workflow. Fix: Use a trusted close-event workflow that does not check out or execute PR code, and run cleanup with the repository credential for previews created for fork PRs.

**Earlier blockers, at this head:**
- R1.B1 not_fixed — docs/hosting/preview-environments.mdx:104 retains the fork-head equality condition on `down`, and lines 127-129 repeat the same restriction.

Fix the blockers and push: the push is reviewed again. (pr-fix takes over if nobody does.)

Reviewed `c5710c22d2119f7db8a5c9d7a5661a6386dff6a0`.
<!-- review-changes sha=c5710c22d2119f7db8a5c9d7a5661a6386dff6a0 -->
<!-- review-round sha=c5710c22d2119f7db8a5c9d7a5661a6386dff6a0 kind=code -->
<!-- review-blockers sha=c5710c22d2119f7db8a5c9d7a5661a6386dff6a0 [{"gap":"defect","kind":"code","line":104,"path":"docs/hosting/preview-environments.mdx","problem":"(again: R1.B1) The `down` job still requires `head.repo.full_name == github.repository`. Unlike `up`, close-event cleanup need not execute PR code, so a fork PR preview created by a maintainer or another trusted process cannot be deleted on close; it remains billable until expiry/GC. The prose suggesting `pull_request_target` cleanup does not implement it in the documented workflow."}] -->