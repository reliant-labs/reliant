# Design: per-workload placement — one environment holds any mix of hosted and self-applied parts

**Status:** proposed, awaiting owner sign-off (§11). Nothing here is implemented. **Date:** 2026-10-26.
**Scope:** forge (`pkg/release`, `internal/cli`, `internal/deploytarget`, `internal/bundle`, KCL) and the
control plane (`deploy_environments` and everything that reads its `kind`; the deploy, plan, admission,
reconcile, secret and domain paths), with control-plane's own `prod` + `prod-web` split as the forcing case.
**Owner direction (2026-10-26 04:11Z):** "services should be able to mix and match with some services hosted
and some not... not an all or nothing thing that's silly. Each service declares where/how it is hosted/served....
not an env...."

## Summary

- **Placement belongs to the workload or frontend.** Each one already declares its runtime binding
  (`OnCluster`, `OnHosted`, `OnBucket`, `OnFirebase`, `OnCompose`, `OnHost`, `BuildOnly`). That binding is the
  only placement fact. An environment has no placement kind.
- **One env, one release, one deploy.** An env holds any mix of runtimes. One release carries all of them. One
  `forge env deploy` applies all of them, in an order derived from declared dependencies (§6). The control
  plane places only the hosted parts.
- **The env keeps the facts that really are env-wide.** These are its identity, its lifetime
  (`persistent | preview`), its secret posture (`readable | write_only`, which can only ratchet one way), its
  cluster bindings, and a hosted slot (namespace + host cluster). The hosted slot is allocated the first time a
  hosted part appears, not decided at creation.
- **Every predicate that read `kind` is asked of facts instead**: "does this env have hosted deployments", "is
  it bound to a cluster", "who actuates this placement". Most are answered per release or per deployment row. §3
  and §4 list every call site and what replaces it.
- **`kind` is removed** from `pkg/release`, the wire and the schema. This uses forward-only expand/contract
  migrations; no compatibility with old clients is kept (we are pre-launch). The one exception is reading sealed
  historical bundles, whose bytes are content-addressed and cannot be rewritten (§5.4).
- **prod-web merges into prod** with zero downtime. The SPA becomes a hosted frontend of `prod`. It is served from
  a new prefix in the same bucket, through the same CDN and static origin, and is verified on its platform
  hostname. Then `app.reliantlabs.io` is moved with the single-row atomic rebind the bindings table was built for.
  prod-web is archived, not rewritten (§8).

## 1. The defect, precisely

### 1.1 Where kind is derived, recorded and enforced

| Step                | Where                                                                                                                                                                                                                                                                                                                                                                                                                            | What it does                                                                                         |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Derived             | forge `internal/cli/hosted_env_resolver.go:136` `hostedEnvKindOf`                                                                                                                                                                                                                                                                                                                                                                | anything hosted → PERSISTENT; else anything on an author-operated cluster → SELF_MANAGED; else LOCAL |
| Derived (shape)     | forge `internal/cli/env_shape.go:464` `shapeEnvKindOf` → `release.Shape.Kind` (`pkg/release/bundle.go:428`)                                                                                                                                                                                                                                                                                                                      | the same predicate, written into every bundle's shape and every declared shape                       |
| Defined             | forge `pkg/release/env.go` `EnvKind` (+ `Placed()`, `Promotable()`), `internal/deploytarget/hosted.go:296` `HostedEnvKind`                                                                                                                                                                                                                                                                                                       | "immutable once recorded: an env whose kind changed is a different env"                              |
| Sent                | every `EnsureDeployEnvironment` (`hostedEnvRefFor`, `hosted_env_resolver.go:98`). `HostedProvider.Deploy` **hard-codes** `HostedEnvPersistent` (`deploytarget/hosted.go:1157`)                                                                                                                                                                                                                                                   |                                                                                                      |
| Recorded, immutable | cp `internal/handlers/deploy/rpc_ensure_environment.go:198`: "exists as kind %q and the declaration says %q … never changes"; `rpc_update_environment.go:69`; `declared_shape.go:103` (shape kind must equal spec kind)                                                                                                                                                                                                          |                                                                                                      |
| Enforced in schema  | cp `ck_cp_deploy_envs_kind`, `ck_cp_deploy_envs_ttl`, `ck_cp_deploy_envs_placement` (namespace/host cluster iff persistent/preview), `trg_cp_deployments_platform_placed` (no deployment rows in `local` or `self_managed`), `trg_cp_deploy_promotions_not_local` (migrations `20261002154802_deploy_env_self_managed_kind`, `20261003120000_cluster_credential_and_self_managed_target`, `00095_deploy_env_project_local_kind`) |                                                                                                      |

### 1.2 forge is already per-workload almost everywhere else

The CLI's deploy path already treats hosting as per-workload. `runDeploy` splits the hosted group off and applies
the local part first. It then publishes the hosted group in the same run (`internal/cli/deploy.go:1425`,
`splitHostedGroups`). The comment on `dispatchHostedDeploy` (`deploy_hosted.go:40`) says "Hosting is per
workload, never an env mode". `forge env status` already reports `destination = mixed`
(`env_destination.go:78`). The **env kind is the remaining wall**, and three CLI-side all-or-nothing rules ride on
it (§3).

### 1.3 What `kind` actually conflates

One closed column answers four different questions:

