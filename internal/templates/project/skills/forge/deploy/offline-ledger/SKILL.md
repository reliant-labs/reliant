---
name: deploy/offline-ledger
description: Break-glass for an env whose release ledger is a control plane that is unreachable — `forge env deploy <env> --offline-ledger [--to previous|<release>]` redeploys or ROLLS BACK to a release in this machine's offline copy, by its recorded digests, builds nothing, queues the promotion and replays it on the next successful ledger contact. Use when `forge env deploy` cannot reach the control plane (typically a bad release took it down), and to warm the copy in advance.
---

# Break-glass: control plane down

An env that declares `forge.ControlPlane` keeps its release ledger there. When
the control plane is unreachable — typically because a bad release took it
down — `forge env deploy` cannot read what the env runs, and
`FORGE_LEDGER=machine` is no way out (it trips the unimported-ledger refusal
and holds none of the live history). `--offline-ledger` deploys from a local
copy of the ledger instead.

## Warm the copy first — once per operator machine

While the control plane is up:

```
forge env status prod     # any successful ledger read or write refreshes the copy
```

The copy holds the env's last 5 promotions (`FORGE_OFFLINE_LEDGER_WINDOW`, max
50) with each release's recorded digests, under `$FORGE_LEDGER_HOME`, per
project and env. A machine that never made a successful read has none and is
told so. `forge env status prod` lists what the copy would let you deploy
(`offline_cache` in `--json`).

## During the outage

```
forge env deploy prod --offline-ledger --to previous --yes   # roll back to the release before the current one
forge env deploy prod --offline-ledger --to v1.7.12 --yes    # or any release in the copy (a promotion id works too)
forge env deploy prod --offline-ledger --yes                 # redeploy the current release
```

It deploys the cached release by its recorded digests: nothing is built, the
control plane is not contacted, and the same plan/confirmation gate applies
(`--yes` in CI). It prints the copy's age and what it will replay. A release
that is not in the copy is refused with the list of what is, because there are
no recorded digests to pin it to. It refuses an env whose hosted tiers or
hub-converged clusters the control plane applies, and it is the only thing that
bypasses the unimported-ledger refusal.

## After the outage

The deploy queued its promotion record (with the apply's verdict). The next
successful ledger read — any `forge env status prod` — replays it idempotently,
as a compare-and-set against the promotion the copy named: if someone promoted
something else while you were offline, the record is refused, reported and
stays queued, never overwriting them. Several offline deploys in one outage
replay in order.

Rehearse it on dev: stop dev's admin-server, then `forge env deploy dev-k8s
--offline-ledger --to previous`. It is a release-checklist item.
