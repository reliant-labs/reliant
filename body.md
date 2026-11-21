## review:changes

Lane: **HIGH** (no model review: the rollout pre-check stopped it before the first pass; deterministic checks ran).

HIGH: pipeline (.github/workflows/ci.yml, scripts/release-gate-go-timings.json, …)

No model reviewed this head: the PR body has no Post-merge check and no Rollback section, which every HIGH-risk PR needs, so the review stopped before its first pass. The deterministic checks ran; everything they found is below.

- **blocker** [B1] `:0` — The PR body has no Post-merge check and no Rollback section. A HIGH-risk PR names, for every effect only production or a write shows, how it is checked after the deploy (Post-merge check) and how it is undone if that check fails (Rollback); the review blocks on any effect without them, so no model reviewed this head (rollout-sections) Fix: Add a "## Post-merge check" and a "## Rollback" section to the PR body ("none: <why>" is an answer when nothing is live-only), then push, or reply citing this blocker's id to have this same head reviewed.

Fix the blockers and push: the push is reviewed again. (pr-fix takes over if nobody does.)

Reviewed `ed5e311e9920a8357035574203d1f80fd6627151`.
<!-- review-changes sha=ed5e311e9920a8357035574203d1f80fd6627151 -->
<!-- review-round sha=ed5e311e9920a8357035574203d1f80fd6627151 kind=code -->
<!-- review-precheck sha=ed5e311e9920a8357035574203d1f80fd6627151 -->
<!-- review-blockers sha=ed5e311e9920a8357035574203d1f80fd6627151 [{"gap":"defect","kind":"code","line":0,"path":"","problem":"The PR body has no Post-merge check and no Rollback section. A HIGH-risk PR names, for every effect only production or a write shows, how it is checked after the deploy (Post-merge check) and how it is undone if that check fails (Rollback); the review blocks on any effect without them, so no model reviewed this head (rollout-sections)"}] -->