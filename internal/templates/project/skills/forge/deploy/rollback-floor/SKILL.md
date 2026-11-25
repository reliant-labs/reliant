---
name: deploy/rollback-floor
description: Rollback floors — "never deploy anything older than X to this env". `forge env floor show|set|lower|clear|lift`, `forge env deploy <env> <v> --floor "<why>"`, and the declared floors a promote raises from a forge:breaks-rollback migration or a deploy/rollback-floors/<id>.yaml declaration. Use when a deploy is refused with below_rollback_floor (exit 1), to make an env roll-forward-only, or to declare that an API, feature or data change cannot be rolled back past.
---

# Rollback floors

A **rollback floor** refuses every deploy of a release older than it, to one
environment. A deploy below a floor is refused before anything is built,
confirmed or written: `promote refused (below_rollback_floor)`, **exit 1**.
There is no `--force`; the escape is to lower or lift the floor, which is its
own recorded act with a reason.

## Two kinds

| kind | admits a target that… | set by |
|---|---|---|
| `order` | was not cut before the floor's release (and, when git can tell, whose commit is not an ancestor of the floor's) | `forge env floor set`, `forge env deploy --floor` |
| `declared` | carries every declaration the floor requires | automatically, when the env is promoted to a release carrying a declaration no floor requires yet |

Order floors are about time ("v1.8 corrupts invoices; never again"). Declared
floors are about what the env's schema, API or data now IS, so a release cut
*later* from a branch without the declaration is still refused, and a release
cut *earlier* that carries it (skip-ahead) is admitted.

## Declaring what cannot be rolled back past

- **Schema:** a migration with `-- forge:breaks-rollback <why>` (`db/deploy-migrations`).
- **API, feature, data:** a checked-in `deploy/rollback-floors/<id>.yaml`:

  ```yaml
  kind: api        # api | feature | data
  reason: "v2 webhook payloads; the v1 receivers were removed"
  ```

The cut records every declaration in the release's manifest; a malformed file
refuses the cut. Never delete a released declaration file: later releases must
keep carrying it to stay deployable where its floor stands.

## The commands

```
forge env floor show prod [--all] [--json]          # active floors (--all: lifted too)
forge env floor check prod v1.8.3 [--json]          # would they admit it? reads only: exit 0 / 1 / 2
forge env floor set prod v1.9.0 --reason "…"        # order floor at a release
forge env floor set prod --current --reason "…"     # roll forward only from what prod runs now
forge env deploy prod v2.1.0 --floor "…"            # deploy, and set a floor at it, in one write
forge env floor lower prod v1.8.0 --reason "…"      # lift order floors above v1.8.0, set one at it
forge env floor clear prod --reason "…"             # lift every order floor
forge env floor lift prod flr_8c1e --reason "…" --acknowledge <source>:<id>   # per declaration
```

Lifting a **declared** floor must acknowledge every declaration it requires
(`floor show` prints them; a refused lift prints the exact `--acknowledge`
flags). Typical reason: the rollout that raised it failed before migrating.

Anyone who may promote may change floors, machine tokens included. Every
change records its principal and reason on the ledger and in the activity
stream (`set_floor`, `lift_floor`). `forge env status` shows the active floors;
its `--json` carries `rollback_floors` and `rollback_floors_supported` (a
control plane that predates floors is `null`/`false` — unknown, not none).

## Backends

The control plane enforces floors in Promote under the env's row lock, with a
database trigger behind it. The machine ledger enforces them in its file lock.
`--offline-ledger` judges the floors its copy recorded and refuses `--floor`
(floors change only on the control plane). A copy whose floors are unknown
(it predates them, or they could not be read at its last refresh) refuses the
deploy with exit 2: refresh it with `forge env status <env>` while the control
plane is up. A control plane without floors is recorded as having none.
`--floor` is refused against a
control plane that does not advertise floors, because it would silently drop
the field.
