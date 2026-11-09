# What Forge gives you

A living catalog of what Forge does for a project, written for the people who
build Forge first and for anyone who later turns it into marketing or
landing-page copy. It is not a tutorial: [getting-started](getting-started.md)
walks the first project and [concepts](concepts.md) explains why Forge is
shaped the way it is.

Every entry says three things: what the benefit is, which bug or piece of toil
it removes, and how you use it (a command, a KCL field, a marker, a skill). The
last clause is the proof: it names where the behaviour is defined, so a claim
here can be checked in a minute and a stale one can be spotted.

**Source of truth.** Where a number or a list lives in the binary, this page
points at it rather than copying it. The command surface, every lint lane and
every `// forge:*` marker print from `forge project capabilities`; every
library prints from `forge project libraries`; every skill prints from
`forge skill list`. When this page and one of those disagree, the binary is
right and this page is the bug.

**Status words.** "Enforced" means something refuses the action. "Recorded"
means Forge writes the declaration down and nothing refuses yet. "In progress"
means designed but not shipped. The page uses these words deliberately, because
a catalog that blurs them is marketing. Written against forge `v1.2.x`.

Sections:

1. [Production from day 0](#1-production-from-day-0)
2. [Releases, promotion and release health](#2-releases-promotion-and-release-health)
3. [Linters and conventions](#3-linters-and-conventions)
4. [Observability](#4-observability)
5. [Auth and identity](#5-auth-and-identity)
6. [Secrets](#6-secrets)
7. [Data](#7-data)
8. [APIs and frontends](#8-apis-and-frontends)
9. [The dev loop](#9-the-dev-loop)
10. [Testing and CI](#10-testing-and-ci)
11. [Workers, jobs, operators, CLIs and mobile](#11-workers-jobs-operators-clis-and-mobile)
12. [LLM integration](#12-llm-integration)
13. [Agents: forge as a surface for LLMs](#13-agents-forge-as-a-surface-for-llms)
14. [Maintaining this page](#maintaining-this-page)

---

## 1. Production from day 0

What `forge project new` scaffolds on the first commit is what runs in
production, configured differently. There is no toy mode that has to be
replaced before shipping. The entries here are the properties every scaffolded
service has without anyone asking for them.

- **A request is one span, one metric sample and one log record at the edge.**
  The RPC interceptor chain (`serverkit.NewChain`) records all three for every
  call, adopts or starts a W3C trace, and attaches the request id. This removes
  the "we added tracing later and half the requests have no trace" class of
  work. Use: nothing to do; read it with `forge obs trace`. Source:
  `pkg/observe`, skill `observability/component-chain`.

- **Every component-to-component call is instrumented by default.** Any package
  whose constructor returns its contract gets a generated
  `middleware_gen.go` decorator routed through an owned `observe_chain.go`
  seam, and the ORM registers a `bunotel` hook so each query is a child span.
  Internal calls therefore show up in traces without a hand-written span. Use:
  opt a package out with `// forge:no-observe`. Source: skill
  `observability/component-chain`, `pkg/orm`.

- **Errors never leak internals, and the fault decides the log level.** The
  canonical service-error to Connect-error mapping is `pkg/svcerr`; a handler
  returns `svcerr.Wrap(err)` and a driver's SQL, a DSN or an internal host never
  reaches the caller. `svcerr.Classify` sets each record's `error_class`
  (`server`, `user`, `canceled`), so ERROR means "we must act" and alerts,
  forwarders and post-deploy gates can trust it. Use: `svcerr.Wrap`,
  `svcerr.WithReason`, `svcerr.WithClass`. Lint backstops:
  `forgeconv-handler-connect-error`, `forgeconv-unwrapped-domain-error`,
  `forgeconv-no-handler-error-mapping`. Source: `pkg/svcerr`, skill
  `observability`.

- **Server faults carry a bounded stack and a stable fingerprint.** Internal,
  Unknown, DataLoss and Unavailable errors capture a capped stack that lands
  in `exception.stacktrace`, and grouping uses a fingerprint that folds in the
  normalized cause (the SQLSTATE, the refused dial target) so one
  "Transaction failed" does not become six different outages in one issue.
  Use: `forge obs issues`. Source: skill `observability/error-stacks`.

- **Health probes and graceful shutdown are written into the spec.** A service
  gets `/readyz` and `/healthz` on its http port, and the pod's grace period is
  derived from the drain settings (`PRE_STOP_DELAY + SHUTDOWN_TIMEOUT` + 5s),
  so a rollout never SIGKILLs a pod mid-drain. A replica that is draining
  reports its in-flight calls as `canceled`, not as errors. Use: nothing to do;
  `forge doctor` checks probes, resource requests and limits across every env.
  Source: `pkg/serverkit`, `pkg/lifecyclekit`, skill `deploy/model`.

- **Auth fails closed, per rpc.** An RPC needs a valid credential unless its
  proto says otherwise. Use: `option (forge.v1.method) = { auth_required: false }`
  publishes exactly one rpc, and the setting is projected into
  `pkg/middleware/procedures_gen.go`. See [section 5](#5-auth-and-identity).

- **Request limits and edge hygiene are on.** Connect payload caps, CORS,
  security headers and request-id middleware ship in the chain, and
  `forge doctor` checks the payload caps, probes and resource limits of every
  declared env with nothing running. Use: `forge doctor`. Source:
  `pkg/middleware`, `pkg/serverkit`.

- **Outbound calls cannot reach what they must not.** `pkg/netguard` refuses
  connections to loopback, private, link-local, cloud-metadata and in-cluster
  destinations for callers that picked the destination (webhook targets,
  fetch-by-URL). This removes the SSRF review for every outbound fetch. Use:
  `forge project libraries netguard`.

- **Secrets, config and identity are typed, not read from the environment by
  hand.** Config is a proto message projected into KCL and into a typed Go
  struct; `forgeconv-no-getenv` and the typed-access guardrail catch a stray
  `os.Getenv`. See [section 6](#6-secrets) and
  [section 3](#3-linters-and-conventions).

- **Migrations that never wait unboundedly.** A migration never waits for a lock without a limit
  (`lock_timeout` 5s, `statement_timeout` 30m, five attempts with backoff), so
  one slow reader plus one `ALTER TABLE` is not an outage. See
  [section 7](#7-data).

## 2. Releases, promotion and release health

The release model is the part of Forge that most reduces deploy-day toil: a
release is built once, recorded as an immutable content-addressed set of
artifacts, and promoted between environments by that record, so what ran in
staging is the thing that runs in prod.

### Build once, promote by digest

- **A release is immutable and content-addressed.** `forge env build <env>
--release <v>` records one artifact set under a version label, and image tags
  are the commit SHA and pinned to a digest on deploy. This removes "staging
  passed but prod rebuilt something slightly different". Use:
  `forge env deploy prod --from staging` deploys exactly what staging runs (same
  control plane only; `--from-promotion` pins the source promotion, and if the
  source moved the deploy exits 3 `source_moved`). Source: `pkg/release`, skills `deploy/release`,
  `deploy/build`.

- **A promotion is an append-only event.** Binding an env to a release is one
  ledger entry, never an edit, so `forge env releases list|show|at` can answer
  "what was live at 14:05, and what changed" from the record: commits and PRs,
  the shape diff against the previous release, migrations, gates and health.
  Use: `forge env releases at prod 2026-10-21T14:05:00Z`, or `at` a trace id.
  Source: `pkg/release`, skill `observability/query`.

- **The plan is printed before anything is written.** `forge env deploy` shows
  what changes (objects added, changed, removed, image digests moved) and
  stops for confirmation; with no terminal and no `--yes` it exits 5 having
  built, pushed and cut but promoted nothing. `--plan-only` then
  `--approve <digest>` makes a two-stage pipeline where a plan that changed
  between stages is refused (exit 3 `plan_stale`) rather than re-approved blind.
  Stop-class findings (`stateful_deletion`, `lb_identity_change`) need
  `--acknowledge-destructive <code>` even with `--yes`, so the flag hard-coded
  in CI cannot pre-approve a future data loss. Use: `forge env deploy <env>
[--plan-only|--approve|--acknowledge-destructive]`. Source: skill
  `deploy/plan`.

- **The plan diffs against the last applied bundle.** Field paths name list items by `name`
  and never carry a value, so the plan is safe to print into CI logs. Source:
  skill `deploy/plan`.

### The promotion ledger and compare-and-set

- **Every promote is a compare-and-set against the promotion the plan read.**
  A row lock orders two writers but does not stop a writer acting on a stale
  read: CI job A plans "prod is on v5, moving to v6", waits for approval, a
  human hotfixes prod to v5.1 meanwhile, and job A then writes v6 over the
  hotfix. With CAS the write asserts "prod is still on the promotion I planned
  from" and job A fails with exit 3 instead. The expectation is a promotion
  id, not a version, because v1 to v2 to v1 makes "current is v1" ambiguous. Use:
  `forge env deploy <env> --expect-current <promotion-id>` (the default is the
  one the plan read), `--expect-current unbound` or `--expect-unbound` for a
  never-promoted env. Source: `internal/cli/promote_cas.go`.

- **A rollout in flight is not trampled.** Promoting while the current
  promotion is still rolling out is refused (exit 4); the override is explicit
  and recorded on the new entry. Use: `--supersede`. Source:
  `internal/cli/deploy.go`.

- **Retries are idempotent.** A retry of a promote that already landed reaches
  the requested end state and is a no-op rather than a conflict; only a real
  move is compared. Source: `internal/cli/promote_cas.go`.

- **Exit codes mean one thing each, across every verb.** 0 success, 1 wrong,
  2 could not tell, 3 conflict (someone else moved it; never retry blind),
  4 refused (waiting may help), 5 plan unconfirmed, 6 superseded, 7 queued on
  a person (billing), 8 timed out. A pipeline can branch on them without
  parsing messages. Use: `$?`; the table is in `internal/cli/exitcodes.go`.

- **A release carries the migrations it embeds, and refuses an out-of-order
  one.** The cut records a migration manifest; the cut and the deploy refuse a
  release whose migrations an environment's schema has already passed (or, for
  a shared env, one carrying unmerged migrations), instead of letting the
  migrator find out with crash-looping pods. This is the 2026-10-24 reliant
  incident, encoded. Use: `forge env build <env> --release`, waivers
  `--allow-passed-migrations`, `--allow-unmerged-migrations`; backfill with
  `forge env releases backfill-migrations`. Source: skill `db/release-guard`.

- **A cut that died mid-push resumes.** Images pushed but never recorded are
  picked up from the cut journal by re-running the same build from the same
  clean commit; nothing is rebuilt. Use: `forge env releases interrupted <env>
<v>`. Source: skill `deploy/interrupted-cut`.

- **Ledgers move and verify.** `forge ledger where|show|export|import` says
  which backend holds an env's ledger and lets you move history between
  backends; `forge release verify` proves every artifact a release names
  exists and matches; `forge release bundle` pulls a recorded bundle and diffs
  it against the live clusters. Break-glass when the control plane is down:
  `forge env deploy <env> --offline-ledger [--to previous|<release>]` deploys a
  cached release by its recorded digests and replays the promotion on the next
  contact. Source: skills `deploy/offline-ledger`, `deploy/release`.

- **A frozen env.** An env whose reconcile policy is `pinned` refuses every
  promote with exit 4 `environment_pinned`, so a freeze is a declared state, not
  a convention. Source: `internal/cli/exitcodes.go`.

### Release health tied to telemetry, issues and alerts

- **"Did the deploy work?" is a command with a verdict.** `forge obs check
<env>` compares the window after a promotion with the equal window before it,
  per service, and lists what the release opened: new and regressed issues,
  alerts still firing, workload restarts. Error rate is weighted for tail
  sampling, client-cancelled requests are not errors, and latency is judged per
  endpoint so a window emptied of one fast client does not read as a slowdown.
  Use: `forge obs check prod --wait --json`. Exit 0 PASS, 1 FAIL (a `fail_on`
  finding fired), 2 INCONCLUSIVE or could not look, 8 `--wait` timed out. A
  gate must treat 2 as "not proven good", never as a pass. Source:
  `pkg/releasehealth`, skill `observability/query`.

- **The policy is declared per env and recorded in the release.** Thresholds
  (`window`, `min_requests`, `max_error_rate_increase`, `max_p95_ratio`,
  `new_issue_min_events`) and the failing set (`fail_on`) are
  `forge.ReleaseHealth` on the Bundle, so the console, the CLI and alerting
  judge a release the same way. `--threshold`, `--fail-on` and `--window`
  override for one call, and the document says where each effective value came
  from. `--wait --early-pass 8m` ends the wait with a PASS only once at least 8
  minutes are observed, every judged service has reached its own `min_requests`
  on the new version, and there is no finding, new or regressed issue, firing
  alert or restart. Use: `release_health =
forge.ReleaseHealth { ... }` in `deploy/kcl/<env>/main.k`.

- **The control plane records its own verdict, once per promotion.** For an env
  on a control plane, an evaluator judges every promotion when the window closes
  (plus about two minutes to settle), with the env's declared policy and no
  client overrides, and stores pass, fail or inconclusive append-only. A FAIL
  raises the `ReleaseRegressed` alert, which stays silent on PASS and on
  INCONCLUSIVE (too little traffic to judge). The alert's payload names the
  findings, the release and its commit range. Use: `forge obs check <env>
--release <v>` reproduces the verdict. Source: skills `observability/alerts`,
  `observability/query`.

- **The next env can be gated on the previous one's verdict.**
  `delivery_policy = forge.DeliveryPolicy { health_gate = forge.HealthGate
{ after = ["staging"] } }` makes the control plane refuse a prod promote
  until staging holds a PASS for that release, inside the promotion's
  transaction, so a client cannot skip it. Refusals are distinct:
  `health_gate_pending` (exit 4, window still open; `forge obs check staging
--release R --wait` then retry), `health_gate_unproven` (exit 4),
  `health_gate_regressed` (exit 1). A release prod already ran with a PASS is
  admitted without staging, so a rollback needs no detour. The incident
  override is `--override-health-gate "<why>"`, recorded with who, why and what
  staging showed. Status: enforced by a control plane that advertises
  `shape-health-gate`; recorded only on a self-hosted env. Source: skill
  `deploy/delivery-policy`.

- **From a release to the issues and the commits behind it.** `forge obs issue
<id>` returns the culprit, frames, latest event and trace, events per
  version, the release live when it was last seen and suspect commits from
  local git; `forge obs issues --since-release|--since-deploy` lists what a
  release introduced; `resolve --in next|<version>` ties a fix to a release so
  a later event from an older one is a regression. Use: the `obs issue`
  family. Source: skill `observability/query`.

- **Every `obs` verb answers in one JSON document with the next command.**
  `forge.obs.<verb>/v1` documents carry `ok`, `exit_code`, `scope`, bounded
  lists with `next_page_token`, and `suggested[]`: up to three complete,
  runnable follow-up commands. An agent runs them instead of paraphrasing.
  Source: skill `observability/query`.

- **Check results attach to the promotion as evidence.** `forge lint
--gate-json`, `forge obs check --gate-json` and `forge gate record|list`
  record one check's result (lint, test, smoke, a manual sign-off) against the
  promotion it is about, with who claimed it and when, and `forge env deploy
--gate` reads them. This is evidence, not enforcement: forge refuses nothing on
  a gate's status; what refuses a promotion is the delivery policy below. Source:
  `forge gate --help`.

### What a release must prove (delivery policy)

`forge.DeliveryPolicy` declares per env what a release must prove before it
ships, each control with `off`, `audit` (record and alert, never block) or
`enforce` modes. The honest status, because this is the easiest section to
oversell: **`health_gate` (above), `signing` and `flux_verify` are enforced on
a control plane; `provenance_sbom`, `trusted_builder`, `admission_verify`,
`signed_promotions`, `vuln_gate`, `bundle_kinds` and `approvals` are validated
and recorded in the release shape and the ledger, and nothing refuses a
release because of them yet.** Setting one to `enforce` today is a declaration
of intent. Images are not signed yet; only an env with a control plane gets
its bundles signed (by the platform). The rule behind the defaults: a control
that cannot block a deploy defaults to `enforce`; one that can refuse a deploy
defaults to `audit`, so you read its would-deny verdicts before turning it on.
Use: `delivery_policy = forge.DeliveryPolicy { ... }`. Source: skill
`deploy/delivery-policy`.

### Rollback and the rollback floor

- **Today: roll forward.** There is no rollback command, deliberately: a
  failed deploy leaves the previous ReplicaSet serving, a bad migration is
  stopped by the pre-rollout gate before any workload changes, and recovery is
  a new release. Binding an env to an older release is still an ordinary
  promote, labelled `direction BEHIND` in the plan and the ledger, with
  `--note` to say why. Down migrations are not written, run or accepted
  (`no-down-migration`). Source: skill `deploy/release`.

- **In progress, not shipped: rollback floors.** An internal design (owner
  decisions recorded 2026-11-08, `rollback-floor.md` in the owner's design
  notes, not in this repository) adds a
  per-env constraint, "never deploy anything older than X", where "roll
  forward only from here" is a floor set at the release the env currently runs.
  Floors would be raised automatically by declared floors (a
  `-- forge:breaks-rollback` migration, or a checked-in
  `.forge/rollback-floor/<id>.yaml` for an api, feature or data change),
  enforced by the control plane in the promote transaction as a new
  `below_rollback_floor` refusal and by `forge env floor` verbs, with no
  `--force`: the escape is an audited lift. None of this is in a released
  forge today; do not cite it as a capability until it ships.

### Hosting, domains, static sites and claimable deploys

- **Staging and prod are hosted on Reliant by default.** A new project binds
  them to Reliant hosting: a managed Postgres, the API as one workload, the
  frontend on platform static hosting with a CDN, per-env secrets and custom
  domains, with `dev` on the host. Rebinding to a cluster you operate is a
  one-line change. Use: `forge env deploy staging`, then `forge env deploy prod
--from staging`. Source: skill `deploy/hosting`.

- **Declare a workload once, bind it per env.** A `fw.Workload` is declared in
  `workloads.k`; each env binds it to `OnHost`, `OnCluster`, `OnHosted`,
  `OnCompose` or `BuildOnly`. An env file that restates a workload's command or
  build is a second copy that drifts, so forge does not ask for one. Use: `forge
env render|shape|diff`. Source: skill `deploy/model`.

- **A static site is a full forge project.** A landing page, docs site, SPA or
  PWA gets dev/staging/prod, build-once releases promoted by digest, hosted
  static hosting (the platform owns the bucket, CDN and hostname; one hosted
  static site per org is free, counted across envs), per-env runtime config
  without a rebuild, and browser logs mirrored to disk. Adopt an existing Vite
  or Next.js app in place. Use: `forge project new site --kind site`, or
  `forge project new --in-place --kind site`. Source: skill
  `deploy/static-site`.

- **Custom domains are a control-plane resource.** Claim, bind to one env's
  workload or frontend, verify DNS and unbind without editing spec. Use:
  `forge domain add|bind|verify|ls|show|unbind|rm`. Source: skill
  `deploy/domains`.

- **Deploy with no account.** `forge env deploy <env> --claimable` publishes
  with no signup and prints a live URL plus a claim link that expires in 72
  hours; opening it signs the person up and makes the deploy theirs. It fits one
  static site, or one service with one database, and is refused when signed in.
  Source: skill `deploy/hosting`.

- **Billing never fails a deploy silently.** A deploy that needs billing is
  recorded and queued, exit 7, with the link to fix it, and proceeds on its own
  when the hold clears. A capacity pre-flight runs before building. Use:
  `forge env deploy <env> --wait` to block until live. Source: skill
  `deploy/hosted-capacity`.

- **Lifecycle from the CLI.** `forge env stop|start|delete` operates a hosted
  env (delete shows the id, project, deployment count and bound domains first,
  and refuses while a custom domain is bound), and `forge env list --orphaned`
  lists hosted envs the project no longer declares. Source: skill
  `deploy/lifecycle`.

- **Reconciler-safe by construction.** Flux re-applies the same bytes every
  interval, so a Job with a TTL or an unhashed name would re-run or wedge.
  Forge refuses those objects at bundle build and lints them earlier across
  every env (`flux-job-ttl`, `flux-job-unhashed`, `flux-force-on-data`). Source:
  skills `deploy/flux-safe`, `deploy/flux`.

- **Change one field of a rendered object without forking the template.**
  `Bundle.overrides`, keyed by `Kind/name`, applied at render; conflicts fail
  the render. Bring your own YAML or Helm chart with `forge.Manifests`. Source:
  skills `deploy/overrides`, `deploy/byo-yaml`.

- **Connect a cluster you run.** `forge cluster connect` registers a Kubernetes
  cluster as one env's binding with a scoped ServiceAccount token, and the
  grant is derived from what the hub applies. Source: skill
  `deploy/cluster-connect`.

- **A boot config check before a push.** `forge env build --push`, `--release`
  and `forge env deploy` boot each workload with its rendered env under
  `FORGE_CONFIG_CHECK=1` before pushing, so a missing required config value is
  found before the image is public. Use: `--config-check=gate|warn|off`.
  Source: skill `deploy/config-check`.

- **Declared-env catalog travels with the release.** A released bundle carries
  `declared_envs`, one entry per `deploy/kcl/<env>/main.k`, so the console can
  list envs that never recorded anything, and `forge env status --json` with no
  env prints the whole-project topology (`pkg/topology`). Source: skill
  `deploy/shape`.

## 3. Linters and conventions

`forge lint` is a report-only gate: it writes nothing unless `--fix`, and every
fixer's rewrite is type-checked and taken back if it breaks its package. One
dial grades every forge rule: `lint.rules` in `forge.yaml` maps a rule id to
`error`, `warn` or `off` identically in text, JSON and `--gate-json` output.
Suppressions are by comment (`forge:lint-disable-next-line <rule>: <why>`), a
reason is required, and `forge lint --show-suppressed` lists every suppression
the project declares with file, line and stated reason. Never pipe `forge lint`
through `head`: the verdict is the last line (`--quiet` prints only what
failed). `--scope <path>` lints a slice of a shared checkout.

**Everything here prints from the binary.** `forge project capabilities` lists
every lane (the "analyzers" array, including lanes hidden from `--help`) and
every marker; the per-rule ids below are the ids `lint.rules` accepts, and
`gradableRules()` in `internal/cli/lint/lint_rules.go` is the authoritative list
(a unit test keeps it complete against the rule ids the lane code declares).

### Warn first, gate next release

A rule that joins `forge lint`, or is tightened, is a **warning** for projects
pinned below its `GatesFrom` release, and the finding says so ("new in vX;
gates from vY"). Every gradable rule has a row in
`internal/cli/lint/lint_rollout_table.go`, and
`TestEveryGradableRuleDeclaresItsRollout` fails a rule with no row, so a new
lane cannot land without choosing its date. `lint.rules` wins in both
directions (`{rule: error}` opts in early, `{rule: warn}` extends the grace
period). The verdict line counts the warnings, so a green run never hides that
a pin bump will turn red. A rule that must gate on arrival is the owner's
call, not an author's: it breaks every pinned consumer. Source: skill
`ci/lint-rollout`.

### How to read the tables

Each row is one rule id (the `[rule]` in a finding), the **bug class** it
prevents, and the lane or flag that runs it. "Lane" is the `forge lint` flag
that runs it alone; every lane except the explicitly opt-in ones also runs in a
bare `forge lint`. Rules that are advisory by default say so; `lint.rules` can
raise any of them.

### Data integrity: who may write a column, and what a migration may do

| Rule                                                                      | Bug class it prevents                                                                                                                                                                           | Lane                   |
| ------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------- |
| `forgeconv-read-only-field-unwritten`                                     | A `forge:read-only` column nothing populates ships as the type's zero (a `$0.00` total) with no error anywhere; no constraint, test or log catches it                                           | `--read-only-fields`   |
| `forgeconv-computed-field-unwritten`                                      | Same silent zero for a `forge:computed` field no non-generated Go file assigns; the consequence text is derived from the field's type                                                           | `--computed-fields`    |
| `forgeconv-guarded-field-written`                                         | A scaffolded edit page whose `update_mask` still names a `forge:guards` column writes it raw and bypasses the rpc that owns it                                                                  | `--guarded-fields`     |
| `forgeconv-create-request-nullability`                                    | An entity field and its `Create<Entity>Request` field disagree on `optional`, silently dropping write presence                                                                                  | `--create-nullability` |
| `forgeconv-list-filter-optional`                                          | A List filter without `optional` has no presence, so "no filter" collapses into "filter by zero" and generated filters no-op                                                                    | `--conventions`        |
| `forgeconv-crud-owner-resolver`                                           | An owner-scoping wrapper takes its owner from a different principal than its siblings (a `tenant_id` column scoped by a user id): compiles, runs, returns nothing or someone else's rows        | `--conventions`        |
| `forge-column-markers`                                                    | A misspelled or misplaced `forge:` marker in a `COMMENT ON` statement does nothing and postgres stores it verbatim (`forge:immutible`)                                                          | `--column-markers`     |
| `forge-identity-subject-unreadable`                                       | A `forge:identity-subject` column the read-only role cannot read, so the console never draws the identity link                                                                                  | `--column-markers`     |
| `forge-closed-set`                                                        | A CHECK whose value list drifts from the Go set it mirrors: a value Go grants and the CHECK refuses fails every write of it; the finding carries the forward migration                          | `--closed-sets`        |
| `forge-db-grants-undecided`, `forge-db-grants-unverified`                 | A credential-shaped column (`token`, `password`, `key`, `hash`) that nobody decided is readable by the read-only `forge_ro` role                                                                | `--db-grants`          |
| `forge-fixture-rejected`, `forge-fixture-unverified`                      | A scaffolded CRUD test fixture the current schema rejects (a GENERATED column, a UNIQUE clash, a dangling FK, a later CHECK), found by running each statement in a rolled-back transaction      | `--fixture-drift`      |
| `forge-time-bucket-session-tz`                                            | A two-argument `date_trunc` over a `TIMESTAMPTZ` truncates in the session timezone, so every daily total moves with the deploy host                                                             | `--time-bucketing`     |
| `forgeconv-bun-dollar-placeholder`, `forgeconv-bun-literal-question-mark` | Porting `$1` SQL into bun-formatted SQL, or leaving a literal `?` bun consumes, so a statement fails at runtime or runs against shifted arguments                                               | `--conventions`        |
| `forgeconv-tx-lock-under-serializable`                                    | A `SELECT … FOR UPDATE` or advisory lock inside a SERIALIZABLE or REPEATABLE READ transaction: the snapshot predates the wait, so each contended wait costs a full rollback or drops the update | `--conventions`        |
| `forgeconv-service-dir-consistency`                                       | A proto service name that disagrees with its `proto/services/` directory, so scaffolds and handler directories land where nothing expects them                                                  | `--conventions`        |

### Migration safety

All of these run in `forge lint --migration-safety` and `forge ci
migration-safety`. Every one is gradable through `lint.rules`; the pragma
suppressions are in the migration file itself so the reason lives next to the
SQL.

| Rule                                                                                  | Bug class it prevents                                                                                                                                                                                                           |
| ------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `no-down-migration`                                                                   | A down migration claims to undo a release and cannot: by the time anyone would run it, rows are in the new shape and other services have read them. Forge writes none, runs none, flags any                                     |
| `duplicate-migration-version`                                                         | Two files claiming one version: golang-migrate records one integer per migration, so whichever reached a shared database first leaves the other permanently unapplied                                                           |
| `non-timestamp-migration-version`                                                     | Sequential numbering makes parallel branches collide; a UTC timestamp cannot without two allocations in one second                                                                                                              |
| `migration-version-below-merged-head`                                                 | A new migration numbered at or below the default branch's head is not pending, it is invisible: `up` reports current, CI passes on a fresh database, and every already-migrated database never runs it                          |
| `destructive-change`                                                                  | A `DROP`/`TRUNCATE`-class statement; split into data-preserving steps or allow it by `forge:allow-destructive`                                                                                                                  |
| `unsafe-add-not-null-column`                                                          | `ADD COLUMN … NOT NULL` without a default fails on a populated table; add nullable, backfill, then `SET NOT NULL`                                                                                                               |
| `set-not-null-without-backfill`                                                       | `SET NOT NULL` with no earlier backfill in the migration fails on a populated table                                                                                                                                             |
| `volatile-default`                                                                    | A volatile `DEFAULT` in `ADD COLUMN` can rewrite and lock a populated table                                                                                                                                                     |
| `non-transactional-statement`                                                         | `CREATE INDEX CONCURRENTLY` and kin cannot run in a transaction block (a migration file is one transaction); it must be alone in its file                                                                                       |
| `enum-value-used-before-commit`                                                       | Using an enum value the same file adds fails with SQLSTATE 55P04                                                                                                                                                                |
| `invalid-adopts-directive`                                                            | A `-- forge:adopts` baseline declaration the migrator would refuse at boot on every database                                                                                                                                    |
| `migration-compat-conflict`, `migration-compat-reason`, `migration-compat-undeclared` | A migration that must declare `-- forge:backward-compatible — <why>` or `-- forge:breaks-rollback <why>` and either contradicts itself or says neither; this declaration is what a rollback decision and the release guard read |

### Composition, contracts and Deps (architecture that stays testable)

| Rule                                                                                                      | Bug class it prevents                                                                                                                                                                                                                  | Lane                    |
| --------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------- |
| `forgeconv-internal-package-contract-names`                                                               | A non-canonical `contract.go` (`Service`, `Deps`, `New(Deps) Service`): the generated bootstrap hard-codes those names and would not compile. `forgeconv-contract-names` in an old `lint.rules` still grades it, with a rename warning | `--contract`            |
| `forgeconv-internal-package-missing-contract`                                                             | A component built by hand without `contract.go` is invisible to wiring, mocks, the observability decorator and every reporter                                                                                                          | `--contract`            |
| `forgeconv-deps-are-interfaces`                                                                           | A concrete Deps field defeats the all-mock unit-test surface `New(Deps)` exists for                                                                                                                                                    | `--contract`            |
| `forgeconv-outbound-io-no-rpc`                                                                            | A package marked `// forge:outbound-io` that also serves an inbound handler contradicts its own marker                                                                                                                                 | `--contract`            |
| `forgeconv-deps-surface`                                                                                  | A Deps interface declaring methods the package never uses: the abstraction is wider than the need, so it leaks (autofix: `forge fix narrow-deps`)                                                                                      | `--conventions`         |
| `forgeconv-no-container-deps`                                                                             | A Deps field that is a container (the composition root's types, a locator, `any`): the dependency graph hides in a bag                                                                                                                 | `--conventions`         |
| `forgeconv-deps-no-downcast`                                                                              | A type assertion turning a Deps field back into a concrete type                                                                                                                                                                        | `--conventions`         |
| `forgeconv-no-getenv`                                                                                     | A component reading the process environment itself instead of its typed config                                                                                                                                                         | `--conventions`         |
| `forgeconv-deps-root-config`                                                                              | A Deps field holding the whole root config instead of the component's own block                                                                                                                                                        | `--conventions`         |
| `forgeconv-optional-dep-marker-position`                                                                  | A `// forge:optional-dep` that is misplaced is silently ignored, so `validateDeps` still gates a field the author thinks is optional                                                                                                   | `--conventions`         |
| `forge-optional-deps-guard`                                                                               | An unguarded dereference of an optional dep is a latent nil-pointer panic no construction-time gate can catch                                                                                                                          | `--optional-deps-guard` |
| `forge-config-deps`                                                                                       | A scalar Deps field is configuration, not a collaborator; wiring leaves a typed zero and a TODO forever                                                                                                                                | `--config-deps`         |
| `forge-config-unreachable`                                                                                | A config message no binary and no frontend loads still generates Go, KCL and env projections and is read by nobody                                                                                                                     | `--config-reach`        |
| `forgeconv-config-service-name`                                                                           | A config field bound to `OTEL_SERVICE_NAME` overrides the per-workload name project-wide, so every workload files telemetry as service `unknown`                                                                                       | `--conventions`         |
| `forge-graph-unreachable`                                                                                 | A component or provider no root reaches: no process constructs it                                                                                                                                                                      | `--graph`               |
| `forge-graph-audience`                                                                                    | A listener serving a service through a chain authenticated by another audience: a daemon caller could reach a user service                                                                                                             | `--graph`               |
| `forge-graph-assemble`                                                                                    | A composition graph that does not assemble (the error `generate` would fail with)                                                                                                                                                      | `--graph`               |
| `forgeconv-allocate-port-spacing`                                                                         | Two `allocate_port` bases congruent mod the block size collide at some worktree number, so two listeners bind one port once the stack ceiling is raised                                                                                | `--conventions`         |
| `component-drift/orphan-entry`, `component-drift/undeclared-component`, `component-drift/no-such-command` | `workloads.k` and the code disagree: a component with no entry, an entry with no component, or a workload whose args select a command the binary does not have                                                                         | bare `forge lint`       |
| `forgeconv-handler-file-size`                                                                             | One handler file grown past `lint.handler_file_max_loc`; split per rpc with `forge scaffold handler-file`                                                                                                                              | `--conventions`         |

### API contracts and error handling

| Rule                                 | Bug class it prevents                                                                                                                                                                                                  | Lane                |
| ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- |
| `forgeconv-handler-connect-error`    | `connect.NewError(code, err)` publishes `err.Error()` (a driver's SQL, a DSN, an internal host) to the caller and pins a guessed code over a classified one (autofix: `forge fix connect-errors`)                      | `--conventions`     |
| `forgeconv-unwrapped-domain-error`   | A handler returning a collaborator's error unwrapped puts a domain error on the wire as `500 unknown` and drops the reason header                                                                                      | `--conventions`     |
| `forgeconv-no-handler-error-mapping` | A hand-rolled `toConnectError` switch re-created per service                                                                                                                                                           | `--conventions`     |
| `forgeconv-one-service-per-file`     | A multi-service proto file                                                                                                                                                                                             | `--conventions`     |
| `forgeconv-activity-declared`        | A mutating rpc that does not declare its activity event (or `activity: { none: "<why>" }`) leaves a hole in a user's history; judged from the compiled descriptors, so it cannot disagree with the runtime interceptor | `--conventions`     |
| `forge-proto-markers`                | A misspelled `// forge:` marker in a `.proto` comment is inert and warns nowhere                                                                                                                                       | `--proto-markers`   |
| `forge-proto-options`                | A `(forge.v1.*)` annotation naming a field this binary does not define compiles under buf and is read by nothing                                                                                                       | `--proto-options`   |
| `forge-vendored-proto-drift`         | A vendored `forge.proto` that diverged from the binary's copy: annotations compile, then are ignored                                                                                                                   | `--vendored-protos` |
| `forge-go-markers`                   | A `//forge:` comment in Go that forge does not read, an unknown name or a spelling its parser ignores                                                                                                                  | `--go-markers`      |
| `forgeconv-open-signup`              | A login broker that mounts `POST /auth/register` unconditionally: outside development an unverified address gets a signed-in account, which opens email-bound invitations to takeover (advisory)                       | bare `forge lint`   |
| `forgeconv-identity-methods`         | A `forge.ManagedIdentity` declaring a sign-in method or signup mode the project's own broker does not serve, so the declaration is converged and silently ignored                                                      | bare `forge lint`   |
| `typed-config-guardrail`             | An `os.Getenv` the typed-config forbidigo guard reports (advisory unless `--strict`)                                                                                                                                   | bare `forge lint`   |

### Tracing

| Rule                                    | Bug class it prevents                                                                                                                                                            |
| --------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `forge-tracing-log-without-ctx`         | A log call made where a context is in scope but not passed has no `trace_id` and cannot be joined to anything (autofixed to the `…Context` form by `forge lint --tracing --fix`) |
| `forge-tracing-raw-http-client`         | A hand-built `*http.Client` or `http.Get` cuts the trace at the call; use `observe.NewHTTPClient`                                                                                |
| `forge-tracing-raw-sql-open`            | `sql.Open`/`pgxpool.New` with no tracer drops every query span; use `dbtrace`                                                                                                    |
| `forge-tracing-contract-uninstrumented` | A contract's `New(…)` used without its generated decorator (autofixed to `NewInstrumented`)                                                                                      |

All four run in `--tracing` and are pure AST passes over owned source; they err
toward silence where the code may already be right. A logging wrapper of your
own joins the log rule through `lint.tracing.log_wrappers`. Source: skill
`observability/tracing-lint`.

### Tests

| Rule                                                 | Bug class it prevents                                                                                                                                                                                                                                                                      | Lane            |
| ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------- |
| `forge-slow-test-unmarked`, `forge-slow-test-inputs` | A test that execs `go`, `forge`, `kcl`, `node`, `npm`, `git` or `docker`, drives containers or dials an external host without `testkit.Slow` runs in the blocking `-short` tier, and a file its child process reads is invisible to Go's test cache, so a stale PASS replays after an edit | `--slow-tests`  |
| `forgeconv-handler-tests-use-tdd`                    | Hand-rolled `{name, call}` tables instead of `tdd.RunRPCCases` (codemod: `forge project migrate tdd`)                                                                                                                                                                                      | `--tests`       |
| `forgeconv-frontend-hook-tests`                      | A generated hooks file with no sibling test                                                                                                                                                                                                                                                | `--tests`       |
| `forgeconv-frontend-hook-test-shape`                 | A hook test whose assertions can never pass (a query-shaped test on a mutation hook, or the reverse)                                                                                                                                                                                       | `--conventions` |

### Frontends

| Rule                                                                                                                                                                                                                                                                                                             | Bug class it prevents                                                                                                                                                                                                                                                                                                                                                    | Lane                |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------- |
| `static-export-not-exported`, `static-export-dynamic-segment`, `static-export-route-handler`, `static-export-middleware`, `static-export-server-action`, `static-export-next-headers`, `static-export-dynamic-config`, `static-export-next-image`, `static-export-config-routes`, `static-export-trailing-slash` | Anything a `next build` static export refuses (a `[id]` route without `generateStaticParams`, a server action, `cookies()`, `next/image` without `unoptimized`) is an error with file and line; what the export builds and then silently drops (middleware, rewrites, POST handlers, no `trailingSlash` on a bucket) is a warning, so dev and prod do not diverge unseen | `--static-export`   |
| `forgeconv-frontend-process-env`                                                                                                                                                                                                                                                                                 | Reading `process.env` or `import.meta.env` in frontend source freezes the bundle to the environment it was built for, defeating build-once promotion, and bypasses the typed schema and the secret refusal                                                                                                                                                               | `--conventions`     |
| `forgeconv-frontend-env-forge-owned`                                                                                                                                                                                                                                                                             | A committed dotenv line setting a forge-owned variable (mock API, API URL) drifts from KCL and risks shipping a mock build                                                                                                                                                                                                                                               | `--conventions`     |
| `forgeconv-frontend-stores-no-server-data`                                                                                                                                                                                                                                                                       | A Zustand store importing a generated Connect client creates a second source of truth beside React Query                                                                                                                                                                                                                                                                 | `--frontend-stores` |
| `forge-frontend-lock-drift`                                                                                                                                                                                                                                                                                      | A `package.json` range the lockfile does not record: the state `npm ci` and `pnpm --frozen-lockfile` refuse                                                                                                                                                                                                                                                              | bare `forge lint`   |
| Frontend lint and typecheck                                                                                                                                                                                                                                                                                      | eslint, stylelint and a TypeScript typecheck run for every declared frontend, severity `lint.frontend.typecheck`                                                                                                                                                                                                                                                         | bare `forge lint`   |

### Deploy and secrets

| Rule                                                                                                                                                                                                                                                             | Bug class it prevents                                                                                                                                                                                                                                                                                                                                                                                                                                  | Lane              |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------- |
| `pdb-blocks-drain`                                                                                                                                                                                                                                               | A PodDisruptionBudget that can never allow a disruption blocks every node drain forever (checked over each env's real render, hand-written manifests included)                                                                                                                                                                                                                                                                                         | bare `forge lint` |
| `hosted-image-base/host-bearing-image`                                                                                                                                                                                                                           | An image naming a registry host on a hosted workload, where the platform admits only its own subtree                                                                                                                                                                                                                                                                                                                                                   | bare `forge lint` |
| `flux-job-ttl`, `flux-job-unhashed`, `flux-force-on-data`                                                                                                                                                                                                        | A Job TTL that Flux re-creates every interval, a Job whose name carries no spec hash and so fails every reconcile after its first change, a force-recreate annotation on a kind that holds data                                                                                                                                                                                                                                                        | bare `forge lint` |
| `identity-share-with`                                                                                                                                                                                                                                            | A `ManagedIdentity.share_with` one env's render cannot judge: a missing or chained owner env                                                                                                                                                                                                                                                                                                                                                           | bare `forge lint` |
| `shellbuild-tokens/retired-token`                                                                                                                                                                                                                                | A retired `${TARGETARCH}` token left in a ShellBuild command: the shell resolves it empty, `GOARCH=` builds for the host, and an amd64 cluster silently receives an arm64 binary                                                                                                                                                                                                                                                                       | bare `forge lint` |
| `no-dotenv`                                                                                                                                                                                                                                                      | A `.env` file: injected wholesale into host services so values never have to be declared in KCL, which is how config stops being reproducible                                                                                                                                                                                                                                                                                                          | bare `forge lint` |
| `secrets-cluster-kind`, `secrets-es-policy`, `secrets-key-coverage`, `secrets-unreferenced-key`, `secrets-immutable-pin`, `secrets-out-of-band-reason`, `secrets-single-owner`, `openbao-wildcard-read`, `secrets-escrow-in-cluster`, `secrets-env-not-rendered` | The platform secrets model per env: cluster-scoped ESO kinds the operator disables, an ExternalSecret that would delete the Secret on a store outage, a pod stuck in `CreateContainerConfigError` because no declaration produces a key it reads, a Secret with two owners that fight over keys, a floating read of an immutable store name, an unexplained out-of-band reason, a reasonless OpenBao glob read, an OpenBao escrowed in its own cluster | `--secrets`       |

### Scaffold ownership and the generated tree

| Rule                                                                                                        | Bug class it prevents                                                                                                                                                                                                                                                                                                                                                         | Lane                                              |
| ----------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------- |
| `forge-generated-file-edited`, `forge-generated-file-unverified`                                            | A `Code generated … DO NOT EDIT` file hand-edited after forge wrote it, which the next `generate` silently overwrites                                                                                                                                                                                                                                                         | `--generated-drift`                               |
| `generated-code-ignored`, `machine-local-state-tracked`, `tool-derived-file-tracked`, `forge-state-ignored` | A `.gitignore` the project was born with that ignores generated code (CI fails on `undefined:` symbols that live in ignored files while `verify-generated` is blind to ignored paths), a machine-local file committed, a tool-only generated output committed, or a `.forge/` ignore line that voids the scaffold's re-includes so deleted scaffolds come back in every clone | bare `forge lint`                                 |
| `scaffold-not-customized`                                                                                   | A file still carrying `FORGE_SCAFFOLD` placeholders (a warning: an uncustomized scaffold is pending work, not a defect)                                                                                                                                                                                                                                                       | bare `forge lint`                                 |
| `gen-missing-header`, `gen-missing-source`                                                                  | A `_gen` file that does not claim to be generated, or names no input                                                                                                                                                                                                                                                                                                          | bare `forge lint`                                 |
| `cross-tier-derived-symbol`                                                                                 | Forge-internal: a template emitting a reference to a per-component symbol no regenerated template defines (the `c.WorkerNightly undefined` build break after a component is added)                                                                                                                                                                                            | `--scaffolds` (forge's own tree)                  |
| `banner-tier1-missing-generated-header`, `banner-tier2-missing-scaffold-header`, `banner-unclassified`      | Forge-internal: a template that does not carry its ownership tier's banner                                                                                                                                                                                                                                                                                                    | `--banners` (forge's own tree; a no-op elsewhere) |
| `workaround-testing-extras`, `workaround-cmd-not-in-binaries`                                               | Ad-hoc patterns that paper over a missing primitive (a `testing_extras.go`, an undeclared `cmd/<name>.go`)                                                                                                                                                                                                                                                                    | `--check-workarounds`                             |
| `golangci-build-tags`                                                                                       | A finding in a file behind a project build tag (`//go:build integration`), which golangci-lint, run once with no tags, never saw                                                                                                                                                                                                                                              | bare `forge lint`                                 |

Other lint-lane flags that are not rules: `--strict` (escalates advisory
findings, such as RPCs missing an auth-posture annotation, to errors),
`--allow-skip LANE=REASON` (accept a named lane that cannot run here, by name
and with a reason), `--skip-frontends` (backend-only gate), `--check` and
`--no-fix` (print each fix's diff and fail if one is pending), `--gate-json`,
`--json`, and the hidden maintainer lanes `--scaffolds`, `--banners`,
`--check-workarounds`, `--exported-vars` and the `--suggest-*` helpers.
Related checks outside `forge lint`: `forge doctor` (deployability, payload
caps, tooling, cluster capability), `forge ci verify-generated`, `forge ci
test-budget`, `forge ci vuln-scan`, `forge ci validate-kcl`.

### `// forge:*` markers

`forge project capabilities` lists every marker (46 distinct names in 50
entries, because a few apply to more than one target: `forge:secret` and
`forge:owner` on a field and on a column, `forge:entity` on a message and on a
view, `forge:append-only` on an entity and on a table) with where each goes and
what it does. They fall into four families: **entity and write policy** (`entity`,
`soft-delete`, `append-only`, `read-only`, `computed`, `generated`, `guards`,
`immutable`, `version`, `fill`, `owner`, `secret`, `readable`, `closed-set`,
`ref`, `go-type`, `identity-subject`), **rpc behaviour** (`custom`, `mutation`,
`query-scope`, `unmanaged`), **composition** (`constructor`, `service`,
`contract`, `no-observe`, `exclude-contract`, `external-component`,
`not-a-component`, `root`, `root-only`, `standby`, `audience`, `optional-dep`,
`tx-opener`, `resolves-caller`), and **lint control** (`allow`,
`auth-unscoped-ok`, `optional-checked`), plus the `forge:lint-disable-*`
suppression comments that are not markers at all. A
misspelled marker is caught by `--proto-markers`, `--go-markers` and
`--column-markers`, so none of them can fail silently. Source: skill `proto`,
`forge project annotations`.

### Codemods that pair with lints

`forge fix` rewrites code to what a lint asks for, on request: `connect-errors`,
`narrow-deps`, `tx-isolation` (spell out every transaction's isolation level and
attempt budget), `composition-graph`, `harness-mocks`, `operatorkit`,
`config-proto-package`. Each is verified and a failed rewrite is taken back.
Source: `forge fix --help`.

## 4. Observability

Every forge project ships a full observability stack for the dev loop and the
same wire format for production. The signals are OpenTelemetry and the app
never learns the backend: it reads the standard `OTEL_*` variables.

- **A local ClickStack (ClickHouse, HyperDX and an OTLP collector) comes up
  with `forge env up`.** One container, loopback-only ports, three sources
  pre-created (Logs, Traces, Metrics) so search works on first open, and no
  sign-in. This removes "set up an observability stack before you can debug".
  Use: `forge env up dev`; opt out with `_observability = False`. Source: skill
  `observability`.

- **Browser console output is mirrored to disk.** Every `console.*` call, every
  uncaught error and every unhandled rejection from a scaffolded frontend lands
  in `.forge/logs/<env>/frontend_<name>.log` prefixed `[browser:<level>]`, next
  to the backend logs, so "I cannot see what the browser did" is a grep. An
  uncaught `TypeError` has no `console` call in source; it is in that file.
  Use: `grep '\[browser:error\]' .forge/logs/dev/*.log`. Source: skills `debug`,
  `frontend-runtime/telemetry`.

- **One log path.** The server writes JSON lines to stdout with the process
  resource; locally a collector tails `.forge/logs/<env>/*.log`, on a cluster a
  node agent reads container logs, and `trace_id` / `span_id` become the log's
  trace link. Mutually exclusive with the OTLP logs exporter by design, so a
  line is never double-counted. Source: skill `observability`.

- **`forge env status` proves the pipeline, not just the process.** It reads
  ClickHouse itself and reports rows in the store for traces, metrics and logs
  from the last 15 minutes, naming the services that sent them, plus app health,
  pprof and Delve; missing evidence is UNDETERMINED, never PASS. It verifies the
  telemetry pipeline, not app-flow correctness, and says so. Use: `forge env
status dev --signal traces|metrics|logs|profiles|app`. Source: skill
  `observability`.

- **Ask the running system from the shell, one JSON document per command.**
  `forge obs status|workloads|check|changes|issues|issue|logs|traces|trace|
endpoints|metrics|query|sessions|session|profile|alerts|dashboards` answer
  against the local ClickStack or, for a hosted env, the control plane, through
  one query compiler (`pkg/obsquery`), so a query that works locally means the
  same against prod. Raw SQL goes through a read-only guard; `--explain` prints
  the compiled statements. Use: `forge obs logs prod --service api --severity
error --json`. Source: skill `observability/query`.

- **Issues: grouped errors with an owner, a cause and a release.** Issues are
  fingerprinted at query time locally and at ingest on the control plane with
  the same algorithm; issues that share a cause and first fired together
  collapse into one incident without being merged; triage (`resolve`, `ignore`,
  `assign`, `comment`) is a verb. Source: skill `observability/query`.

- **Traces you can read and trust.** `forge obs trace` prints a capped tree with
  error spans and the critical path expanded, how each span ended (`rpc=CANCELED`,
  `http=403`), linked log lines, and `⋯ uninstrumented 9.99s of 10.00s` where
  time went to code that emitted no span. Filter any attribute with
  `--filter span.attr.chat.id=<id>`. Source: skill `observability/query`.

- **Metrics are declared.** `internal/metrics/metrics.go` declares each
  instrument (name, type, UCUM unit, attribute keys, stability, `since`); an
  undeclared attribute panics in development and is dropped and counted in
  production, which keeps cardinality from silently exploding. The declared set
  prints with `<bin> metrics schema --json` and `forge obs metrics declared`.
  Source: skill `observability`.

- **Continuous profiles with no setup.** Every process with an OTLP endpoint
  samples a 5s CPU profile every five minutes and a live-heap snapshot every two
  hours, pushed over OTLP; `forge obs profile` exports pprof, speedscope or
  collapsed stacks. Source: skill `observability/profiles`.

- **Alerts are code.** `forge.AlertRule` (a ClickHouse SQL `expr`, every
  returned row a firing instance), `forge.AlertRoute` and `forge.AlertDefault`
  live in KCL, are validated at render and recorded in the release shape. Forge
  generates defaults: `ServiceErrorRateHigh`, `ServiceLatencyP95High`,
  `ServiceSilent`, `FrontendJSErrorSpike`, `FrontendLCPPoor`, `NewIssue`,
  `IssueRegressed`, `IssueSpike`, `ReleaseRegressed`, `ServiceAnomaly` (a
  4-MAD seasonal comparison), `UptimeCheckFailing`/`UptimeCheckDown`,
  `CertificateExpiring`/`CertificateExpiringSoon` and `WorkloadCrashLooping`.
  `forge env up` evaluates rules continuously and delivers them through a local
  Alertmanager. Error-rate defaults read the tail-sampled spans correctly rather
  than over-stating the rate. Use: `forge obs alerts check|run`. Source: skill
  `observability/alerts`.

- **Dashboards derived from the code.** Process and role dashboards come from
  the deployed commands and the composition graph, as HyperDX JSON in
  `deploy/observability/dashboards/` (only `*_gen.json` is forge's); a dashboard
  that renders empty is diagnosed by replaying each panel and probing the empty
  ones. Use: `forge obs dashboards check|audit|add-panel`. Source: skills
  `observability/dashboards`, `debug/dashboards`.

- **Real-user monitoring and sessions.** The browser SDK, a same-origin `/_otel`
  relay (cookies and `authorization` never forwarded), source maps extracted to
  a registry sidecar and never served, and `forge obs sessions|session` render
  what one user did as a text timeline of navigations, clicks on masked
  selectors, failed requests and errors with their issue and trace ids. Session
  replay is the one opt-in category and is always masked. Source: skills
  `frontend-runtime/telemetry`, `observability/query`.

- **Uptime and certificate checks, outside in.** `forge.UptimeCheck` probes a
  hosted env's public frontends and routes on a schedule and derives checks with
  no declaration; the certificate-expiry alerts fire at 14 and 3 days. Source:
  skill `observability/uptime`.

- **Send telemetry anywhere.** `forge.Observability` names an OTLP endpoint, a
  headers secret _reference_, which signals and sampling; forge renders the
  collector for the runtime (compose forwarder, node-agent DaemonSet). A local
  env can store its telemetry in the org's hosted store (`dev_telemetry`,
  `--telemetry=auto|hosted|local`). Source: skill `observability/destination`.

- **Opt-out is real and enforced at the source.** `observability =
forge.Observability { enabled = False }` or per category (`traces`,
  `metrics`, `logs`, `profiles`, `metadata`, `browser`); disabled data is never
  collected or stored, so never billed, and the platform drops it at the node
  agent. Session replay is opt-in. Source: skill `observability`.

- **A queryable audit trail, and a typed activity stream.** Every RPC writes a
  structured audit record to the logs; `pkg/audit` adds a DB-persisted trail
  with a scoped `ListAuditEvents`. The activity stream is declared on the rpc
  (`(forge.v1.method).activity`: verb, target, env) and written by one
  interceptor, never by hand, and `forgeconv-activity-declared` makes the
  declaration mandatory. Source: skills `observability/audit-log`, `activity`.

- **A read-only window on app data.** `forge db query <env> "<sql>"
--confirm-env <env> --reason <why>` runs one statement in a READ ONLY
  transaction as the `forge_ro` role, which cannot read `forge:secret` columns
  (refused by name, through `select *`, `upper(col)` and `row_to_json`), under
  the env's `data_access` policy (`open`, `audited`, `deny`) with an audit row
  and an activity event on hosted envs. Source: skill `db/query`.

## 5. Auth and identity

Authentication is a middleware and nothing more: an interceptor validates the
credential and puts `*Claims` on the context, fail-closed. Forge authenticates;
it does not authorize, and says so.

- **Managed identity is the default for a project with a frontend.** Declare the
  users once in `deploy/kcl/identity.k` (`forge.ManagedIdentity`: roles, owners,
  machine users, seed users, signup mode, sign-in methods) and every env imports
  it. Forge fills the validator's issuer, audience and the login broker's
  credentials per env: a dev IdP in dev, the control plane's identity instance
  in staging and prod. A workload that sets `JWT_ISSUER`, `JWT_AUDIENCE`,
  `OIDC_CLIENT_ID`, `OIDC_REDIRECT_URI` or `IDP_BASE` beside `Bundle.identity` is
  refused at render, so there is one source per field. Use: `forge.ManagedIdentity`;
  administer a hosted env's users with `forge cloud identity
users|invite|reset|lock|unlock|set-roles|revoke-sessions|export|delete`. Source:
  skills `auth`, `deploy/managed-identity`.

- **A real sign-in works locally with no setup.** `forge env up` converges a
  Zitadel as a host process (a pinned, checksum-verified binary, no container),
  prints where to sign in, and writes the broker token and ids to the env's
  secret store. Mail goes to a declared Mailpit. This removes the usual detour
  through a hand-rolled credential-injecting shim that has to be deleted later.
  Use: `forge env up dev`. Source: skill `auth/dev-loop`.

- **A login broker with passwords, passkeys, magic links and social sign-in.**
  `methods`, `social`, `signup` (`invite`, `open`, `domain`), `owners` and
  `email` are declared in KCL and projected to the instance, and
  `forgeconv-identity-methods` fails a declaration the project's own broker does
  not serve. Source: skills `auth/frontend`, `deploy/managed-identity`.

- **Which RPCs are public is declared in the proto, per rpc.** `auth_required:
false` publishes one; unset or `true` needs a valid token. There is no second
  list. `forge project audit` includes an `unscoped_auth` list: authenticated
  rpcs that never resolve the caller, with `// forge:auth-unscoped-ok: <reason>`
  as the acknowledged exception. Source: skill `auth`.

- **One listener per kind of caller.** `// forge:audience <word>` names the
  trust domain an authenticator serves, and `forge lint --graph` fails a
  listener that serves a service of a different audience, so a daemon token
  cannot reach a user service. Source: skill `architecture/graph-checks`.

- **From identity to principal, at one chokepoint.** `enrichClaims` runs after
  validation and before any handler, hydrating by the stable subject, never by
  email. `// forge:owner` on a column makes forge scaffold the owner-scoping
  wrapper and `forgeconv-crud-owner-resolver` keeps siblings consistent. Binding
  an invitation or membership by email must require a verified email. Source:
  skill `auth/authorization`.

- **API keys are wired and cannot be confused with tokens.** `X-API-Key` or
  `Bearer fk_…` is routed by shape to a validator over your own table; the
  Authenticator stamps `Claims.Credential` itself (unforgeable by a token), so a
  per-tenant key can carry its tenant and role and a handler can refuse key
  management made with a key. `pkg/apikey` provides generation, hashing and
  constant-time verification. Source: skill `auth/api-keys`; package `pkg/apikey`.

- **A shared, operated IdP survives an outage on restart.** The JWKS snapshot
  fallback lets a restart succeed while the issuer is down, with the trust model
  stated. Source: skill `auth/shared-issuer`.

- **One machine credential.** `pkg/accesstoken` is the `rlat_` token: wire
  format, hashing, constant-time comparison, a closed scope vocabulary, and the
  rule that a token may never grant authority it does not hold. Org automation
  tokens are minted with `forge cloud token create`. Source: `pkg/accesstoken`.

- **Bring your own issuer.** Declare no identity in an env and set
  `jwt_issuer` and `oidc_client_id` in its config; Auth0, Okta and Keycloak
  remain supported. Source: skill `auth`.

## 6. Secrets

- **A secret is declared once as a reference, bound per env to a provider, and
  never stored in git.** KCL is checked in and stays pure: secret values never
  appear in rendered output, only the references. Use: a `sensitive` config
  field or `EnvVar.secret_ref`; `forge secret set --env <env> <KEY>` (the env
  is required, with no default and no positional form, so a secret cannot land
  in the wrong one). Source: skill `secrets`.

- **Providers per env.** `forge.FileSecrets` (a gitignored YAML store) for dev,
  `forge.HostedSecrets` (write-only on the control plane; who may use a secret is
  `forge secret allow|deny|access`) for hosted envs, `forge.ExternalSecrets` and
  the `forge.ExternalSecretsOperator` component for a cluster, a self-run
  `forge.OpenBao` with JWT-only client auth, and `forge.ControlPlaneSecret` for a
  value the control plane supplies and renews with no restart. Source: skills
  `secrets`, `deploy/external-secrets`, `deploy/openbao`,
  `deploy/control-plane-secret`.

- **No `.env` files.** The `no-dotenv` lint fails on one, because a dotenv is
  injected wholesale into every host service and values never have to be
  declared in KCL. Source: skill `secrets`.

- **Generated key material.** `generate` mints random keys, passwords that meet
  a complexity policy, and key pairs and certificates for `FileSecrets`. Source:
  `pkg/secretgen`.

- **Secrets are judged per env, with file and line.** `forge lint --secrets`
  checks the model described in [section 3](#deploy-and-secrets). `forge env
secrets verify` compares a live Secret with its store copy per key (match or
  no-match, never a value or hash), `adopt` hands a verified Secret to ESO, and
  `forge env shape` never carries a secret value. Source: skills `secrets`,
  `deploy/shape`.

- **Key rotation without unreadable data.** `pkg/crypto` is AES-256-GCM at rest
  under a versioned keyring so a key can rotate without making sealed values
  permanently unreadable. Source: `pkg/crypto`.

- **Credential-shaped columns are withheld by default.** The read-only role
  refuses `forge:secret` columns and `--db-grants` fails a credential-named
  column nobody decided. Source: skill `db/query`.

- **Redaction on the way out.** `pkg/redact` removes credential shapes and
  personal paths from text that leaves the process (bug reports, error strings
  sent to telemetry); `forge report-bug --dry-run` prints exactly what would be
  sent. Source: `pkg/redact`, `docs/report-bug.md`.

## 7. Data

SQL is the schema language: the applied migrations are the single source of
truth, and everything else is a projection of them.

- **The ORM, stores and CRUD are projected from the applied schema.** Forge
  runs `db/migrations/*.up.sql` in order against a real ephemeral Postgres and
  generates the entity structs, typed ORM, per-entity stores and CRUD wiring
  from the result, so the code cannot disagree with the database. Behaviour comes
  from real columns: `created_at`+`updated_at` give managed timestamps, a
  time-typed `deleted_at` gives soft delete, text columns give generated list
  search. Use: `forge generate`; `COMMENT ON TABLE t IS 'forge:no-entity'` opts a
  table out. Source: skills `db`, `db/generated-orm`, `db/store`.

- **`forge scaffold entity` births everything an entity needs.** Mark a message
  `// forge:entity` and run bare `forge scaffold` for the create-table migration,
  the CRUD quintet, managed fields, ORM, CRUD ops, a lifecycle test, list,
  detail, new and edit pages, nav and mock data. Source: skills `db/birth`,
  `authoring`.

- **Write policy is declared where the column lives.** `forge:read-only`,
  `forge:computed`, `forge:generated`, `forge:guards`, `forge:immutable`,
  `forge:version` (optimistic concurrency) and `forge:append-only` say who may
  write a column, with a decision table. Lints in
  [section 3](#data-integrity-who-may-write-a-column-and-what-a-migration-may-do)
  make an unwritten field a build failure. Source: skill `db/write-policy`.

- **Append-only ledgers are enforced twice.** `forge:append-only` removes
  Update/Delete from the store interface and delegates and installs a trigger
  that rejects every UPDATE and DELETE. Source: skill `db/append-only`.

- **Transactions name their correctness argument.** `s.deps.DB.RunTx(ctx,
orm.Serializable|orm.ReadCommitted, fn)` has no default isolation: lock-free
  read-check-write under SERIALIZABLE with retry, or a lock under READ COMMITTED
  with one attempt, never both, and `forgeconv-tx-lock-under-serializable` checks
  it. `orm.AfterCommit` holds side effects until commit. Use: `forge fix
tx-isolation`. Source: skill `service-layer/transactions`.

- **Forward-only, never-waiting migrations.** Timestamp versions, a per-file
  transaction, `lock_timeout` 5s with bounded retry, `statement_timeout` 30m,
  `-- forge:lock-timeout` and `-- forge:statement-timeout` overrides, and
  `CREATE INDEX CONCURRENTLY` with invalid-index cleanup. Deployed envs apply
  migrations from a rendered initContainer under an advisory lock so replica
  races cannot double-apply. `forge db migration rebase` re-versions a branch's
  migrations after everything merged. Use: `forge db migration new`, `forge db
migrate up|status|version|force`, `forge db squash`. Source: skills
  `db/migration-timeouts`, `db/deploy-migrations`, `db/versioning`.

- **A wedged dev database recovers without hand-editing.** Dirty migration
  state is repaired and cleared with `forge db migrate force`; `forge db reset`
  drops, recreates, migrates and seeds in dev only. Source: skill `db/recovery`.

- **Seed data derived from the schema.** `forge db seed apply` materializes
  deterministic, FK-coherent rows at runtime (never a persisted seed file),
  satisfying CHECKs, UNIQUEs, composite FKs and status lifecycles, and learns
  your nouns from `db/seeds/vocab.yaml`. Use: `forge db seed apply|reset|status`.
  Source: skill `db/seeding`; package `pkg/seedplan`.

- **A real Postgres in every test.** `pkg/pgtest` and `testkit.NewPostgresDB`
  give each test its own migrated clone of a template database on a shared
  server per run, with no Docker, so database tests belong in the fast tier.
  Source: package `pkg/pgtest`, skill `testing`.

- **Dashboards over your database, by Postgres privileges.** The `forge_dash`
  role is granted exactly the columns a panel needs by an ordinary migration, with
  a row cap, timeout and concurrency limit; the allowlist is privileges, not an
  SQL parser. A browse-only table browser is `pkg/dbbrowse`. Source: skill
  `db/dashboards`.

- **Time series read back correctly.** `--time-bucketing` plus the
  three-argument `date_trunc('day', col, 'UTC')` keep a daily total from moving
  with the deploy host. Source: skill `db/time-buckets`.

- **Object storage.** Declare `forge.Bucket` per env (MinIO in dev, hosted
  provisioning on Reliant, any S3-compatible bucket elsewhere), and
  `forge/pkg/blobstore` presigns uploads that go from the browser straight to the
  bucket so bytes never pass through the API. Source: skill `storage/blobstore`.

## 8. APIs and frontends

- **Connect RPC from the proto, with typed hooks.** The proto is the API's
  canonical input: forge generates the handler stubs, mounts and typed React
  Query hooks, and an rpc's logic is a `*Service` method in
  `internal/handlers/<svc>/rpc_<name>.go`. Use: `forge scaffold rpc <svc> <Name>`
  writes a correctly signed method and a self-destructing test row, so nothing
  is hand-written to the stub. Source: skill `api`.

- **REST and OpenAPI as projections.** `api.openapi: true` emits OpenAPI 3 from
  the proto in the buf pipeline, and `connectrpc.com/vanguard` transcodes REST
  URLs onto Connect handlers at runtime, with no parallel codegen. `forge api
curl` prints a copy-pasteable curl for any method. Source: skills
  `api-openapi`, `api-rest`, `api/http`.

- **Validation at the boundary.** `pkg/validate` enforces protovalidate
  (`buf.validate`) rules as an interceptor, so request validation is declared in
  the proto. Source: `pkg/validate`.

- **Breaking changes are gated.** `buf breaking` runs in a generated
  `proto-breaking.yml` and a project's reserved-tag discipline is a skill
  (`proto-breaking`). Source: skills `ci`, `proto-breaking`.

- **A browse-only inventory of the API.** `forge generate` writes
  `gen/forge_service_catalog.json` (`forge.dev/service-catalog/v1`): processes,
  the proto services each serves, methods with auth and idempotency, and
  components with source locations, from the same typed graph, with no server
  needed. Source: `docs/service-catalog.md`; package `pkg/servicecatalog`.

- **The frontend runtime.** `@reliantlabs/forge-web-runtime` is the web twin of
  `forge/pkg`: a transport interceptor stack (error normalization, auth, brand,
  always-on W3C `traceparent`), typed errors with a total `.reason` vocabulary
  (never match on message text), the app shell, a generic `<Resource>`
  data-table with the loading/error/empty/data ladder, debounced server filters
  and cursor pagination. Source: skills `frontend-runtime`,
  `frontend-runtime/transport`, `frontend-runtime/resource`.

- **Per-env values without a rebuild.** Frontend config is a typed
  `config_gen.ts` read at runtime from `<basePath>/config.js`, which the platform
  writes per env, so one bundle promotes unchanged; `forgeconv-frontend-process-env`
  forbids build-time inlining. A key not in the schema gets a did-you-mean.
  Source: skills `frontend/runtime-config`, `frontend-runtime/config`.

- **Mock mode that is not a stub.** `mock_api` (`""`, `true`, `hybrid`) answers
  every RPC from the descriptor with deterministic, schema-valid messages (ids,
  recent timestamps, names a field suggests, streams included), so a UI can be
  built before the backend exists; scenarios stub specific rpcs. Source: skill
  `frontend/mock-mode`.

- **A component library and design discipline.** 70+ components
  (`forge component search|install|list`), Tailwind v4, enum-aware status
  badges and FK pickers scaffolded, and a `frontend/design` skill that asks for
  a brief first and will not invent an aesthetic. The scaffolded UI is starter
  code, stated as such. Source: skills `frontend`, `frontend/components`,
  `frontend/design`, `frontend/design/verify`.

- **Static export is checked before CI.** `forge lint --static-export` says,
  with file and line, what `next build` would refuse. Source: skill
  `frontend/serving`.

- **Any frontend, adopted in place.** An existing Vite or Next.js app moves
  under `frontends/` or is named by a KCL path and forge generates into it with
  one writer per `src/gen`. A pnpm-workspaces layout is opt-in for sharing code
  between web and mobile. Source: skills `frontend/adopt`, `frontend-workspaces`.

## 9. The dev loop

- **One command brings up the whole stack.** `forge env up dev` builds, applies
  to a cluster if anything is cluster-bound, launches host processes and serves
  every frontend, with Postgres, the IdP, ClickStack and mail declared as infra
  that is READY before anything that dials it starts. The scaffolded `dev` runs
  entirely on the host and needs neither Docker nor a cluster. Use: `forge env up
dev [--no-build] [--no-deploy] [--target <workload>] [-D name=value]
[--background|--watch]`. Source: skill `dev`.

- **Hot reload, per workload.** A host workload is run by `go-run`, `air` or a
  binary runner, and `--target` scopes the whole run to one workload. Source:
  skill `dev`.

- **Every `up` measures itself.** A phase timings table prints above the summary
  and one JSON line is appended to `.forge/logs/<env>/up-timings.jsonl`
  (`forge.env.up.timings/v1`), so a dev-loop speedup is measured before it is
  claimed. Source: skill `dev`.

- **Parallel stacks do not collide.** A worktree gets its own port block and its
  own data; `allocate_port(base, key)` derives each port, `forge env devstack
list|port|prune|release` manage the blocks, and
  `forgeconv-allocate-port-spacing` catches two bases that would collide. The
  default ceiling is `max_stacks: 8`. Use: `forge env ps` lists every stack on the
  machine. Source: skill `dev`, `internal/config/config.go`.

- **Stopping a stack never stops the process running the command.** `forge env
down` walks its own parent chain first, so it cannot end the agent or shell
  that started it; `--all` stops every stack on the machine. Source: skill `dev`.

- **Read the resolved config.** `forge env config <env> [--workload <name>]`
  prints what `deploy/kcl/<env>/` hands each workload, `forge env exec` runs a
  command with it, `forge env options` lists the `-D` render options, and
  `forge doctor parity` diffs a service's host-mode against cluster-mode env.
  Source: skill `dev`.

- **`forge generate` is safe, cached and lockable.** It never overwrites
  hand-written logic; a re-run that finds every input byte-identical (tree,
  ignored inputs, forge binary, tools, environment) prints `nothing to do` in
  seconds, and `--no-cache` forces a full run. Concurrent `generate` and
  `scaffold` runs from parallel agents queue on `.forge/forge.lock` and the OS
  releases it when the holder exits, so a killed run never strands it. Source:
  skill `forge`.

- **Heavy work queues instead of thrashing.** `pkg/hostslot` is a host-wide
  counting semaphore of exclusive file locks: at most N heavy jobs (builds, test
  lanes, codegen, full lints, gate checks) run at once on a machine, default
  min(CPUs/2, memory GiB/8). Measured on a 16 vCPU host, `go build ./...` took
  36-50s beside 0-3 other heavy jobs and 101-155s beside 8 or more; a full local
  gate went from about 40 minutes to about 2.3 hours when everything ran at once.
  A crashed holder never strands a slot. Use: `forge slot run`, `forge slot
status`, `FORGE_HEAVY_SLOTS`. Source: skill `ci/slots`.

- **Local caches stay bounded.** `forge storage status|check|gc|gocache|worktrees`
  report host capacity, refuse a build below the free-space reserve, trim Go
  build caches and remove idle, clean, pushed worktrees. Source: `forge storage
--help`.

- **Debug a running service.** `forge debug start|break|continue|step|locals|
eval|stack|stop` drives Delve against a service. Source: `forge debug --help`.

- **Local k3d and multi-worktree namespaces.** `forge cluster up|reload|status|
logs|urls|instances` and per-worktree namespaces on one shared cluster, with a
  kubectl-context pin as a safety rail. Source: skill `dev`.

## 10. Testing and CI

- **Three test tiers map onto forge machinery.** Unit tests use the real subject
  plus generated contract mocks (`mock_gen.go`, one per interface in
  `contract.go`, never hand-rolled); integration tests use a real ephemeral
  Postgres with your migrations applied; end-to-end tests run against a stack
  from `forge env up dev`. The subject under test stays real at every tier. Use:
  `task test:short -- ./internal/<pkg>/...` while iterating, `task test` once at
  the end, `task test:integration`, `task test:e2e`. Source: skill `testing`.

- **A slow test says so; an unmarked test is fast.** `testkit.Slow(t, why,
testkit.Inputs("deploy/**", …))` skips a test under `-short` and in the
  integration lane and runs it in `task test`. Inputs are string-literal globs so
  a tool can select a render test exactly when `deploy/` changed without running
  it. A plain `if testing.Short() { t.Skip(...) }` also counts. Two guards hold
  the line because neither sees everything: `forge lint --slow-tests` (static,
  before it ever runs) and `go test -short -json ./... | forge ci test-budget`
  (measured, after). Source: skill `testing`; package `pkg/testkit`.

- **Cache soundness is a rule, not a hope.** Go's test cache keys only on what
  the test process opens inside the module, so a test that execs a child reading
  repo files can replay a stale PASS after the edit that broke it. Such a test
  either skips under `-short` or opens those inputs itself, and
  `forge-slow-test-inputs` reads the declared inputs from source. Never set a
  private `GOCACHE`; do not add `-count=1` or `-race` to inner-loop runs.
  Source: skill `testing`.

- **Table-driven handler tests without the boilerplate.** `pkg/tdd` plus a
  scaffolded per-rpc test row; `forge project migrate tdd` converts hand-rolled
  tables. Flow tests seed the FK spine with `seedplan.SeedGraph` and drive the real
  service against a migrated DB. Source: skills `testing/patterns`,
  `testing/flow`.

- **`task ci` is the whole PR gate on your machine.** `forge ci gate` runs
  `ci:checks` (verify-generated, golangci-lint, buf lint, migration safety,
  per-frontend lint and typecheck and CSS health, go build, vuln scans, license
  check, KCL validation, deployability), then `task test`, then
  `task test:integration`, in one summary table. Four rules: every failure is
  reported; a skip is not a pass and fails the gate unless named in `CI_SKIP`
  (a name that matches no check is an error); tools are pinned and built into a
  shared cache so a copy on PATH at another version is never used; and the CI
  workflow is a thin wrapper around the same tasks, so "CI did not run" never
  has to mean "unverified". Verdicts are cached on an unchanged committed tree
  (`PASS (cached <sha>)`) and a branch unit-tests only the packages the change
  reaches. Use: `task ci`, `forge ci gate --list`, `CI_SKIP`, `CI_FULL=1`.
  Source: skill `ci/gate`.

- **`verify-generated` makes generated code reviewable.** `forge ci
verify-generated` fails when generated code is stale or hand-edited, and the
  commit-policy lints keep it tracked so a checkout builds as cloned. Source:
  skill `ci`.

- **Generated workflows with a stated ownership boundary.** `ci.yml`,
  `proto-breaking.yml`, `build-images.yml` (cosign signature, SBOM, SLSA
  provenance), `deploy.yml` (a matrix entry per declared env), `e2e.yml`,
  `pre-commit.yml`, Dependabot and CODEOWNERS ship, each labelled Tier-1
  (regenerated, do not edit) or Tier-2 (scaffolded once, yours). Add checks with
  `ci.gate.extra_checks`. Source: skills `ci`, `ci/extending`.

- **Deploys from CI go through forge.** `forge env build <env> --push` then
  `forge env deploy <env> --yes`; a hosted env needs one token and no registry
  credential; `forge ci summarize` renders any `--json` document as a job
  summary and `forge ci run` shows one run's timeline across cut, checks,
  promotes and rollouts. Source: skill `ci`.

- **Reading CI evidence back.** `forge ci verify-test-run` proves a `go test
-json` run actually ran its tests, and `forge ci vuln-scan` scans Go, npm and
  Docker per `forge.yaml`. Source: `forge ci --help`.

## 11. Workers, jobs, operators, CLIs and mobile

- **Workers share the server's lifecycle, typing and shutdown.** A worker is a
  supervised component constructed in the same composition as services, with typed
  Deps and graceful drain, scaffolded with a lifecycle test. Use: `forge scaffold
worker <name>`. Source: skill `workers`; package `pkg/lifecyclekit`.

- **Cron that fires once, not once per replica.** `--kind cron --schedule
"*/5 * * * *"` runs on `pkg/cronkit` so each tick fires on exactly one replica,
  with the scheduled time identical on every replica (`cronkit.TickTime`), and
  `pkg/lease` gives a named process-level singleton over Postgres advisory locks
  with hot standby. This removes the "the nightly job ran three times" bug. Source:
  skill `workers`; packages `pkg/cronkit`, `pkg/lease`.

- **Pick the right long-running shape.** Service (Connect RPC mounted on the
  server), worker (in the server's lifecycle), binary (its own `cmd/<bin>` and
  Deployment) and operator (controller-runtime with CRDs) each have a scaffold
  and a decision table. Use: `forge scaffold service|worker|binary|operator|crd|
webhook|adapter`. Source: skills `binaries`, `services`.

- **Operators and CRDs.** `forge scaffold operator` and `forge scaffold crd` give
  a reconciler with `pkg/controller` runtime helpers and `pkg/appkit`; a
  StatefulSet whose declared volume grew is converged by a pre-rollout resize job
  (`pkg/stsresize`). Source: skills `binaries`, `deploy/statefulset-volumes`.

- **Operators and cron need a cluster you run.** Reliant hosting runs services,
  workers and jobs; cron-kind workloads and operators run on a cluster you
  operate (a worker with its own schedule runs hosted). Source: skill
  `deploy/hosting`.

- **CLIs and libraries.** `forge project new --kind cli` gives a cobra binary
  with `pkg/cmdkit`, per-binary typed config loadable from a file, and
  subtree configs shared by several commands; `--kind library` gives a Go module
  with no server. Source: skills `binaries`, `binaries/config-files`,
  `binaries/subtrees`.

- **Outbound boundaries are adapters.** `forge scaffold adapter` writes one
  narrow, vendor-neutral boundary per third-party system, and
  `// forge:outbound-io` plus `forgeconv-outbound-io-no-rpc` keep it outbound-only.
  Source: skill `adapter`.

- **Approvals for things an agent proposed.** `pkg/approval` holds proposed items
  until a person approves or rejects them, expires the undecided at their
  deadline and hands each decided item to the app's follow-up exactly once.
  Source: `pkg/approval`.

- **Mobile.** `forge scaffold frontend --kind mobile` produces an Expo React
  Native app with `app/` screens and `app:background`/`app:foreground` events,
  sharing the same generated Connect hooks, with an optional `ui-native` workspace
  package. Source: skills `frontend`, `ui-native-package`.

- **Adopt an existing codebase.** Migration skills cover service, CLI and
  library projects, forge-version upgrades with per-version playbooks, and
  `forge project upgrade list|apply` applies every mechanical fix for a range.
  Source: skills `migration`, `migration-service`, `migration-cli`,
  `migration-upgrade`.

## 12. LLM integration

- **An app never holds a model key.** Declare `llm = forge.LLM { runtime =
forge.OnHosted {} }` per env and give a workload `env = forge.llm_env()`: the
  platform mints a per-environment, capped, billed key and puts it in the pod.
  Reliant's gateway speaks the OpenAI and Anthropic wire formats, so the official
  SDKs are used unmodified (`anthropic.NewClient()` reads
  `ANTHROPIC_BASE_URL` and `ANTHROPIC_API_KEY`), with streaming on both and
  image generation. Dev and your own cluster use `forge.LLMEndpoint`, including a
  local Ollama. Use: `forge.LLM`, `forge.llm_env()`. Source: skills `deploy/llm`,
  `llm`.

- **A decision about where the model belongs.** The `llm` skill states the split
  that works: agentic or judgement work (several steps, tools, loops, "should we
  do X") belongs in a Reliant workflow that calls the app's API through fixed
  steps and keeps the app LLM-free, while one bounded call with a structured
  result (classify, summarise, extract) may be inline. The rule is "the LLM
  proposes, code decides": money, limits, approvals and halts are rules in the
  app, an inline call's output is untrusted input, and the model is never called
  from the frontend (the key would ship to the browser). Source: skill `llm`.

## 13. Agents: forge as a surface for LLMs

A large part of what Forge is for is making an LLM agent effective in a
project, and several features exist only for that.

- **The whole surface prints from the binary.** `forge project capabilities`
  (every verb, lint lane and marker), `forge project annotations` (the
  entity-authoring vocabulary), `forge project libraries` (every `forge/pkg`
  package, and with a name its full signatures, which `go doc` renders as
  `struct{ ... }`), `forge project audit --json` (what this project currently
  is) and `forge project shapes` (every rpc, message, enum, table, handler and
  hook with `file:line`), so an agent never needs forge's own source. Source:
  skill `forge`.

- **Skills come from the binary and match the forge in use.** `forge skill
list|load|search` serves 180+ skills, never written into a project; a thin
  pointer is generated for Claude Code and Cursor. A skill that disagrees with
  the project is reported as a forge bug. Source: `forge skill --help`.

- **Machine-readable everything.** `--json` on lint, obs, deploy plan and
  status, with documented schemas, shared exit codes and `suggested[]` next
  commands; `forge mcp` exposes forge as an MCP server on stdio for any harness
  (`docs/mcp.md`).

- **Friction is reported, not hidden.** `forge report-bug` sends a defect with a
  redacted payload and `--dry-run` prints exactly what would go. Source:
  `docs/report-bug.md`.

## Maintaining this page

This page is only useful while it is true, so changing it is part of changing
Forge.

- **Add an entry with the feature PR.** A PR that adds a user-visible benefit
  adds an entry here in the same PR, under the section it belongs to, in the
  shape _bold name, one or two sentences on what it is and what it prevents, a
  `Use:` pointer, a `Source:`_ pointer. The PR title is the changelog entry
  (never edit `CHANGELOG.md`), and this page is the capability list.
- **State status honestly.** Use "enforced", "recorded" and "in progress"
  exactly as defined at the top. A design that has not shipped belongs under an
  "in progress" label, never in the present tense.
- **Keep the lint tables in sync with the binary.** The rules in section 3 are
  checked against `gradableRules()` in `internal/cli/lint/lint_rules.go`. When a
  rule is added, add its row to the table for the bug class it prevents, and its
  `IntroducedIn` / `GatesFrom` row in `lint_rollout_table.go` (a test already
  forces that one). Refresh the lane list from `forge project capabilities`
  (`--json | jq '.analyzers[].flag'`) and the marker list the same way
  (`.markers[].name`).
- **Prefer a pointer over a copy.** A list that prints from the binary (verbs,
  libraries, skills, markers) is linked, not retyped, so it cannot go stale.
- **A proposed guard (not built).** The cheapest way to stop the lint tables
  drifting is a test beside `TestEveryGradableRuleDeclaresItsRollout` that reads
  `gradableRules()` and fails when a rule id does not appear in
  `docs/benefits.md`, with a small allowlist for the two forge-internal families
  (`banner-*`, `cross-tier-derived-symbol`). It would make "I added an analyzer"
  impossible to merge without an entry. A second, optional test could compare
  the marker and lane counts quoted here with `project capabilities`.
