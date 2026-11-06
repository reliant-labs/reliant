# 2026-11-05: forge GetTopology answered 503 while the chosen laptop slept

## What happened

Prod, 2026-11-03..05: every `reliant.v1.ForgeService/GetTopology` of the owner's
control-plane console answered 503 (157 of 157): `daemon command
forge.topology failed: unavailable: no daemon connected for user`. The console
had selected the Reliant project created on a laptop, checked out only on two
laptops, both asleep, while three connected cloud machines held a checkout of
the same forge project under a second Reliant project. Since #2016 every forge
command is pinned to the machine holding the project's checkout, so nothing
else was asked.

## The fix (reliant#2233)

`forgeReadDispatch` (`internal/handlers/forge/forge_fallback.go`): when the
chosen machine is not reachable (`daemonerr.Unreachable`), a topology read is
asked of the project's other checkouts, then of the caller's own projects that
are the same github.com repository and declare the same forge.yaml name. Env
status, verify, writes, deploy status and secrets never move. Code only: no
migration, no proto or public-surface change.

## Post-deploy verification

After the release carrying #2233 is deployed to prod:

1. `reliant forge obs check prod --wait --json` exits 0 (the release did not
   regress the api server).
2. With the owner's laptops asleep and a cloud machine holding a control-plane
   checkout connected, open the control-plane console's forge topology view on
   the laptop-created project. It loads instead of answering 503.
3. The `reliant-api-server` logs for that request carry
   `forge: the checkout's machine is not reachable; another checkout of the
   forge project answered` with `command=forge.topology`, the laptop's
   `daemon_id` and a cloud machine's `answered_daemon_id`.
4. The GetTopology 503 rate (`unavailable: no daemon connected for user`) for
   that user drops to zero while a cloud checkout is connected.

If step 2 still answers 503 and step 3 logs nothing, the fallback found no
eligible checkout: confirm both projects' `remote_url` name the same
github.com repository and both carry the same `forge_project_name`.

## Rollback

Revert the #2233 merge commit on main and ship it through `rc-train`
(`release:asap`). The change has no migration and no schema, proto or stored
state, so a revert (or redeploying the previous release) restores the prior
behaviour exactly: topology goes back to the chosen machine alone, and the
only effect is that the 503 returns while that machine is asleep.
