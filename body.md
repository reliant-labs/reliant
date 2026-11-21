## review:changes

Lane: **HIGH** (no model review: the rollout pre-check stopped it before the first pass; deterministic checks ran).

HIGH: prod-deploy (deploy/kcl/config_gen.k)

No model reviewed this head: the PR body has no Post-merge check and no Rollback section, which every HIGH-risk PR needs, so the review stopped before its first pass. The deterministic checks ran; everything they found is below.

- **risk** `internal/diskwatch/worktreereap_test.go:200` — The test reads the wall clock (RC-04): an assertion that holds only away from an hour, day or interval boundary, or on timestamps that happen not to contain a pattern, fails at random. (clock-test) Fix: Inject a fixed clock, or align the test to the boundary it depends on.
- **blocker** [B1] `:0` — The PR body has no Post-merge check and no Rollback section. A HIGH-risk PR names, for every effect only production or a write shows, how it is checked after the deploy (Post-merge check) and how it is undone if that check fails (Rollback); the review blocks on any effect without them, so no model reviewed this head (rollout-sections) Fix: Add a "## Post-merge check" and a "## Rollback" section to the PR body ("none: <why>" is an answer when nothing is live-only), then push, or reply citing this blocker's id to have this same head reviewed.

**Generated, reviewed through their inputs (not line by line):** `cmd/reliant/cmd/roots_gen.go`, `frontends/web/src/gen/reliant/config/v1/cli_config_pb.ts`, `frontends/web/src/gen/reliant/config/v1/config_pb.ts`, `gen/reliant/config/v1/cli_config.pb.go`, `gen/reliant/config/v1/config.pb.go`, `generated/docs-source/reference/cli.md`, `internal/app/graph_gen.go`, `pkg/config/config_gen.go`

Fix the blockers and push: the push is reviewed again. (pr-fix takes over if nobody does.)

Reviewed `b52e90dda02c5cc1c48fdd915a7485241b8ef24c`.
<!-- review-changes sha=b52e90dda02c5cc1c48fdd915a7485241b8ef24c -->
<!-- review-round sha=b52e90dda02c5cc1c48fdd915a7485241b8ef24c kind=code -->
<!-- review-precheck sha=b52e90dda02c5cc1c48fdd915a7485241b8ef24c -->
<!-- review-blockers sha=b52e90dda02c5cc1c48fdd915a7485241b8ef24c [{"gap":"defect","kind":"code","line":0,"path":"","problem":"The PR body has no Post-merge check and no Rollback section. A HIGH-risk PR names, for every effect only production or a write shows, how it is checked after the deploy (Post-merge check) and how it is undone if that check fails (Rollback); the review blocks on any effect without them, so no model reviewed this head (rollout-sections)"}] -->