| Question                                                                              | Today's answer                                         | Really a property of                                                   |
| ------------------------------------------------------------------------------------- | ------------------------------------------------------ | ---------------------------------------------------------------------- |
| Is it reaped (TTL)?                                                                   | `preview`                                              | the **env** (lifetime)                                                 |
| Are its secrets readable back?                                                        | `local`                                                | the **env** (trust boundary), and it must only ratchet toward stricter |
| Does the platform hold placement (namespace, host cluster), schedule and converge it? | `persistent \| preview`                                | **each workload**: "this one is `OnHosted`"                            |
| Who applies it, and where is "what is live" recorded?                                 | self_managed → forge or hub; placed → hosted converger | **each placement**                                                     |

The first two are real env facts. The last two are per-workload facts promoted to the env, and the defect is
exactly that promotion. A self-managed env could not gain a hosted site (#1099 → #1105), and the inverse is
refused too: a placed env "may not bind a cluster". The 2026-10-24 prod-web outage (22:14Z → 02:42Z,
`runtime_config_guard.go:21`) happened because the split let a hotfix branch ship the SPA's env independently of
the API's. The fix that followed, #1284, is a guard on a seam the split created.

## 2. The model

### 2.1 Placement is the item's runtime binding

| Runtime binding                                                                   | Placement                                       | Actuator                          | "What is live" evidence                                     |
| --------------------------------------------------------------------------------- | ----------------------------------------------- | --------------------------------- | ----------------------------------------------------------- |
| `OnHosted` workload / `ManagedDatabase` hosted / `OnHosted` frontend (StaticSite) | the env's **hosted slot** on the platform fleet | the platform's hosted converger   | `deployments.applied_bundle_id` (`deployplan/store.go:208`) |
| `OnCluster` (bound to a connected cluster, `hub_converges` unit)                  | that cluster                                    | the hub (Flux)                    | `deploy_convergences` (`deployplan/store.go:269`)           |
| `OnCluster` (`applier = "forge"`, or unbound)                                     | that cluster                                    | forge, from the deploying machine | `deploy_applies` (BeginApply/FinishApply)                   |
| `OnBucket` / `OnFirebase` frontend                                                | the author's bucket / Firebase site             | forge                             | `deploy_applies`                                            |
| `OnCompose` / `OnHost` / host infra                                               | the developer machine                           | `forge env up`                    | session report, never a promotion                           |
| `BuildOnly`                                                                       | nowhere                                         | —                                 | release artifact only                                       |

This table already exists implicitly across forge. The design makes it the only source of placement truth, and
removes the env-level summary that contradicts it.

### 2.2 What the environment still holds

| Field            | Values                                                          | Rule                                                                                                                                                                                  |
| ---------------- | --------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| identity         | `(org, project, name)`                                          | unchanged                                                                                                                                                                             |
| `lifetime`       | `persistent \| preview`                                         | immutable. `preview` ⇔ `expires_at IS NOT NULL` (the existing TTL biconditional, renamed off `kind`)                                                                                  |
| `secret_posture` | `readable \| write_only`                                        | **monotonic**: readable → write_only only, never back. This keeps the one security property kind's immutability existed for (`deploy/types.go:296`, "which is why kind is immutable") |
| hosted slot      | `namespace`, `host_cluster_id` (+ optional `tenant_cluster_id`) | NULL until the first hosted part is ensured. Allocated once, in the same transaction as that part's deployment row, and immutable afterwards. Released only when the env is deleted   |
| cluster bindings | `deploy_environment_cluster_bindings`                           | unchanged. Now legal beside a hosted slot                                                                                                                                             |
| `hub_converged`  | as today                                                        | read per placement (cluster units), no longer guarded by `kind = 'self_managed'`                                                                                                      |

### 2.3 Derived predicates (replace `IsPlaced`, `IsDeployTarget`, `Local`)

All of these are cheap SQL over stored facts. None is a stored mode.

| Predicate               | Definition                                                                                           | Replaces                                                                      |
| ----------------------- | ---------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `HasHostedSlot(env)`    | `namespace IS NOT NULL`                                                                              | `kind.IsPlaced()` where the question is "is there a namespace"                |
| `HostedParts(release)`  | the bundle's objects under `release.BundleHostedCluster` / its shape workloads with a hosted runtime | `kind.IsPlaced()` where the question is "does this release need the platform" |
| `PlatformActuates(env)` | the env has a hosted slot **or** a bound cluster with `hub_converged`                                | the reconcile-lease allowlist (`reconcilestore/store.go:133`)                 |
| `Promotable(env)`       | `secret_posture = 'write_only'`                                                                      | `IsDeployTarget` (everything but local)                                       |
| `SecretsReadable(env)`  | `secret_posture = 'readable'`                                                                        | `kind = 'local'` (`secret_store_authz.go:84,138`, `localsecret`)              |
| `Scratch(env)`          | `lifetime = 'preview' OR secret_posture = 'readable'`                                                | `appdb.effectivePolicy`'s preview/local test                                  |

### 2.4 Invariants the schema keeps (fail-closed, as today)

1. `secret_posture = 'readable'` ⇒ no hosted slot, no cluster binding, no promotion, no deployment row. Today's
   LOCAL rules restate onto the posture.
2. A deployment row requires its env to have a hosted slot. The trigger tests the fact
   (`namespace IS NOT NULL`), not a kind.
3. `(namespace IS NULL) = (host_cluster_id IS NULL)`. A `tenant_cluster_id` or `scale_to_zero_enabled` requires
   a hosted slot (they describe hosted placement only).
4. Allocating a hosted slot, adding a cluster binding, or recording a promotion **ratchets** `secret_posture`
   to `write_only` in the same transaction (a trigger, so no writer can forget). The ensure response says so
   loudly (§7.6).

## 3. Every forge use of kind, and its replacement

| Call site                                                                                                                                                      | Today                                                           | Replacement                                                                                                                                                                                                                      |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pkg/release/env.go` `EnvKind`, `EnvKinds`, `Placed`, `Promotable`, `ParseEnvKind`; `EnvRecord.Kind`                                                           | closed kind                                                     | **deleted**. `EnvRecord` gains `Lifetime` and `SecretPosture`, read from the server and never derived by forge                                                                                                                   |
| `pkg/release/bundle.go:428` `Shape.Kind`; `Shape.Validate` kind check (`:974`); `EnvRecord.Validate` shape/kind agreement                                      | kind in every shape                                             | **deleted** from new shapes. Placement is already in `Shape.Workloads[].Runtime` (+`Cluster`). Decode tolerates a legacy `kind` key in sealed bundles (§5.4)                                                                     |
| `pkg/release/diff.go` `ShapeDiff.KindChanged`                                                                                                                  | "always an error"                                               | **deleted**. A placement move is already reported per workload in `RuntimeChanges`                                                                                                                                               |
| `internal/bundle/shape.go:21` `ShapeInput.Kind`                                                                                                                |                                                                 | deleted                                                                                                                                                                                                                          |
| `internal/cli/hosted_env_resolver.go` `hostedEnvKindOf`, `runsOnOwnCluster`, `hostedControlPlaneKindName`, `isLocalControlPlaneEnv`                            | env classifier                                                  | **replaced by `envPlacements(e) placementSet`** (hosted / cluster-forge / cluster-hub / own-static / machine), one pure function the rest ask questions of. `isLocalControlPlaneEnv` becomes `placements.OnlyMachine()`          |
| `internal/cli/env_shape.go:464` `shapeEnvKindOf`                                                                                                               |                                                                 | deleted. `forge env shape` prints placements per workload                                                                                                                                                                        |
| `internal/deploytarget/hosted.go:296` `HostedEnvKind`, `HostedEnvRef.Kind`, `:1157` hard-coded `HostedEnvPersistent`, `:446` "no environment kind was derived" | ensure carries a kind                                           | the ensure carries `lifetime` (and the TTL for previews) and **no placement claim**. The server derives the hosted slot from the deployments it is asked to ensure and the posture from §2.4                                     |
| `internal/cli/hosted_ledger.go:927` refuse promotion into LOCAL                                                                                                | client-side kind check                                          | `placements.OnlyMachine()` refusal kept (same message); the server is authoritative via `Promotable`                                                                                                                             |
| `internal/cli/env_hosted_lifecycle.go:89` `requireHostedEnv` (suspend/resume/restart verbs)                                                                    | refuses anything not PERSISTENT                                 | requires **at least one hosted part**, acts on the hosted deployments only, and prints the parts it does not own ("api runs on cluster prod-k8s; forge applies it")                                                              |
| `internal/cli/audit_prereqs_cli.go:133` `envAppliesManifestsToCluster`                                                                                         | "only a PERSISTENT env's manifests are applied by the platform" | **bug for a mixed env** (it would skip the cluster workloads' prereqs). Becomes per workload: any `OnCluster` workload forge applies is demand                                                                                   |
| `internal/cli/unit_convergence.go:194` `hubUnitsConvergeable`: "a hosted tier … and one env cannot be both"                                                    | refusal                                                         | **removed**. Hosted parts are published, and hub units are converged. They are different actuators for different placements in one env. `envInferredHubConverged` (`:208`) stays a per-unit inference over the cluster part only |
| `internal/cli/session_reporter.go:125`                                                                                                                         | reports kind                                                    | reports placements                                                                                                                                                                                                               |
| `internal/cli/ledger_cmd.go:522,661`, `hosted_import.go:465`, `hosted_records.go:141`, `hosted_live.go:432`                                                    | file-ledger import / record mapping by kind                     | an imported env is `lifetime = persistent`, `secret_posture = write_only`. Nothing else is claimed                                                                                                                               |
| `internal/cli/env_destination.go:24` `control_plane_kind` in `forge env status --json`                                                                         | `local \| persistent`                                           | replaced by `secret_posture` + the existing `destination` (`mixed` already exists) + `workloads[].placement`. This is a breaking JSON change, called out in the PR title                                                         |
| `kcl/schema.k:6526` (Bundle.applier doc)                                                                                                                       | refers to the derived kind                                      | rewritten to name placements                                                                                                                                                                                                     |

Unchanged and already correct: `dispatchHostedDeploy`, `envAppliesLocally`, `splitHostedGroups`,
`destinationOf`, `generate_ci_hosted.go`, `hosted_capacity.go`, `smoke.go` (all ask `HasHosted()`, which is a
per-item question).

## 4. Every control-plane use of kind, and its replacement

| Call site                                                                                                                                                                      | Today                                                                | Replacement                                                                                                                                                                                                                                                                                                                                                                                   |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `handlers/deploy/rpc_ensure_environment.go:198`                                                                                                                                | kind immutable → FailedPrecondition                                  | **gone**. Ensure no longer carries a placement. The only immutable field left is `lifetime` (a preview cannot become persistent; a TTL'd row stays TTL'd)                                                                                                                                                                                                                                     |
| `handlers/deploy/rpc_update_environment.go:69`                                                                                                                                 | refuses a kind change                                                | refuses a lifetime change                                                                                                                                                                                                                                                                                                                                                                     |
| `handlers/deploy/declared_shape.go:103`                                                                                                                                        | shape kind must equal spec kind                                      | deleted                                                                                                                                                                                                                                                                                                                                                                                       |
| `handlers/deploy/rpc_create_environment.go:159` `requirePlacementCluster`, `:208` `requireLiveTargetCluster`, `:476` placement fields only when placed                         | kind-gated                                                           | `requirePlacementCluster` runs when a hosted slot is **allocated** (first hosted ensure), not at env creation. `requireLiveTargetCluster` runs for any binding. Tenant cluster / scale-to-zero require a hosted slot (invariant 3)                                                                                                                                                            |
| `deploystore/create.go:93`                                                                                                                                                     | allocates namespace iff placed                                       | never allocates at creation. A new `deploystore.EnsureHostedSlot(tx, env)` allocates on the first hosted `EnsureDeployment`, under the env row lock (so two concurrent first deployments allocate one namespace)                                                                                                                                                                              |
| `deploy/types.go` `EnvironmentKind`, `IsPlaced`, `IsDeployTarget`                                                                                                              |                                                                      | deleted. `Environment` gains `Lifetime`, `SecretPosture`, `HasHostedSlot()`                                                                                                                                                                                                                                                                                                                   |
| `deployplan/store.go:65` `readLiveBundleRef` dispatch                                                                                                                          | **one** evidence source chosen by kind                               | **union** of the placements the bundle actually has: hosted parts from `deployments.applied_bundle_id`, hub clusters from `deploy_convergences`, forge-applied clusters from `deploy_applies`. Live = the bundle every present placement agrees on. Disagreement is the existing `Disagreed` (converging) answer, never a guess                                                               |
| `deployplan/assemble.go:28`                                                                                                                                                    | refuses LOCAL                                                        | refuses `!Promotable`                                                                                                                                                                                                                                                                                                                                                                         |
| `deploystore/plan_gate.go:198` `environmentIsConverged`                                                                                                                        | only placed envs need an approved plan for the converger             | the plan is required whenever the **release has a platform-actuated placement** (hosted parts, or hub units), whatever else it carries                                                                                                                                                                                                                                                        |
| `handlers/deploy/bundle_admission.go:471` `bundleDestination` / `admitBundleObjects`                                                                                           | ONE destination per env: isolated (self-managed) **or** hosted       | **per cluster path** of the bundle (it is already split cluster × unit × phase). Objects under the hosted path go through `admitHostedObjects` against the env's hosted slot, and an absent slot is refused. Objects under a bound cluster go through `admitBoundClusters`. **An object under any other path is refused** (fail closed)                                                       |
| `handlers/deploy/bundle_demand.go:181` `isHostedEnvironment` (compute + static free-tier admission)                                                                            | whole env                                                            | demand counts **hosted parts only**. A customer's own cluster costs us nothing                                                                                                                                                                                                                                                                                                                |
| `handlers/deploy/rpc_record_bundle.go:333`                                                                                                                                     | refuses LOCAL                                                        | refuses `!Promotable`                                                                                                                                                                                                                                                                                                                                                                         |
| `handlers/deploy/rpc_restart_workload.go:78`                                                                                                                                   | refuses unplaced env                                                 | per deployment: restart a hosted part. A cluster workload is refused naming its actuator                                                                                                                                                                                                                                                                                                      |
| `deploystore/converge_environment.go:217` `forge_applies = kind = 'self_managed' AND hub_converged IS NOT TRUE`                                                                |                                                                      | `hub_converged IS NOT TRUE` over the **cluster units** only. Hosted parts always converge through the hosted tree                                                                                                                                                                                                                                                                             |
| `reconcilestore/store.go:133` lease allowlist                                                                                                                                  | `kind IN (persistent, preview) OR (self_managed AND bound)`          | `namespace IS NOT NULL OR EXISTS(binding) OR due hub intent`. This is still an allowlist over facts: a new kind of fact is invisible until someone adds it                                                                                                                                                                                                                                    |
| `deploystore/imports.go:406` `importableKind`                                                                                                                                  | refuses import into persistent/preview ("history was always hosted") | refuses import into an env **that has platform-recorded promotions** (`importConflicts` already finds them). An env that has only ever been forge-applied can import, whatever its placements now are                                                                                                                                                                                         |
| `deploystore/store.go:888` `IsDeployTarget`                                                                                                                                    |                                                                      | `Promotable`                                                                                                                                                                                                                                                                                                                                                                                  |
| `storeaccess/access.go:330`, `cpsecrets/sync.go:18`, `identityreconcile/directory.go:38`, `appdb/appdbstore/store.go:59` `declaredKinds`                                       | `kind IN (persistent, preview, self_managed)`                        | `secret_posture = 'write_only'`                                                                                                                                                                                                                                                                                                                                                               |
| `alerting/store.go:64` `ConvergedKinds`, `alerting/autopilot.go:366`                                                                                                           | persistent + self_managed                                            | `lifetime = 'persistent' AND secret_posture = 'write_only'`                                                                                                                                                                                                                                                                                                                                   |
| `alerting/builtin.go:182` platform builtin alerts only when `kind = persistent`                                                                                                |                                                                      | platform builtins attach to **hosted parts** (they read platform-side telemetry)                                                                                                                                                                                                                                                                                                              |
| `hostedhttp/store.go:18`, `app/hosted_http_grants.go:26`                                                                                                                       | `kind IN (persistent, preview)`                                      | `HasHostedSlot`                                                                                                                                                                                                                                                                                                                                                                               |
| `app/secret_store_authz.go:84,138`, `secretstore/service.go:449` (audit metadata)                                                                                              | `Local = kind == local`                                              | `SecretsReadable`. Audit rows record `secret_posture`                                                                                                                                                                                                                                                                                                                                         |
| `appdb/service.go:158` `effectivePolicy`                                                                                                                                       | preview/local are scratch                                            | `Scratch(env)`                                                                                                                                                                                                                                                                                                                                                                                |
| `deploy/ledger_import.go:119`                                                                                                                                                  | "historical environments import as SELF_MANAGED"                     | import as `persistent` / `write_only`                                                                                                                                                                                                                                                                                                                                                         |
| reliant console (product UI): `services/forge/register.ts:94`, `cloudEnvs.ts:187`, `components/Forge/Workloads/WorkloadPages.tsx:42,88`, `services/forge/secretSurface.ts:142` | env-level kind picks badges and the secret sentence                  | each workload row shows its own placement ("hosted" / "your cluster prod-k8s" / "your bucket"). The secret sentence is chosen **per secret from its consumers' placements** (the shape already lists `declared_by` workloads). A secret read only by cluster workloads gets the External Secrets sentence. One read by hosted parts gets "fetched at deploy time". One read by both gets both |

## 5. Control-plane API and schema changes

All migrations are forward-only, created with `make migration`, and carry forge's migration-safety marker. They
use expand → contract across two control-plane releases. That is the deploy order of our own fleet (the release
running during the rollout must survive the migration), not compatibility owed to anyone.

### 5.1 Expand (release N)

1. `ALTER TABLE deploy_environments ADD COLUMN lifetime TEXT, ADD COLUMN secret_posture TEXT`. The backfill is
   lossless:

   | old kind     | lifetime   | secret_posture | hosted slot     |
   | ------------ | ---------- | -------------- | --------------- |
   | persistent   | persistent | write_only     | kept            |
   | preview      | preview    | write_only     | kept            |
   | self_managed | persistent | write_only     | NULL (as today) |
   | local        | persistent | readable       | NULL (as today) |

   Then add `NOT NULL` and CHECKs (`lifetime IN (…)`, `secret_posture IN (…)`, `(lifetime = 'preview') =
(expires_at IS NOT NULL)`).

2. Replace `ck_cp_deploy_envs_placement` with invariants 1 and 3 over the new columns. `kind` stays, written for
   the old reader. It is no longer constrained against placement, so a row can be `self_managed` with a
   namespace, and the old release never reads that combination (it is created only by the new ensure).
3. Replace `deploy_refuse_unplaced_environment()` with one that tests `namespace IS NULL` (deployments) and
   `secret_posture = 'readable'` (promotions). It **raises the same constraint names** the running release
   classifies (`ck_cp_deploy_local_env_not_a_target`,
   `ck_cp_deploy_self_managed_env_not_placed`). This is the error-contract expand trick
   `20261002154802` already used.
4. Add trigger `trg_cp_deploy_envs_posture_ratchet`. It refuses `write_only → readable`, and it sets
   `write_only` when a hosted slot is allocated, a binding is inserted, or a promotion is inserted.
5. Ensure ignores a kind mismatch and stops refusing it. It derives the hosted slot from deployments. Every
   reader in §4 switches to the predicates.

### 5.2 Wire (proto, `controlplane/v1/deploy.proto` + `services/deploy/v1/deploy.proto`)

Follow the `proto-breaking` discipline: deprecate first, then reserve the tags.

- `DeployEnvironmentKind` and every `kind` field (`controlplane/v1/deploy.proto:292,426`,
  `services/deploy/v1/deploy.proto:845,1909`) are marked `deprecated`, ignored on input, and still populated on
  output in release N (derived from the new columns, for the old reliant console). They are removed and their
  tags `reserved` in N+1.
- New on `DeployEnvironment`: `lifetime` (enum), `secret_posture` (enum), `hosted_slot` (`namespace`,
  `host_cluster_id`, present or absent), and `placements` (per workload of the **current** release: name,
  runtime, cluster, actuator, verdict). The last one is derived at read time from the current promotion's
  bundle shape plus the evidence tables, never stored.
- `EnsureDeployEnvironmentRequest.spec`: `kind` deprecated. `lifetime` is new and required (UNSPECIFIED is
  refused, for the same reason `environmentKindFromProto` refuses it today).
- `EnsureDeploymentResponse` gains `hosted_slot_allocated` and `secret_posture_changed`, so forge can print the
  one-time notices.

### 5.3 Contract (release N+1)

Drop `deploy_environments.kind`, `ck_cp_deploy_envs_kind`, and the two legacy constraint names in the trigger.
Reserve the proto tags. Drop the `--kind` columns from internal-console views.

### 5.4 Sealed history keeps its bytes

Shapes are stored in `deploy_bundles` and `deploy_environments.declared_shape`, and a bundle's shape is bound to
its config digest. **Sealed bundles are never rewritten**: a `jsonb - 'kind'` backfill would break digest
verification of every historical bundle. Instead `release.Shape`'s decoder accepts and discards a legacy `kind`
key forever. This is reading content-addressed history, not client compatibility. `declared_shape` is not
digest-bound and is rewritten in the expand migration.

## 6. One release, one deploy: dependency ordering

### 6.1 Today

forge applies the local part, then publishes the hosted group (`deploy.go:1611`). For `prod`, this happens to
be "servers before the SPA", but only because the API is `OnCluster` and the SPA is `OnHosted`. A hosted API
with an `OnBucket` frontend would run in the wrong order. `DeliveryUnit.after` (`kcl/schema.k:5939`) orders
units for the Flux path only ("Direct apply ignores units").

### 6.2 The rule

The deploy is a DAG over **placement nodes**. A node is one item: a workload, frontend, database or manifests
group. Edges come only from declarations:

1. **`release_dependencies`**, which workloads (`kcl/workload.k:381`), frontends (`kcl/schema.k:3302`) and
   manifests groups already carry. `{peer: contract}` says "I consume `peer`'s contract `contract`", so `peer`
   must be **Ready at the new release** before this item changes. This is the claim the release-cohort
   machinery already reads (`release_compose_declaration.go`). The same edge decides both "must change
   together" and, **across placements only**, "goes first". Inside one placement it orders nothing: services
   that share a protocol name each other (reliant's api-server and daemon-gateway both declare
   `reliant/api-gateway-nats/v1`), and reading that as order made every such pair a cycle. That refusal stopped
   control-plane v1.7.85 after its promotion was recorded. A peer that is `build-only` (an image the release
   carries and something else runs) is never deployed, so it orders nothing either.
2. **`DeliveryUnit.after`** between units, now honored by direct apply and the hosted publish as well as by Flux.
   "Direct apply ignores units" is deleted.
3. **No declaration means no order guess, with one conservative default.** A _frontend_ with
   `release_dependencies` absent (which that field already defines as "unknown") waits for every server workload
   in the same release, whatever their placement. A workload with it absent gets no edge, which is today's
   behavior. An explicit `{}` means independent.

There is no fixed tier ("cluster before hosted"). The local-then-hosted order becomes just the topological order
when no edge says otherwise. Nodes with no path between them run concurrently. A cycle through placements (a
hosted part and a cluster part each waiting for the other) is a render error that names it.

### 6.3 Who honors the DAG

The DAG is computed at render time and recorded in the bundle beside the tree list (`trees`, `cluster_paths`
already exist), so every actuator reads the same order:

- **forge** (direct apply / `applier = "forge"`) walks it. It applies a cluster node, waits for it to be Ready,
  and then publishes or awaits the hosted nodes that depend on it. The hosted readiness wait already confirms the
  platform applied _this_ bundle (`HostedProvider.RecordBundle` digest).
- **The hosted converger** holds a hosted node whose predecessors are not yet Ready **at this bundle**,
  reading the predecessors' evidence (§4, live-bundle union). So a promote from the console, where no forge
  process drives the order, still ships the SPA after its API.
- **The hub** already orders units with `dependsOn`. Hosted ↔ hub edges are honored by the converger's hold.

A node that fails stops its dependents, and the deploy reports one verdict:
`degraded: reliant-web held (reliant-api-server not Ready at bundle sha256:…)`. Nodes that do not depend on the
failed one finish. The env verdict is the worst placement verdict.

## 7. Edge cases

1. **Frontend-only env (pure hosted).** It gets a hosted slot on its first deploy and is `write_only`, with no
   bindings. Behavior is identical to today's persistent static-site env. `forge project new --kind site`
   projects are unaffected except for the `forge env status --json` field rename.
2. **Cluster-only env.** It never gets a hosted slot. It is leased by the reconciler only if bound (as today),
   and live comes from convergences/applies. Behavior is identical to today's self_managed. control-plane prod
   before the merge is this case.
3. **Local dev (`forge env up`).** An env whose placements are all machine-local is `readable` and not
   promotable, exactly like LOCAL today. `forge env up` launches the machine-local placements. In a mixed env it
   builds and deploys the rest, as `forge env up <env>` already does. Binding a dev env's one workload to
   `OnHosted` ratchets it to `write_only` (§7.6) instead of failing with "delete and re-create".
4. **Existing customers, either kind.** The §5.1 backfill is lossless and needs no action. A former
   self_managed customer can add a hosted site to the same env. A former persistent customer can add an
   `OnCluster` workload once they bind a cluster. Nothing has to be recreated.
5. **Moving one item between placements across releases** (`OnHosted` → `OnCluster`). The plan already shows a
   `RuntimeChange`. Stateless items are make-before-break: the new placement must be Ready before the old one is
   retired, so the DAG gets a synthetic edge. A database or any item holding data is a stop-class finding (data
   does not move with a runtime flip).
6. **The secret-posture ratchet.** When it fires, the response says so and forge prints: "env `dev` now holds a
   hosted part, so its secrets are write-only from now on. Values set before this point may have been pulled to
   developer machines; rotate any that a deployed workload will use." The ratchet is automatic because it only
   ever restricts. Decision D2 asks whether it should instead need `--allow-seal`.
7. **Preview env × cluster placement.** A preview is reaped. The platform can retract hosted parts and
   hub-converged units (`hubintent` teardown), but nothing reaps a forge-applied cluster part. Previews therefore
   refuse forge-applied cluster placements, with a refusal that names the workload (D4). This is a rule about one
   lifetime × one actuator, not an env mode.
8. **Deleting a mixed env.** Teardown runs per placement, in reverse DAG order. The platform tears down hosted
   parts, the hub retracts its units, and forge-applied parts are reported as "left running on prod-k8s; run
   `forge env down`". The env row is soft-deleted only after every platform-owned placement confirms.
9. **Custom domains.** A binding's target resolves to one item. If it is hosted, the edge routes to it. If it
   runs on the customer's cluster, the binding is refused ("the platform edge does not front your cluster; use
   your ingress"). The refusal is per target, not per env.
10. **Free tier and billing.** Static-site and compute demand count hosted parts only (§4). Today a
    self-managed org's first hosted site needed a second env to exist at all, so this changes no price.
11. **Observability.** `forge.Observability` stays env-wide. Hosted parts get platform builtin alerts and
    cluster parts get what they declare. prod-web's `signals.replay = True` must move into prod's
    `Observability` (§8).

## 8. Migrating control-plane's split: prod-web → prod

**Facts this rests on (verified):** releases are org-scoped (`uq_cp_deploy_releases_org_version`), so prod and
prod-web already share one release namespace. Promotions, bundles, applies and deployments are env-scoped. A
static site's content lives at `sites/<org>/<env-id>/<site>/live` (`operators/staticsite/layout.go:30`).
`app.reliantlabs.io` reaches it through `custom_domain_bindings`, and rebinding is "an UPDATE of this row …
a single atomic operation" (`00105_domains_and_bindings.up.sql:162`). Publishing is level-triggered and tracked
by `published_binding_at` (migration `20261006170000_custom_domains_published_binding`).

### 8.1 Ledger and history: archive and link, do not interleave

prod's promotion chain is its CAS basis: `--expect-current`, the plan basis, and the "current promotion" that
every guard reads. Rewriting prod-web's promotions into prod would put SPA-only releases into that chain, so prod's
"current release" would briefly be one that carries no API. **So prod-web's rows are not moved.** The expand
migration adds `deploy_environments.merged_into_environment_id`. Once prod serves the SPA, prod-web is
soft-deleted with that link set. History views (`forge env releases list prod`, the console's Live timeline) union
the absorbed env's promotions, labeled `reliant-web (from prod-web)`. Audit keeps every original row. D1 offers
the rewrite alternative.

### 8.2 Steps (each is reversible until step 7)

1. **Ship forge + control-plane expand** (§10 P1–P3). prod and prod-web keep deploying as they do now. Nothing
   in either env changes.
2. **Declare the SPA in prod.** `deploy/kcl/prod/main.k` adds `frontends = [rw.reliant_web]` and carries
   prod-web's `observability.signals.replay = True`. `reliant-web` declares `release_dependencies =
{"reliant-api-server": "<contract>"}` (and admin-server, if it calls it), so it ships after the API. prod-web
   stays declared.
3. **Cut and promote prod** with `scripts/cut-release.sh --promote prod` as usual. One release carries the API
   and the SPA. The first hosted ensure gives prod a hosted slot and its StaticSite publishes to
   `sites/<org>/<prod-id>/reliant-web/live`, the **same bucket, static origin and CDN**, at its own platform
   hostname. `app.reliantlabs.io` still points at prod-web, so users are untouched. That release's
   `promote-web` stage still deploys prod-web from the same pin, so both copies are the same build.
4. **Verify the new copy before it takes traffic.** Run `check-web-version.sh` against prod's platform hostname,
   and diff `config.js` on that hostname with the one `app.reliantlabs.io` serves: same keys, same mode
   selectors. This step is required, not advisory. #1284's guard compares against the env's own current release.
   On prod's first SPA-bearing release that has no StaticSite, so the guard skips by design. Its general fix
   comes from the §8.1 link: the guard's baseline for `(prod, reliant-web)` falls back to the same-named site in
   any env merged into prod. Until that lands, this step is the guard.
5. **Rebind.** `forge domain bind app.reliantlabs.io --env prod --target reliant-web` updates the one binding
   row. A rebind withdraws nothing. `domainconverge.publish` swaps the route only after the new binding has been
   evaluated (`edgeFresh`), and then `published_binding_at` advances. Until then the old prefix keeps serving.
   Both prefixes hold byte-identical builds, so a request served by either is correct. The edge certificate is
   per hostname (`Edge.Issue(hostname)`), not per binding, so it is not re-issued. Then confirm the published binding and run
   `check-web-version.sh` (it reads `app.reliantlabs.io`). **Rollback:** bind back to `--env prod-web`, which is
   the same single-row update.
6. **Soak** for one full release cycle, with prod-web still deployed and still the rollback target.
7. **Retire prod-web.** Remove the `promote-web` stage from `cut-release.sh` (the `forge env deploy prod`
   already ships the SPA). Keep `check-web-version.sh` after the promote. Delete `deploy/kcl/prod-web/` and
   rewrite `lib/reliant_web.k`'s header (it says "one env cannot be both"). Move
   `TestProdReliantWebBuildCarriesReleaseIdentity` and the hosted-only placement test onto prod. Then
   `forge env delete prod-web`, which tears down its StaticSite and prefix and sets `merged_into_environment_id`.
   During steps 3–7 the org temporarily has two static sites; it is our own billed org.

### 8.3 Guards across the mixed release

- **`--expect-current` / native snapshot preflight** (`cut-release.sh:900–915`): unchanged. There is one chain,
  prod's. The separate unguarded prod-web deploy goes away, and with it the hotfix-branch failure mode of
  2026-10-24.
- **#1284 runtime-config key-drop refusal**: it runs on prod's promote over prod's StaticSite. After step 7 it
  protects the SPA on the same promote that ships the API, which is strictly better than today. The baseline
  fallback is in §8.2 step 4.
- **Migration manifests** (`release_migrations.go`): unaffected. The SPA carries no migration set, and
  prod-web's history is not in prod's applied-release list (it was never there, and §8.1 does not add it).
- **Pin guards** (`pin-sibling.sh`, `pin-drift.sh`, `check-pin-ancestry.sh`, `check-forge-coherence.sh`,
  `TestReliantWebGitSourceMatchesGoMod`): they read the GitSource in `lib/reliant_web.k`, which does not move.

## 9. Alternatives, and the devil's advocate

**A. Keep splitting envs (status quo).** It needs no platform change. Each split costs two ledgers, two CAS
bases, two plan approvals, ordering in shell scripts, domain bindings and secrets spread across envs, and one
outage so far. Every customer with a cluster and a site would rediscover it. Rejected.

**B. Env groups.** A "group" promotes several envs together in a declared order, and kinds stay per env. The
schema change is smaller. But it keeps two ledgers under a third object, keeps the user choosing an env per
placement class, and still needs every guard to work across a group. It makes the split official instead of
removing it. Rejected.

**C. Make kind mutable** (self_managed → persistent when a hosted part appears). This is the cheapest unblock,
about a day. But it changes what "persistent" claims about prod: the whole env becomes platform-actuated. Bundle
admission would then check the cluster objects against the hosted namespace and refuse them (`bundle_admission.go`
has one destination per env). `readPlacedLiveBundle` would ignore the cluster convergences, `audit_prereqs`
would skip cluster prereqs, and `hubUnitsConvergeable` would still refuse. It moves the all-or-nothing from
"immutable" to "flip the whole env". Rejected. It is also the trap to avoid as an interim step: it makes the
bugs live in prod.

**D. A fifth kind, `mixed`.** This is a bitset of placements in disguise. Every reader would need a
`mixed` branch that asks the per-workload question anyway. Rejected.

**E. Per-workload placement (chosen).** Each service declares where it is served, which is the owner's
direction. The env keeps only what is truly env-wide.

Against E:

- _"Kind was one cheap column; now every reader composes facts."_ The facts are already stored (namespace,
  bindings, deployment rows, evidence tables), and §2.3's predicates are single-column or `EXISTS` tests. The
  lease query gets no slower. The real cost is touching ~35 call sites once (§3–4), and that list is complete.
- _"Immutability was a security property."_ It was, for exactly one fact: readable secrets never come back.
  The monotonic `secret_posture` plus invariant 1 keeps it in the schema, enforced by trigger, and so stronger
  than application code.
- _"A mixed deploy can half-succeed."_ So could the split, but invisibly, across two ledgers and a shell
  script. Now one promotion has per-placement verdicts and a DAG that holds dependents. Half-success becomes a
  named `degraded` state instead of a skew.
- _"Admission per cluster path widens the attack surface."_ It narrows it. Today a self-managed env's whole
  bundle takes the isolated path. Per-path dispatch applies hosted rules to exactly the hosted objects and
  refuses unknown paths (fail closed). Its test needs a bundle that smuggles a hosted-path object into a
  cluster env, and must prove it is refused.
- _"Reusing `release_dependencies` for order conflates cohort and sequence."_ Both claim "I consume the
  peer's contract". An edge that orders without that claim would be ordering on a false premise. If a real
  ordering-only need appears, `DeliveryUnit.after` already expresses it at unit granularity.
- _"A lazily allocated namespace is never released if the hosted parts go away."_ Accepted: it is one
  namespace per env, freed on env delete, and re-allocation churn would be worse.
- _"Our own prod now depends on the platform to place part of itself."_ prod-web already did. The API stays
  forge-applied on our clusters; only the SPA is platform-placed, which is today's blast radius.

## 10. Implementation sequence (after sign-off)

| Phase | Repo                  | Content                                                                                                                                                                                                                         | Gate                                                                                                                                                                                      |
| ----- | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| P1    | control-plane         | §5.1 expand migration + predicates + per-path admission + live-bundle union + lazy hosted slot + posture ratchet. Proto fields deprecated, new fields added (§5.2)                                                              | `task ci`. Integration tests: a self_managed row gains a hosted deployment. A smuggled hosted-path object is refused. The posture never un-ratchets. The lease allowlist covers each fact |
| P2    | forge                 | delete `EnvKind`/`Shape.Kind`/`KindChanged`; `envPlacements`; ensure sends lifetime; §3 fixes (`audit_prereqs`, lifecycle verbs, hub + hosted); the deploy DAG (§6) and its record in the bundle; tolerant legacy-`kind` decode | `task test` + e2e. A fixture env with `OnCluster` API + `OnHosted` frontend renders one bundle with a DAG edge. The PR title carries `!` (status JSON)                                    |
| P3    | control-plane         | the hosted converger honors the bundle DAG; `merged_into_environment_id` + history union; #1284 baseline fallback; pin forge P2                                                                                                 | `task ci`                                                                                                                                                                                 |
| P4    | reliant               | console shows per-workload placement and per-secret delivery sentences; stops reading `kind`                                                                                                                                    | `make test-affected`                                                                                                                                                                      |
| P5    | control-plane (ops)   | §8.2 steps 2–6 in prod                                                                                                                                                                                                          | the step-4 checks; step 5 rollback rehearsed against `dev`'s own site first                                                                                                               |
| P6    | control-plane + forge | contract: drop `kind`, reserve tags (§5.3); §8.2 step 7                                                                                                                                                                         | `task ci:full`                                                                                                                                                                            |

## 11. Decisions for the owner

| #   | Question                              | Options                                                                                                                               | Recommendation                                                                                                  |
| --- | ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| D1  | prod-web's history                    | (a) archive prod-web with `merged_into_environment_id`, union in history views; (b) rewrite its promotions/bundles into prod          | **(a)**: (b) puts SPA-only releases into prod's CAS chain and makes "prod's current release" lie for those rows |
| D2  | secret-posture ratchet                | (a) automatic, with a loud notice + rotation advice; (b) refuse until `--allow-seal`                                                  | **(a)**: it only ever restricts, and a refusal recreates the "delete and re-create" wall in a smaller form      |
| D3  | source of ordering edges              | (a) `release_dependencies` + `DeliveryUnit.after`, conservative default for frontends; (b) a new `after = [...]` on Workload/Frontend | **(a)**: one declaration of "I depend on X", already read by the release-cohort machinery                       |
| D4  | preview × forge-applied cluster parts | (a) refuse, naming the workload; (b) allow and leave them for the author to clean up                                                  | **(a)**: an unreaped preview is an unbounded bill on the customer's cluster                                     |
| D5  | the old `kind` on the wire            | (a) deprecate in N, reserve in N+1; (b) remove in one release                                                                         | **(a)**: it is free, and keeps our own fleet's rolling deploy from breaking a mid-rollout reliant console       |
