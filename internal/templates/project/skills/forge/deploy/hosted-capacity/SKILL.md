---
name: hosted-capacity
description: The hosted capacity pre-flight and QUEUED deploys — what `forge env deploy` and `forge env build --push` check with the control plane before building, which answers queue the deploy (exit 7, waiting on billing) and which refuse it, and the Unimplemented fallback.
---

# Hosted capacity pre-flight, and queued deploys

`forge env deploy <env>` (every form, including `--plan-only` and
`forge env deploy <env> <release>`) and `forge env build <env> --push` /
`--release` on an env with hosted workloads ask the control plane
(`DeployService.CheckDeployCapacity`, read-only) whether the org may run what
the deploy would run — summed CPU, memory and storage, workloads, databases,
builds, static sites — BEFORE anything is built, pushed or recorded. The server
decides the same thing again at record/promote; the pre-flight only makes the
answer arrive in seconds instead of after a build.

## Queued, not refused: a deploy that needs billing

A deploy that only needs **billing the org has not set up** is ACCEPTED and
QUEUED, not refused. Anything hosted — a workload, a managed database or a
static site — needs a Reliant Compute plan. When the org has none:

- the pre-flight prints `Capacity: this deploy will be QUEUED on billing — …`
  and the deploy continues: built, pushed, release cut, bundle recorded,
  promotion written;
- the control plane holds the promotion (rollout phase `HELD`), and the env
  shows "waiting on billing";
- `forge env deploy` prints ONE block — what it waits on, why (the real
  quantity), what to do, and the action URL — and **exits 7**:

```
Error: deploy to prod is QUEUED, not failed: release 20261006.101500-abc is recorded and waits on billing.
  why   this runs compute (1 workload, 1 database) and the organization has no active compute plan
  do    Subscribe to a Reliant Compute plan in Reliant → Settings → Billing (an org admin can). …
  open  https://app.reliantlabs.io/forge/env/prod?forgeProject=acme
  then  nothing to re-run: it deploys on its own once that is done. To block until it is live: forge env status prod --wait
  (exit 7: queued on a person, not a failure)
```

- it goes live **by itself** once billing is set up — the billing webhook wakes
  the control plane; nothing is re-run. A newer deploy to the env replaces a
  queued one.

**Agents:** on exit 7, hand the `open` URL to the user and stop; do not retry,
do not poll in a loop. `--json` carries it structured (`.queued.holds[].action_url`,
`.queued.waiting_on`). `forge env deploy … --wait` (or
`forge env status <env> --wait`) blocks until it is live or `--timeout`; still
queued at the deadline is 7 again. `forge env status <env>` on a queued env
exits 7 with the same block rather than reporting drift.

## Refusals (nothing is built or recorded)

| Code | Meaning | Fix |
|---|---|---|
| `NO_COMPUTE_PLAN` | Anything hosted (static sites included) with no active compute plan — **queued** (above) on a current control plane; refused by one that predates queueing | Subscribe to a compute plan in billing settings |
| `EXCEEDS_CEILING` | The plan's CPU/memory/storage ceiling is below the demand | Reduce resources or upgrade |
| `SPEND_CAP_REACHED` | The org's spend cap is reached | Raise the cap |
| `PLAN_UNKNOWN` | The plan could not be determined | Retry; it is never read as "no plan" |

A plan too small, a met spend limit and an unknown plan are still REFUSED: a
ceiling breach is usually a config fix, a spend limit also clears unattended at
the next billing period, and "could not look" is never a reason to queue.

A control plane that predates the check answers Unimplemented: forge prints a
one-line WARNING ("capacity could not be pre-checked") and continues. A
network or auth failure fails the deploy.
