---
name: observability/changes
description: Forge Changes — remote config, gradual rollouts and A/B tests as one model. Evaluate a change in Go (pkg/changes Store, or the OpenFeature provider in pkg/changes/openfeature) or the browser (loadChanges, forgeChangesProvider), pick the unit that stays together (user, account, visitor), preview it to your team, and what each evaluation records (flag_evaluated with run, unit, reason, revision).
---

# Forge Changes

A **change** is one object with a default behavior, one or more named
variants, and an answer to "who gets it?" — its delivery mode:

| Mode | Who gets a variant | Is it |
|------|--------------------|-------|
| `off` | nobody, previews included: the kill switch | |
| `preview` | only the units on the change's preview list | a team preview |
| `everyone` | every caller, one variant | remote config |
| `rollout` | a stable percentage of an audience, one variant | a gradual rollout |
| `experiment` | a stable percentage, split across variants | an A/B test |

One change moves from preview to experiment to everyone without being
recreated or re-instrumented.

**Status: beta.** The evaluation SDK below ships, and so does the declaration
(`changes.yaml` at the project root, shipped in every release, with generated
typed accessors: `docs/changes.md`). Delivering each environment's policy to
every hosted app, and steering it from the console's Experiments screen, are
still rolling out; until the platform delivers a snapshot, every call site gets
the default written in code, so it is safe to ship evaluation code now.

## Declare it

```yaml
# changes.yaml
version: 1
changes:
  new-checkout:
    type: bool # bool | string | number | json
    default: control
    variants: { control: false, treatment: true }
```

`reliant forge generate` writes one accessor per change
(`internal/changes/changes_gen.go`, `src/lib/changes_gen.ts`) whose fallback is
the declared default. The control plane refuses to launch a variant the
environment's deployed release does not declare.

## Evaluate in Go

The app reads the environment's policy snapshot from a file the platform keeps
current (on Reliant hosting, `FORGE_CHANGES_FILE` names it). Evaluation is
local: no network call per request.

```go
store := changes.OpenFile(ctx, os.Getenv(changes.EnvFile), changes.Options{})
defer store.Close()

ec := changes.EvalContext{TargetingKey: userID, Attributes: map[string]string{"plan": plan}}
if changes.Bool(store.Evaluate(ctx, "new-checkout", ec), false) {
	// the new checkout
}
```

`OpenFile` never fails: a missing file (or an empty path) is an empty
snapshot, so every call site gets the default written in code; a file that
does not parse keeps the last good snapshot and logs once. The file is re-read
every 10s. `String`, `Float64` and `Decode` read the other value types.

### Not on Reliant hosting

An app on its own cluster or host fetches the same snapshot from the control
plane, with an access token carrying `changes:read` (it reads one
environment's snapshot and nothing else). That needs a control plane that
admits `changes:read` (Reliant cloud from the release that carries
control-plane#3713); until then, never give an app a broader token to read it.
On Reliant cloud create the token in the web app (Settings → API tokens): it
refuses token administration to a CLI login. Elsewhere, `reliant forge cloud
token create --env <env> --name <app>-changes --scopes changes:read
--bind-env <environment-id>`; binding to one environment needs a control
plane with control-plane#3713, and an unbound token is org-wide. Set
`FORGE_CHANGES_URL` (the control plane's base URL),
`FORGE_CHANGES_ENVIRONMENT_ID` and `FORGE_CHANGES_TOKEN`, allow egress to the
control plane on 443, and open the store with
`changes.OpenFromEnv(ctx, os.Getenv, changes.Options{})`: it reads
`FORGE_CHANGES_FILE` when set (hosting), else fetches every 15s, keeping the
last good snapshot and backing off on errors. The browser never holds the
token: serve `/_forge/changes.json` from your server with
`fetchChangesSnapshot` (`@reliantlabs/forge-web-runtime/changes-server`).
Details: forge `docs/changes.md`, "Apps not on Reliant hosting".

Prefer the OpenFeature API? Register the provider and keep call sites
vendor-neutral:

```go
of.SetProviderAndWait(forgeof.NewProvider(store)) // forge/pkg/changes/openfeature
on, _ := of.NewDefaultClient().BooleanValue(ctx, "new-checkout", false,
	of.NewEvaluationContext(userID, map[string]any{"plan": plan}))
```

## Evaluate in the browser

```ts
import { loadChanges, changeBool } from "@reliantlabs/forge-web-runtime";

const changes = await loadChanges(); // GET /_forge/changes.json; never rejects
if (changeBool(changes.evaluate("new-checkout", { targetingKey: userId }), false)) { … }
```

A page reads the snapshot once; a new revision applies on the next page load,
so a treatment never switches mid-interaction. With OpenFeature's web SDK:
`OpenFeature.setProvider(forgeChangesProvider(await loadChanges()))`.

## Units: what stays together

A change names its unit. The same unit always gets the same outcome, and
raising a percentage only adds units.

- `user` — the context's targeting key (the signed-in user id).
- `account` — the `account` attribute (an org or team id).
- `visitor` — the `visitor` attribute: an anonymous id the app holds with
  consent (the web runtime's `visitorId()` exists only in the `full`
  analytics tier). The rotating cookieless visitor-day is not a unit.

A context with no id for the unit gets the default and is never enrolled —
not counted as a control.

## Previews

A preview list names units by hash (never raw ids, since browsers read the
snapshot) and the variant each sees. It applies under every mode but `off`, so
your team keeps its variant while an experiment runs, and a previewed unit is
never measured.

## Deploying a release that drops a variant

A deploy whose release no longer declares a variant a running change
delivers, or the change's whole key, goes through, and the control plane
stops that change (everyone gets the default) and records why. The plan
`forge env deploy` prints says so first, under `running changes this deploy
stops`, as a `[warn]` line naming the change, its run and the variant; `--json`
carries it as `deploy_plan.change_warnings`. It is a warning only: no
acknowledgement, no exit code. To keep the change running, keep the variant
declared in `changes.yaml` until the change ends.

## What gets recorded

Every evaluation through a store records the product event `flag_evaluated` —
the same event `trackExposure` and other flag providers record — with
`flag`, `variant`, `provider=forge`, `run`, `unit`, `reason` and `revision`,
plus `account` (the `account` attribute the change hashed) when its unit is
`account`: a user or visitor id is a column of every event, an account id is
not, so a readout counts an account-unit run by it on an event that names no
org (in the browser, only in the tiers that carry ids). Only results a readout or a dashboard can use: enrolled ones (`rollout`,
`experiment`, the unit's assignment, recorded before the app acts on it) plus
`preview` and `everyone`. Once per unit, change, run and variant per day per
process. Readouts count only enrolled rows of the run, by assigned unit. In
Go, `Options.OnEvaluate` replaces the recording.

Measure the outcome with your own product events (`observe.Track`, `track()`):
see `observability/product-events`.

## Local dev

Point `FORGE_CHANGES_FILE` at any snapshot file and edit it: the Go store
picks the change up within one poll. The snapshot schema is
`forge.changes/v1`; `changes.ParseSnapshot` is its validator.
