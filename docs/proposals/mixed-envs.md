# Proposal: mixed envs (a self-managed env that also holds hosted, placed items such as a static site)

**Status:** analysis, not implemented. Needs an owner decision.
**Date:** 2026-10-21
**Origin:** forge#1078, moved here on 2026-11-04 and closed, with its one comment. Nothing below has changed since then.

**Found while moving reliant-web (app.reliantlabs.io) off Firebase onto forge hosted static (Session K, 2026-10-21).** Owner decision pending. This records the analysis; nothing here is implemented.

## What happened

control-plane prod declares cluster workloads applied by forge to our own clusters, so the env kind is `self_managed`. Adding `forge.Frontend{runtime = forge.OnHosted {}}` to it made forge classify the env `persistent` (`internal/cli/hosted_env_resolver.go hostedEnvKindOf`: `HasHosted()` wins over `runsOnOwnCluster`). The bundle write was refused:

    ensure environment "prod": EnsureEnvironment failed (failed_precondition):
    environment "prod" exists as kind "self_managed" and the declaration says "persistent";
    an environment's kind never changes

The release (images, plus the static artifact) was cut and pushed; only the bundle record failed. Workaround shipped: reliant-web is its own persistent env `prod-web` (`deploy/kcl/prod-web/main.k`).

## Why it is a model gap, not a bug

- **forge** (`pkg/release/env.go`): kind is a predicate over the declaration. `persistent` = something is OnHosted; `self_managed` = nothing hosted, runs on a cluster the author operates. A declaration with both has no kind.
- **control-plane** (`internal/deploy/types.go`, `deploystore/create.go`, `ck_cp_deploy_envs_placement`): `persistent`/`preview` are PLACED (namespace + host cluster; the platform schedules and converges). `self_managed` places nothing and may name a connected cluster. A placed env may NOT name a cluster. The StaticSite CR for a hosted site lives in the env's namespace, so a hosted item needs placement.
- forge's deploy path already knows "mixed" (`deploy.go` applyPath `mixed…`, `envAppliesLocally`), but the ledger and the server model do not.

## Options

1. **Separate envs (status quo, shipped).** The hosted part is its own persistent env in the same project. No model change, and it is the customer path today. Cost: two deploys per release, plus a guard against them drifting (cp: its release script deploys both; `check-web-version.sh` compares the served `/version.json` with the pin).
2. **First-class mixed env.** A `self_managed` env that may ALSO hold placed static sites (and maybe hosted workloads):
   - cp: relax `ck_cp_deploy_envs_placement` so a self_managed env may carry a namespace + host cluster used ONLY for its placed items; triggers that forbid deployment rows in self_managed envs must admit StaticSite deployments; the converger must apply the hosted half while forge keeps applying the cluster half (the `hub_converged` / `forge_applies` split already exists per env and would become per item or per tree).
   - forge: the kind predicate becomes `self_managed` when anything runs on an own cluster, even if something is also hosted. The hosted publish path must accept a self_managed env with placement. `env shape` / admission / capacity must account for the hosted half.
   - Migration: existing self_managed envs gain placement lazily on their first hosted item.
   - Value: one env, one release, one deploy for "my cluster + a hosted site". That is likely common for customers with BYO clusters and a marketing or app SPA.
   - Risk: it widens the meaning of the most security-relevant field (who applies, and where secrets live) and touches every reader of `kind` (alerting `ConvergedKinds`, cpsecrets, identityreconcile, reconcilestore, deployplan).
3. Recreating prod as persistent is rejected: destructive, and persistent envs cannot bind clusters.

Recommendation: keep (1) until a customer asks for (2). If we build (2), design it as "placement is per item, kind is about who applies the cluster half", not as a new kind.

## Follow-up note (2026-10-21)

Related foot-gun hit during the same move: convergence inference flips silently. A fully bound env with no `applier`/`hub_converges` is inferred hub-converged UNLESS it ships a frontend from the release machine (`envInferredHubConverged`). control-plane prod relied on its Firebase frontend for that. Moving the frontend to its own env flipped prod onto the hub with no diff in any convergence-related line. The deploy was refused only by the hub-ready check (`hub has never reported reconciling it`). Fixed on our side by declaring `applier = "forge"`. Suggestion for forge: once an env has been recorded with a convergence split, a change of INFERRED split should be a plan finding (stop-class), not a silent re-derivation. Or refuse inference for an env whose ledger row says `hub_converged = false`.
