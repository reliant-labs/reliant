---
name: deploy/release
description: What `forge env deploy <env>` does end to end — the two forms (ship this checkout vs deploy a named release), the confirm gate and exit 5, what the plan diffs against, scoped deploys, where releases are recorded (control plane vs machine ledger, FORGE_LEDGER=machine), the live preflight, pre-rollout migrations, and verify + recover by rolling forward (no rollback).
---

# Deploying a release

## Two forms, and the difference is whether you name a version

```
forge env deploy prod             # SHIP THIS CHECKOUT: build → push → cut → plan → confirm → apply → wait
forge env deploy prod --yes       # the same, in CI: "I read the plan" (no TTY to prompt on)
forge env deploy prod v1.7.1      # deploy a release that already exists; builds nothing
forge env deploy prod --dry-run   # render/print without applying
forge env deploy prod v1.7.1 --target item   # one workload of a release that exists
```

With NO
version, deploy does all the deployment bits: it builds the env's artifacts at
the current checkout exactly as `forge env build` does, pushes them, cuts a
release named `<YYYYMMDD>.<HHMMSS>-<tree12>` for this tree, records the shape,
plans, confirms, then promotes and waits. If a release already holds this
checkout — whatever it is named, including one `forge env build --release` cut
— it is deployed and NOTHING is built: it must record this clean tree and this
forge version, cover every artifact the env declares, and still resolve every
image it pins. The first line says which: `reusing release <v>` or
`cutting new release <v> because …`. Naming a version builds
nothing — it deploys a release that is already cut, which is also how you make
a spec-change deploy: name the version the env already runs. A version nobody
cut errors with `forge env deploy <env>` as the fix, never `--no-build`.
A cut that pushed and died before its record: re-run it (`deploy/interrupted-cut`).

## Nothing is written until you confirm

A promotion IS the deploy — the
converger picks it up within minutes — so the plan is printed and approved
BEFORE the write. Three ways through the gate: **interactively**, where the
default is no; **`--yes` in CI**, meaning "I read the plan" (the plan is still
printed into the job log above the write); and **`--plan-only`**, the first
stage of a two-stage pipeline. With no terminal and no `--yes` the command
refuses with exit **5** (`plan_unconfirmed`), having built, pushed and cut but
written no promotion — so approving it afterwards needs no rebuild. A CI deploy
missing `--yes` is the most common cause of exit 5.

**The plan diffs against the last SUCCEEDED apply, never the declaration**,
and whoever applies the env computes it: the control plane for an env it
converges, forge for one it applies itself (a file-ledger env, or a
`forge.ControlPlane` env that `converges nothing`). Every successful apply
records the bundle it shipped, so an unchanged config plans clean. Reading a
plan — `object_removed`, field paths, secret presence, `no recorded config`:
`deploy/plan`.

**A scoped deploy needs a release.** `--frontends-only` and `--target` ship
part of the env, so forge refuses them when no version is named: a no-version
deploy cuts a release over every artifact, and cutting one that ships only
some would record a release that does not describe what is running. Name the
version (`forge env deploy prod v1.7.1 --frontends-only`) or deploy
everything. `--dry-run` / `--explain` cut nothing and are unaffected.

`build` and `deploy` also record that shape on the control plane, so a console
can read an env with no daemon online.

## Where releases and promotions are recorded

An env declaring
`forge.ControlPlane` records on that control plane; every other env in this
machine's ledger (`$FORGE_LEDGER_HOME`, default `~/.forge/ledger`).
`forge ledger where <env>` says which, and why. A hermetic script or test that
renders or imports a real env sets **`FORGE_LEDGER=machine`**: every forge it
starts stays off the declared control plane, and `forge ledger where <env>
--json` reports `"override": "FORGE_LEDGER=machine"` to assert it.
`$FORGE_LEDGER_HOME` alone only relocates the machine ledger — a declared env
still reads and writes its control plane. Control plane down: `deploy/offline-ledger`.

## How each workload ships, and the preflight

Each workload deploys through its runtime: cluster workloads are applied to
the kubectl context the ClusterTarget names (forge refuses when it is
missing rather than using the current one), after a live preflight that
every referenced Secret key and image exists. Hosted workloads publish to
the env's control plane, admitted there under the Restricted profile.

Every refusal that does not depend on the apply runs in that preflight,
BEFORE the promotion is recorded and before anything ships: the Secret/image
check, a pinned env, a frontend that cannot ship (mock build, a forge-owned
dotenv, no `firebase` CLI installed), and what the control plane will converge.
The plan's `converges` line says the last one: `nothing` for an env that
declares `forge.ControlPlane` but binds no tier to it (forge's own apply is
the whole deploy), or its hosted tiers, naming any this deploy publishes for
the first time or leaves unpublished. If a deploy still fails part-way, its
report lists each stage that shipped (`shipped …` / `PARTLY …`); it never
claims nothing shipped once a cluster or frontend host has changed.

## Migrations run BEFORE the rollout

A standalone `kind = "job"` workload is **pre-rollout by default**: `forge env
deploy` applies it and waits for it to COMPLETE before any Deployment in the
same cluster group. If it fails the deploy stops with no workload changed,
printing the exact `kubectl logs` command. The scaffolded `migrate` goes
further — `before = [fw.BEFORE_ALL]` runs it as an initContainer on every
workload, so no new pod serves against an old schema. A job needing this
release's workloads running declares `deployPhase = "post-rollout"`.

A bundle a reconciler (Flux) applies states its order as trees per cluster ×
unit × phase: `deploy/delivery-units`.

## Verify + recover — roll forward, never back

After every deploy confirm the env runs what it claims: `forge env status
<env>` — bound release, running digests, rollout, health, gates, ledger.
Exit 2 + `ledger BEHIND` = pull first.

**There is no rollback command, and that is deliberate.** A rollback claims to
undo a release and it cannot: by the time you would run it the release has
applied its migrations and written rows in the new shape. So forge has no
rollback flag, no `rollback_cmd`, no `kubectl rollout undo` path. Recovery is
always a **new release that rolls forward** — `forge env deploy prod` with the
fix in the tree, or `--plan-only` first to read the plan.

A release deploy compare-and-sets against the plan's read: exit **3** = the env
moved meanwhile, nothing written; never retry blind. health_gate_*:
`deploy/delivery-policy`. CI recipe: `forge env deploy --help`.

A failed deploy changes nothing to undo: the pre-rollout gate stops a bad
migration before any workload changes, and a workload that never becomes ready
leaves the previous ReplicaSet serving. Binding an env to an OLDER release is
an ordinary promote labelled `direction BEHIND`; see `db/deploy-migrations`.
