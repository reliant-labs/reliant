# Changes: one model for remote config, gradual rollouts and A/B tests

**Status:** beta. Direction approved by the owner on 2026-11-20 (the "Settled" list
below). Everything else here is **proposed**; the open owner decisions are
collected in [section 8](#8-open-owner-decisions) and each says what it blocks.
**Date:** 2026-11-20
**Related:** [product-events.md](product-events.md) (the events this measures
with), [start-anywhere.md](start-anywhere.md) (declarations in `forge.yaml`, use
piece by piece), `docs/benefits.md` (`controlled-rollouts-and-experiments`,
status in-progress).

## Where it stands (2026-11-25)

The owner approved finishing Changes, labelled **beta** until delivery, the
readout and the UI ship. Most of [section 9](#9-rollout-plan) has merged.
Merged is not released, so each row says which final tag carries it.

| Slice                             | State                                                                                                                                                                                                             |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Wave 1, evaluation core           | Released: forge #2462 (forge v1.10.0, web-runtime v0.13.21).                                                                                                                                                      |
| 1. Declaration and shape          | Released: forge #2483 (v1.10.0), as `changes.yaml` ([../changes.md](../changes.md)).                                                                                                                              |
| 2. ChangesService, scopes, ledger | Released: forge #2465 (v1.10.0), control-plane #3146 (v1.31.0).                                                                                                                                                   |
| 3. Static-site delivery           | Released: control-plane #3154 (v1.31.0). Live: reliantlabs.io serves `/_forge/changes.json`.                                                                                                                      |
| 4. Evaluation events              | Released: `flag_evaluated` with `run`, `unit`, `reason`, `revision` (forge #2470). The `account` prop for the account unit is not.                                                                                |
| 5. Readout                        | Released: forge #2471 (v1.10.0), control-plane #3153 (v1.31.0).                                                                                                                                                   |
| 6. UI                             | Experiments page released (reliant #3082, v1.27.0) but hidden from prod's nav: control-plane #3555 (`CHANGES_UI` on) is in no final tag. No delivery indicator yet.                                               |
| 7. Agent tools and approvals      | Released: reliant #3082 (v1.27.0), control-plane #3146.                                                                                                                                                           |
| 8. Backend delivery, OpenFeature  | OpenFeature and typed accessors released (forge v1.10.0). Hosted backends: the ConfigMap file merged (control-plane #3154), but it reaches a hosted app only with control-plane #3588, in no final tag. No fetch. |
| Dogfood: landing-page hero test   | Live (landing-page #162): `hero-copy`, unit `visitor`, a 5% experiment.                                                                                                                                           |
| Dogfood: a signed-in Reliant ramp | Not started.                                                                                                                                                                                                      |

Decisions 1, 2, 3 and 6 were decided by the owner on 2026-11-25; the others
record what the released code already does, matching the recommendation in
[section 8](#8-open-owner-decisions).

| #   | State                                                                                                                                                                                                                                                                                                                                         |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | **Decided (owner, 2026-11-25):** a project-root `changes.yaml`, one declaration for every env, no KCL needed (released in forge v1.10.0).                                                                                                                                                                                                     |
| 2   | **Decided (owner, 2026-11-25):** hosted backends read a ConfigMap mounted as the file `FORGE_CHANGES_FILE` names (released); apps not on Reliant hosting fetch `GetChangesSnapshot` with a platform-minted token, plus the egress allow. The fetch is being built.                                                                            |
| 3   | **Decided (owner, 2026-11-25):** a production launch, ramp up or roll-out needs a person, enforced by the control plane: a console browser session, or a one-time code emailed to the person the credential acts for (`step_up_code`). A token or CLI login acting for a person is refused. Stop, end run and ramp down never wait. Released. |
| 4   | (a), per app: `/forge/p/<app>/experiments`.                                                                                                                                                                                                                                                                                                   |
| 5   | (a): `changes:write` and `changes:launch`, grantable per environment.                                                                                                                                                                                                                                                                         |
| 6   | **Decided (owner, 2026-11-25): (b).** A promote that drops a variant a running change uses goes through; the control plane stops that change and records why in its audit trail, and the deploy plan warns at plan time. Being built; today the snapshot delivers such a change off and reports a problem, without stopping the record.       |
| 7   | (a): OpenFeature providers in Go and the browser over the dependency-free core.                                                                                                                                                                                                                                                               |
| 8   | (a): no approval unless the environment is protected or named `prod`/`production`.                                                                                                                                                                                                                                                            |

## Summary

A **Change** is the one object a team uses to alter what an app does without a
deploy. It has a default behavior, a new behavior (or several variants), and an
answer to "who gets it?": everyone (remote config), some people (a gradual
rollout to a stable percentage), or a comparison between versions (an A/B test
with one named success metric). One Change moves from preview to experiment to
rollout to everyone without being recreated and without re-instrumenting the
app.

The release declares what a Change **can** be (key, type, default, variants). The
control plane decides **who gets it** (audience, percentage, run status) as live,
audited state. The app evaluates locally from a versioned snapshot; there is no
per-request network call and no stored assignment table. Measurement reuses
product events. People and agents change the same record through the same
authorized API.

## Settled (owner, 2026-11-20)

1. One user-facing object, a Change, with "Everyone / Some people / Compare
   versions".
2. Cohorts are sticky: same unit, same outcome across requests, devices for that
   id, and ramps. Deterministic hash, no stored assignment table in v1,
   independent enrollment and variant buckets, runs freeze and re-salt, no
   usable id means default behavior recorded as **not enrolled**.
3. Three cohort questions in v1 and no segment query language.
4. Evaluation is local (Go backend SDK, web runtime) from a versioned snapshot.
   Polling or fetch first; streaming later as a "new revision" notification
   only. No load-balancer or proxy routing in v1.
5. Measurement reuses product events (`observe.Track`, web `track()`) into the
   per-org `product_events` table. Enrollment and exposure are distinct. The
   readout compares by assigned unit and says "inconclusive" when it must.
6. People and agents use one authorized API and one audit trail. Agents may
   draft, preview, verify and monitor; production launch and ramp need explicit
   human approval; stop and kill never wait for approval.
7. UI: an Experiments destination in the merged per-project nav with a "New
   change" action. Forge-only users get the same controls.
8. Growth consumes results and is never in the request path. Ads attribution
   comes later and stays separate from randomized assignment.

## 1. Problem and experience

Today a team that wants to try something on part of its traffic writes a
one-off. The landing page's hero test (landing-page `src/lib/experiments.ts`)
hashes a consented anonymous visitor id, reads arms from runtime config
(`EXPERIMENT_<NAME>`) and emits `experiment_exposure`. It works, and it is the
whole feature re-implemented per app: no audit, no ramp discipline, no readout,
nothing for an agent to call. Remote config, gradual rollouts and A/B tests are
usually sold as three products. They are one mechanism: evaluate a deterministic
function of (change, unit) against a policy. Only the policy and the amount of
measurement differ.

**What a person does.** In the Experiments page they press "New change", name it,
and answer one question, "Who gets it?":

| Answer           | Means                                                | Measured | Becomes (spec mode) |
| ---------------- | ---------------------------------------------------- | -------- | ------------------- |
| Everyone         | The new value for all units (remote config).         | No       | `everyone`          |
| Some people      | A stable percentage of an audience gets the variant. | No (v1)  | `rollout`           |
| Compare versions | Random split of enrolled units, one success metric.  | Yes      | `experiment`        |

The lifecycle runs forward on the same record: **preview** (a hashed list of
teammates sees the variant, nobody is measured; it is a stage, not a fourth
answer), **experiment** (a frozen run with a metric),
**rollout** (the winner, ramped 5% to 25% to 100%), **everyone**. Stop returns
every unit to the default at once.

**What an agent does.** The same calls as the UI through `ChangesService`:
create a change, draft its policy, preview it for the team, verify that the
deployed release declares it, read the readout, stop it. Launching or ramping in
production returns a pending approval for a person to decide
([section 4](#4-control-plane)).

**In the app,** code names the change and passes the default; the platform never
becomes a point of failure:

```go
on := client.BooleanValue(ctx, "new-checkout", false, evalCtx)
```

```ts
const on = client.getBooleanValue("new-checkout", false);
```

An unknown key, a missing snapshot or an invalid policy returns the caller's
default. The app never sees an error from evaluation.

## 2. Cohorts and stickiness

Three questions, and only three, in v1:

| Question             | v1 answer                                                                                                            |
| -------------------- | -------------------------------------------------------------------------------------------------------------------- |
| Who is eligible?     | Everyone; **my team** (a preview list); or one app-provided attribute with a list of values (`plan in [paid]`).      |
| What stays together? | The **unit**: `user` (default), `account`, or `visitor` (anonymous sites). Declared per change and frozen for a run. |
| How many?            | `percent_bp`, basis points of eligible units enrolled (0 to 10000).                                                  |

Evaluation is a pure function of the snapshot, the change key and the context.
The hash is MurmurHash3 x86 32-bit, `bucket = hash mod 10000`. Enrollment and
variant choice use different domain labels, so they are independent. The full
algorithm is [Appendix A](#appendix-a-evaluation-spec-v1) and is normative: the
Go and TypeScript implementations must produce identical results from one shared
file of golden vectors. The properties it guarantees, which tests must assert:

- the same input always gives the same output;
- raising `percent_bp` inside a run only **adds** units: the original 5% stay
  enrolled at 25%, and keep their variant;
- lowering and raising again returns the same units;
- editing the audience, the split or what a variant means starts a new run with a
  fresh salt, which re-randomizes everyone; the old run's results are untouched;
- `everyone`, `preview`, `no_unit` and `ineligible` never enroll;
- a unit with no id gets the default and is recorded as **not enrolled**, never
  as "control", so a missing id cannot dilute or bias an arm.

**Team preview** is a list of hashed unit ids in the snapshot
(`murmur3("forge.changes/v1|preview|key|unitId")`, 8 hex characters). A public
browser snapshot therefore carries no raw user ids. The hash is 32 bits and
unsalted per person, so it hides ids from a casual reader and not from someone
who can enumerate candidate ids; a preview list names a handful of teammates and
the variant they see, which is not sensitive. Preview units are never measured.

### Why no assignment table in v1

A table (or a per-request lookup) would make assignment exact and revocable, and
it would also need a network call or a replicated store on the hot path of every
app, offline behavior to specify, and a stored list of every user id ever
evaluated. The hash makes the common promises free: same unit, same outcome, no
call, nothing stored. The cost is what it cannot promise:

| Situation                                                       | Behavior                                                                                                                                                                                       |
| --------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A unit's id changes (anonymous visitor id, then signed-in user) | The two ids hash independently and the person can switch arms. Visitor-unit changes on a site with sign-in should use `user` once known; a person-level link is future work.                   |
| Teammates share one account                                     | With unit `user` they can see different variants. Unit `account` keeps them together; the app must pass an `account` attribute.                                                                |
| The audience attribute changes (free becomes paid)              | Eligibility is evaluated now. A unit that becomes eligible is enrolled on its first evaluation after that. A unit already enrolled stays in the run's results even if it stops being eligible. |
| A run is replaced                                               | Everyone is re-randomized by design. Use it for a changed experiment, not as a way to "refresh" a rollout.                                                                                     |

Revisit a stored assignment (sticky bucketing across an id change, exact
exclusion lists) when a real use needs it. It can be added beside the hash: a
lookup consulted before step 7 of the spec, with the hash as the fallback.

## 3. Architecture

```
 repo: forge.yaml `changes:`  --release shape-->  control plane: ChangesService
   (key, type, default,                              (audience, percent, run, audit)
    variants, unit, surfaces)                                   |
                                                                | snapshot (versioned JSON)
              +-------------------------------------------------+---------------------+
              | static site: /_forge/changes.json     hosted backend: mounted file      |
              |   written beside config.js            (or authenticated fetch)          |
              +-------------------------------------------------+---------------------+
                                                                |
        Go SDK / web runtime: Evaluate(snapshot, key, context) -> {variant, value, reason, run, enrolled}
                                                                |
                      enrolled evaluation --> flag_evaluated product event --> product_events
```

**Declaration (implemented: `changes.yaml`, see [../changes.md](../changes.md)).** A change's key, value type, default, variant names
and values, unit and surface (`web`, `server`) live in the repo and ship in the
release shape. The platform refuses to launch a variant the deployed release
does not know, and codegen can later give typed accessors. The shape precedent is
Funnels and Retention (`pkg/release/bundle.go`, `ShapeRetention` and
`ShapeFunnel`), not `forge.Analytics`, which never reaches the shape (forge
`kcl/render.k:3260-3297`). A new shape field needs a `BundleFeatureChanges`
constant and strip logic in `pkg/release/bundle_features.go`, because the shape
is strict-decoded and older control planes must not choke on it. Where the
declaration is written is an open decision
([section 8](#8-open-owner-decisions)); a sketch, not a settled syntax:

```yaml
changes:
  new-checkout:
    type: boolean
    unit: user
    default: control
    variants: { control: false, treatment: true }
    surfaces: [web, server]
```

**Delivery (proposed).** The platform writes the snapshot where the app already
reads config, instead of every app authenticating to a platform API.

| Runtime                    | Mechanism                                                                                                                                                                                                                                                            | State                                                       |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------- |
| Hosted static site         | The StaticSite operator writes `/_forge/changes.json` in the same pass that writes `config.js`, with no release (control-plane `internal/operators/staticsite/runtimeconfig.go:171-182`). Served `no-cache` with an ETag (`internal/staticorigin/headers.go:19-41`). | Mechanism exists; the file and the trigger are new.         |
| Hosted backend             | Either a platform-written ConfigMap mounted as a file, or an authenticated fetch.                                                                                                                                                                                    | **Open decision.** Backends have no live config path today. |
| Non-hosted (cluster, host) | An authenticated fetch of the same JSON with a platform-minted token, or a file the team writes.                                                                                                                                                                     | Follows the backend decision.                               |
| Dev (`forge env up dev`)   | forge serves a snapshot built from the declaration, defaults only, with a local override file.                                                                                                                                                                       | Forge-only.                                                 |

The snapshot is a separate JSON file, not a field inside `config.js`: `config.js`
is a blocking script the page cannot render without, and the JSON can be
refreshed asynchronously. The cost is a render-ordering question: the web runtime
must have the snapshot before it evaluates, or it returns the default and records
`unknown`. The runtime preloads it at startup and evaluates only once it has
loaded or failed; an unloaded evaluation never enrolls, so a slow fetch costs a
default render and no corrupted experiment. The browser snapshot contains only
changes declared for the `web` surface, and its variant values and audience lists
are public.

For backends the choice is: a **mounted file** refreshes within about a minute
with no new credential and no new egress rule (the operator already manages
ConfigMaps, and egress is default-deny with named allows,
`internal/operators/shared/networkpolicy.go:22`), but it is a new contract
between the platform and the Go SDK (a path named by a forge-declared variable
such as `FORGE_CHANGES_FILE`, read through a sanctioned env reader to pass
`internal/pkgguard`); a **fetch** works anywhere and needs a platform-minted
secret (modelled on `RELIANT_LLM_API_KEY`) and an egress allow. Proposed: file
for hosted, fetch for non-hosted.

**Freshness.** The web runtime fetches once per page load (conditional on the
ETag) and applies a new revision on the **next** load: a rendered page does not
flip variant under the user. Long-lived SPAs may re-poll no faster than every 15
seconds, visible tab only. The Go SDK re-reads the file or the fetch every 15
seconds. Streaming, later, only announces "revision N exists" so a client
fetches sooner.

**Stale and offline.** A client keeps the last good snapshot indefinitely. It
does not fall back to defaults because the snapshot is old: that would flip
everyone mid-run, which is worse than serving a stale policy. A snapshot with an
unknown `schema` is rejected and the last good one kept. With no snapshot ever
loaded, every evaluation returns the caller's default as `unknown`, not
enrolled. A stop reaches a client only when it next refreshes, so "stop" is as
fast as the polling interval and no faster; the UI says so.

**What "applied" means.** One word hides three states, and the UI shows them
separately:

| State         | Proven by                                                                                                                        |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| **Saved**     | The mutation committed as revision R in the control plane.                                                                       |
| **Delivered** | The platform wrote a snapshot with revision >= R to the static site or the workload (the operator records the written revision). |
| **Seen**      | An evaluation event carries `revision >= R` from the running app. Proves a client actually loaded it.                            |

**SDK shape (proposed, held for the owner).** The evaluation core in each
language is small and dependency-free: parse and validate a snapshot, evaluate,
return `{variant, value, reason, run, enrolled}`. Go lives in `pkg/changes`; web
in `web-runtime/src/changes.ts`, in the barrel and pure like `track.ts`
(importing the HyperDX SDK from the barrel would break frontends that never
install it). On top, **Forge Changes is an OpenFeature provider** in each
language, so call sites use the standard API with the default in code, and a team
can move to another provider without touching call sites. The provider is a thin
layer, structurally typed like `exposure.ts` so the web barrel gains no
OpenFeature dependency. Reasons map onto OpenFeature's (`rollout` and
`experiment` to `SPLIT`, `preview` to `TARGETING_MATCH`, `everyone` to `STATIC`,
the rest to `DEFAULT`), with the forge reason kept in flag metadata.

**Event.** The assignment record is the evaluation: the **first** evaluation of
an enrolled unit in a run emits the existing `flag_evaluated` event
(`web-runtime/src/exposure.ts`; forge `pkg/obsbuiltin/product.go` already reads
it), recorded before the treatment renders so a broken treatment cannot hide from
its own denominator. Proposed additional props, all strings: `run`, `unit`,
`reason`, `revision`, and `unit_id` for the account unit only (user and visitor
ids are already columns). A unit that is not enrolled emits nothing. A separate
"actually shown" exposure event is not in v1. Backend evaluation emits through
`observe.Track`, which needs `observe.WithActor` for a visitor id; the
cookieless `visitor.day` hash rotates daily and **cannot** be a sticky unit.

## 4. Control plane

`ChangesService` is a new service (`proto/services/changes/v1/changes.proto`),
not more rpcs on `DeployService`, which already carries about 60. It keys on
`deploy_environments.id`: "project X, env prod" resolves from `(org, project,
name)` once, and grants already bind to that id
(control-plane `db/migrations/20261109214331_org_member_env_grants.up.sql:31-40`).

**State.** Proposed tables, all new and backward-compatible: `changes` (identity,
state, audience, unit, percent, `revision`, current run), `change_revisions`
(append-only, an immutability trigger like `deploy_promotions`, one row per
mutation with actor and diff), `change_runs` (frozen audience, split, variants,
metric, window, minimum sample, start and end), `change_approvals`.

| Lifecycle state (UI) | Spec mode it produces                 |
| -------------------- | ------------------------------------- |
| draft                | `off`                                 |
| preview              | `preview` (only the listed teammates) |
| running experiment   | `experiment`                          |
| ramping rollout      | `rollout`                             |
| rolled out           | `everyone`                            |
| stopped              | `off`                                 |

**Revisioned mutations.** Every mutation carries `expected_revision` and fails as
a conflict when it is stale (the `expected_version` precedent in
`proto/services/org_policy/v1/org_policy.proto:18`). Each mutation writes its
`change_revisions` row in the same transaction, bumps the environment's snapshot
revision (the number in the snapshot), and queues delivery after commit. The
audit trail is that ledger; `internal/audit.Log` is fire-and-forget and is only a
mirror, and `forge/pkg/activity` carries the user-visible stream.

**Validation.** `Launch` and `Ramp` read the env's current shape through
`reconcilestore` (control-plane `internal/reconcilestore/deployment_records.go:123-190`,
the way `obspolicy.ProductEventsDaysFromShape` reads one key) and refuse a key or
variant the deployed release does not declare. The shape is derived server-side
at `RecordBundle`, so a caller cannot state one. A promotion that later drops a
variant a running change uses is an open decision.

**Rpcs (proposed).** `CreateChange`, `GetChange`, `ListChanges`, `UpdateChange`
(`// forge:custom`, takes `expected_revision`), `PreviewChange`, `RequestLaunch`,
`ApproveLaunch`, `RejectLaunch`, `SetRollout`, `StopChange`, `ListChangeRevisions`,
`GetDeliverySnapshot` (the authenticated-fetch fallback, with an ETag) and
`GetChangeReadout`.

**Grants.** Two new scopes, `changes:write` (draft, preview, stop) and
`changes:launch` (launch, ramp, approve), proposed. Scopes are a closed set owned
by forge (`pkg/accesstoken/scope.go:5-17`, mirrored by a DB `CHECK` that
`forge lint` compares), so the order is a forge PR, a control-plane migration
widening the `CHECK`, then `OrgScopes` and `EnvScopes` in `internal/orggrants` so
"launch in prod only" can be granted per environment. An unclassified scope is
silently clipped (`internal/orggrants/store.go:100-104`). Reads of a change and
its readout use `obs:read`, which every member holds.

**Approval (proposed).** An agent calls `RequestLaunch` or `RequestRamp`, which
writes a pending `change_approvals` row bound to (change, run, action, percent).
A person holding `changes:launch` for that environment decides it in the console,
with a step-up when the environment is `protected`. The server enforces this, not
the agent's tool layer, because the CLI path gives no approval card. Two details
matter. A `user_token` counts as a person in the `db:query` precedent
(`internal/handlers/app_database/rpc_query_database.go:187-217`), so an agent
holding a person's token would pass; the decision must require a console session
principal. And **stop and kill need only `changes:write` and never wait.**

## 5. Measurement

The metric is a user-defined product event plus a time window, **chosen before
launch** and frozen into the run. v1 supports one binary metric: did the unit
fire event E within W hours of its enrollment. Rates and sums come later.

| Concern                | Rule                                                                                                                                                                                                                                                   |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Who counts             | Intent-to-treat by **assigned unit**: every unit with a first `flag_evaluated` event in the run, in the arm it was assigned, whether or not it saw the change or did anything else. Deduped with `uniqExact(unit)`, first event per unit per run wins. |
| Enrollment vs exposure | Enrollment is the first evaluation. Not-enrolled, preview, `no_unit` and `ineligible` units emit nothing and are never "control".                                                                                                                      |
| Window                 | Only units whose full window has elapsed are counted; the rest are reported as "pending", never as non-converters.                                                                                                                                     |
| Sample-ratio mismatch  | A chi-square test of observed arm sizes against the declared split. Past a threshold the verdict is `invalid` and no effect is shown.                                                                                                                  |
| Uncertainty            | A confidence interval on the difference in rates, always shown. No bare "winner".                                                                                                                                                                      |
| Verdicts               | `better`, `worse`, `inconclusive`, `invalid`. A run has a minimum sample per arm declared at launch; before it, the readout is **interim**, labelled so, and never says `better` or `worse`. Fixed-horizon in v1; no sequential testing.               |
| Account unit           | The metric joins on the same id. v1 proposes requiring the `account` attribute to equal `org.id` so metric events carry it. Open detail.                                                                                                               |

The readout is a vetted `obsquery` template, `ExperimentReadoutQuery` in forge
`pkg/obsquery`, so the local and hosted answers agree, and it is served as
`GetChangeReadout` under `obs:read`, following `GetUserActivity`
(control-plane `internal/handlers/observability/rpc_get_user_activity.go:11-55`):
the handler runs the template as the org's read-only ClickHouse user, and the
SRM check and interval are computed in Go, never in the browser. Nothing needs a
new column; everything fits in `Props`.

Growth (`../growth`) reads finished results through the API and is never in the
request path. Ads and campaign attribution stay separate: a campaign tag can be
a metric filter later, and it never decides assignment.

## 6. UI and agent tools

**Experiments** is a project destination in the merged per-project left nav. The
Forge console and Reliant are one frontend (reliant `frontends/web`, host selects
the shell, `lib/shell.ts:1-48`), so one build in `components/Forge/Experiments/`
serves Forge-only users with no chat. The nav seam is
`registerConsoleNavEntry({id: "experiments", after: "product-analytics",
available})`, called beside the route, so an unregistered page has no row. It
exists only on the prototype branch `feat/web-project-shell-proto`, in none of
the open PRs; it has to land first, and the nav-merge chat (411eb83a) owns that.
The route stays behind a `ReliantWebConfig` flag (for example `changes_ui`) until
it works.

- **List page.** Changes with state, audience, percent, revision and a delivery
  indicator (saved, delivered, seen). Primary action: "New change".
- **Change page.** The "Who gets it?" control, runs, ramp and stop, the
  revision history, and the readout.
- **Result view.** Modelled on `Observability/ReleaseComparison.tsx`: the server
  computes verdict, SRM and interval; the console only renders. Per
  `web-styling`: forge-ui tokens, blue acts, green only for real success,
  `inconclusive` neutral, status as a dot or icon.

**API for the console.** `controlplane.v1.*` clients come from the vendored
proto tree, so reliant syncs control-plane's `proto/public-api.txt` after
`ChangesService` merges, then adds `services/controlPlane/changes/` (with a demo
fixture, as observability has) and `hooks/changes-queries.ts`.

**Agent tools** model `internal/llm/tools/machine_tools.go`: `list_changes` and
`get_change_readout` (read-only), and `manage_change` with actions draft,
preview, verify, launch, ramp, stop. Launch and ramp raise an approval card
hash-bound to (change, run, action, percent); stop raises none. A
`ChangeController` interface in `internal/app` calls `ChangesService` with the
user's identity, so the tool and the UI hit the same API and write the same
ledger. An unattended run may draft, preview and stop only.

## 7. Alternatives considered

| Alternative                                       | Why not now                                                                                                                                                                                                 |
| ------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Separate flags, rollouts and experiments products | Three objects for one mechanism; a flag cannot become an experiment without recreation and re-instrumentation, which is the thing the owner ruled out.                                                      |
| Proxy or load-balancer routing                    | Needs per-request platform involvement and works only for whole-page differences; the interesting changes are inside the app. Not ruled out for later, as a delivery target of the same policy.             |
| A stored assignment table                         | Exact, and costs a lookup or a replicated store per evaluation plus a list of every user id. Deferred ([section 2](#why-no-assignment-table-in-v1)); the hash can keep working beside it.                   |
| A hosted third party (LaunchDarkly, Statsig)      | Sends user ids and events off-platform, duplicates the product-event pipeline, and cannot tie a variant to forge's release shape. The OpenFeature provider keeps this option open for a team that wants it. |
| Growth as the assignment service                  | Puts a marketing service in every app's request path. Growth consumes results only.                                                                                                                         |
| Per-env declarations (`forge.Change` in KCL)      | Closest to the shape, but a change's variants should not differ between envs, so every env would carry an identical copy. Kept as an option in section 8.                                                   |

## 8. Open owner decisions

Per project rules, the owner decides architectural choices; each row gives the
realistic options, a recommendation, and what it blocks. The owner decided 1, 2,
3 and 6 on 2026-11-25, and the released code settled the rest; see
[Where it stands](#where-it-stands-2026-11-25).

| #   | Decision                                             | Options                                                                                                                                                                 | Recommendation                                                                                                                                                      | Blocks                                                     |
| --- | ---------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| 1   | Where a change is declared                           | (a) `forge.yaml` `changes:` section; (b) a project-level KCL file; (c) per-env `forge.Change` in KCL; (d) call-site code with a build-time extractor.                   | (a): simple values belong in `forge.yaml` (start-anywhere decision 8), one declaration for all envs, works in repos with no KCL. Lowered into the shape either way. | Shape field, validation, codegen, the declaration skill.   |
| 2   | Backend delivery                                     | (a) mounted ConfigMap file; (b) authenticated fetch with a platform-minted token; (c) env var (not live).                                                               | (a) for hosted, (b) for non-hosted. A file needs no credential or egress rule; it is a new SDK and platform contract, so it is yours to approve.                    | Go SDK source, `changesinject.go`, the backend dogfood.    |
| 3   | Who may launch or ramp in prod                       | (a) pending approval row decided by a console-session person; (b) people-only mutations, agents only draft; (c) any holder of `changes:launch`, including agent tokens. | (a). Brief item 6. A `user_token` must not satisfy the decision, unlike the `db:query` precedent, and a protected env adds a step-up.                               | `RequestLaunch`/`ApproveLaunch`, agent tools, prod launch. |
| 4   | Experiments page: per app or per project             | (a) per app, matching the console's app-scoped nav (`/forge/p/<app>`); (b) one project-level page listing changes across apps.                                          | (a) first; the data is keyed by environment already, so (b) is a view over the same list later. The brief says "project-scoped"; the nav is app-scoped.             | Route shape, nav registration.                             |
| 5   | New scopes in forge's public closed set              | (a) add `changes:write` and `changes:launch` (a semver minor, forge PR, migration, `OrgScopes`/`EnvScopes`); (b) reuse an existing scope such as `deploy:*`.            | (a). Reusing deploy scopes would let anyone who can deploy change who gets a feature, and could not be granted separately.                                          | Authorization for every mutation.                          |
| 6   | A promote that drops a variant a running change uses | (a) refuse the promote; (b) allow it and the control plane stops the change and says so; (c) allow it and serve the default for that change.                            | (b), with a warning at plan time. Refusing blocks a rollback of the release; serving a variant the code no longer knows is wrong.                                   | `Promote` interaction, rollback behavior.                  |
| 7   | OpenFeature as the SDK shape                         | (a) forge SDK is an OpenFeature provider over a dependency-free core; (b) a forge-specific API only.                                                                    | (a), held until you confirm.                                                                                                                                        | The provider packages only; the core is unaffected.        |
| 8   | Approval for non-prod environments                   | (a) none; (b) same as prod.                                                                                                                                             | (a), unless the environment is `protected`.                                                                                                                         | Whether agents can run a staging experiment unattended.    |

## 9. Rollout plan

**Wave 1: the evaluation core (this proposal).**

- Appendix A as the spec, plus `testdata/changes/vectors.json` loaded by both a
  Go test and a vitest test: hash vectors, ramp monotonicity, ramp down then up,
  independent buckets, new salt re-randomizes, no id means not enrolled, invalid
  policy degrades to the default.
- `pkg/changes` (`doc.go`, `snapshot.go`, `eval.go`, a file/poll `source.go`) and
  `web-runtime/src/changes.ts`, with a barrel export and a header comment so
  `forge project libraries` lists both. Both must pass `internal/pkgguard`,
  `deadcodeguard` and the web-runtime `published-surface` test.
- The `observability/changes` skill and evidence for the
  `controlled-rollouts-and-experiments` benefit. Nothing needs the control plane
  and nothing ships to users as a platform feature yet.

**Wave 2, as separate slices** (each shippable alone; order mostly follows the
dependencies):

1. Declaration, shape field, `BundleFeatureChanges` and its strip logic
   (decision 1).
2. `ChangesService`, migrations, scopes, revision ledger, validation against the
   shape (decision 5; the forge scope PR comes first).
3. Static-site delivery: `/_forge/changes.json`, force-reconcile on a new
   revision, and a check of the CDN purge list (`DefaultEntrypoints` lists
   `/config.json`, not `/config.js`; the serving headers make the purge
   secondary, but verify).
4. Evaluation events: the extra `flag_evaluated` props and the Go emitter.
5. Readout: `ExperimentReadoutQuery`, `GetChangeReadout`, SRM and intervals.
6. UI behind a flag, after the nav seam lands; vendored proto sync; the result
   view.
7. Agent tools and approvals (decisions 3 and 8).
8. Backend delivery (decision 2), OpenFeature adapters (decision 7), typed
   accessor codegen modelled on `frontend_config_ts.go`.

**Dogfood.** First, the landing-page hero test moves onto Changes, replacing
`experiment_exposure` with `flag_evaluated` and its hand-rolled hash, with the
arm still overridable from runtime config in dev. It keeps today's limit:
anonymous visitors have a stable id only in the consented `full` tier
(`visitorId()` in `web-runtime/src/attribution.ts`), so the test measures
consented traffic. Second, one ramp on a signed-in Reliant feature with unit
`user`, which exercises delivery to a hosted app, the approval flow and the
readout on data that exists.

**Explicitly out of v1:** proxy or load-balancer routing; automatic ramps and
automatic stop on a metric; a segment query language; multi-armed bandits;
mutual-exclusion layers between experiments; sequential testing; continuous
metrics; a stored assignment table; a measured readout for plain rollouts;
streaming beyond a "new revision" notice.

## Appendix A: Evaluation spec v1

Normative. Go and TypeScript must return identical results.

**Hash.** MurmurHash3 x86 32-bit, seed 0, over the UTF-8 bytes of the input
string, with no normalization. `bucket(s) = murmur3_32(s) mod 10000`, an integer
0..9999. Percentages are basis points.

**Inputs.** A snapshot, a change key, and an evaluation context (`targetingKey`
string plus string attributes).

**Unit id** by the change's `unit`: `user` is `targetingKey`; `account` is the
context attribute `account`; `visitor` is the context attribute `visitor`.
Empty or missing means reason `no_unit`, the default value, not enrolled.

**Order of evaluation for a change:**

1. Key absent from the snapshot: `unknown`, the caller's default.
2. `mode: off`: `default` for everyone, preview list included (the declared
   default variant if set, else the caller's default). `off` is the kill switch.
3. Preview: if `preview` lists the unit, return that variant, reason `preview`,
   **not enrolled**. A listed unit is matched by
   `preview_hash = lowercase hex, 8 characters, of murmur3_32("forge.changes/v1|preview|" + key + "|" + unitId)`.
   A missing unit skips this step. It applies under every mode but `off`, so the
   team keeps its variant during an experiment and is never measured.
4. `mode: preview`: `default` for every unit the list did not match. This is
   the "team preview only" state.
5. `mode: everyone`: `variant`, reason `everyone`, not enrolled.
6. Unit id missing: `no_unit`.
7. Audience (optional), `{attribute, in: [values]}`: the context attribute value
   must be in the list (the attribute `targetingKey` is allowed), else
   `ineligible`, the default, not enrolled.
8. Enrollment: `e = bucket("forge.changes/v1|enroll|" + key + "|" + salt + "|" + unitId)`;
   enrolled iff `e < percent_bp`, else `not_enrolled`, the default.
9. `mode: rollout`: `rollout_variant`, reason `rollout`, enrolled.
   `mode: experiment`: `v = bucket("forge.changes/v1|variant|" + key + "|" + salt + "|" + unitId)`;
   walk `split` in listed order with cumulative `weight_bp` (sums to 10000); the
   first range with `v < cumulative` wins; reason `experiment`, enrolled.

**Snapshot JSON (v1).**

```json
{
  "schema": "forge.changes/v1",
  "revision": 42,
  "changes": {
    "new-checkout": {
      "variants": { "control": false, "treatment": true },
      "default_variant": "control",
      "delivery": {
        "mode": "experiment",
        "unit": "user",
        "run": "r_8f2c",
        "salt": "r_8f2c",
        "audience": { "attribute": "plan", "in": ["paid"] },
        "percent_bp": 2000,
        "split": [
          { "variant": "control", "weight_bp": 5000 },
          { "variant": "treatment", "weight_bp": 5000 }
        ],
        "rollout_variant": null,
        "variant": null,
        "preview": [{ "unit_hash": "1a2b3c4d", "variant": "treatment" }]
      }
    }
  }
}
```

**Validation.** An unknown `schema` rejects the whole snapshot and the last good
one is kept. A change whose delivery names an undeclared variant, whose split
does not sum to 10000, or whose `percent_bp` is outside 0..10000 evaluates as
`invalid` and returns the caller's default; other changes are unaffected.
Variant values are JSON (boolean, number, string, object).

**Result.** `{variant, value, reason, run, enrolled}`. Reasons: `unknown`,
`default`, `invalid`, `preview`, `everyone`, `no_unit`, `ineligible`,
`not_enrolled`, `rollout`, `experiment`.

**Reference vectors** (change `new-checkout`, unit `u_123`, salt `r_8f2c`;
computed with an independent MurmurHash3 implementation checked against the
published vector `murmur3_32("hello") = 0x248bfa47`). The golden file adds more:

| Input string                                             | murmur3_32 | Value                   |
| -------------------------------------------------------- | ---------- | ----------------------- |
| `forge.changes/v1\|enroll\|new-checkout\|r_8f2c\|u_123`  | 3681668612 | bucket 8612             |
| `forge.changes/v1\|variant\|new-checkout\|r_8f2c\|u_123` | 438083969  | bucket 3969             |
| `forge.changes/v1\|preview\|new-checkout\|u_123`         | 0x943cb63b | `preview_hash` 943cb63b |

With `percent_bp: 2000`, `u_123` is not enrolled (8612 >= 2000) and is enrolled
from `percent_bp: 8613` up; its 50/50 variant bucket 3969 selects the first arm.

**Separator.** The hash inputs join fields with `|`, so validation rejects a
change key or salt that contains it. The unit id is last and may contain it.
