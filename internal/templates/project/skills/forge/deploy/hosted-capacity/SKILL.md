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
QUEUED, not refused. Anything hosted that runs compute — a workload, a job, a
managed database or an image build — needs the org's **hosting billing**:
pay-as-you-go hosting, enabled in Reliant → Settings → Billing by accepting its
rates and a monthly budget. Hosting billing is not a machine plan. The plans
that pay for Reliant cloud machines (where agents run) include no hosting, and
hosting needs no machine plan. Static sites run no compute: free static hosting
covers one organization per person (one they founded), with no limit on its
sites (each env's site is one), up to 1 GiB stored per site, 10 GiB stored in
total and 10 GiB of transfer a month. A static site in any of their other
organizations needs hosting billing too. When the org has no hosting billing
and the deploy needs it:

- the pre-flight prints `Capacity: this deploy will be QUEUED on billing — …`
  and the deploy continues: built, pushed, release cut, bundle recorded,
  promotion written;
- the control plane holds the promotion (rollout phase `HELD`), and the env
  shows "waiting on billing";
- `forge env deploy` prints ONE block — what it waits on, why (the real
  quantity), what to do, and the action URL — and **exits 7**:

```
Error: deploy to prod is QUEUED, not failed: release 20261006.101500-abc is recorded and waits on billing.
  why   this runs hosted compute (1 workload, 1 database) and the organization has no active hosting billing agreement
  do    … in Reliant → Settings → Billing (an org admin can). …
  open  https://forge.reliantlabs.io/forge/env/prod?forgeProject=acme
  then  nothing to re-run: it deploys on its own once that is done. To block until it is live: forge env status prod --wait
  (exit 7: queued on a person, not a failure)
```

The `why` and `do` lines are the control plane's own words, printed as sent;
the wire code is still `NO_COMPUTE_PLAN`, and some control planes still word the
fix as "a Reliant Compute plan". Whatever the wording, it means hosting
billing, not a machine plan.

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
| `NO_COMPUTE_PLAN` | The deploy runs compute — a workload, job, managed database or image build — and the org has no hosting billing (the code's name predates pay-as-you-go hosting; a machine plan does not clear it). A deploy of static sites alone does not get it, unless the control plane cannot inspect the bundle (then it assumes one workload). **Queued** (above) on a current control plane; refused by one that predates queueing | Enable pay-as-you-go hosting in billing settings |
| `STATIC_FREE_TIER_EXCEEDED` | No hosting billing, static sites only, and the org is not its founder's free hosting organization: free static hosting covers one organization per person, with no limit on the sites in it. **Queued** like `NO_COMPUTE_PLAN` | Move the free hosting organization to this one in organization settings, or enable pay-as-you-go hosting |
| `EXCEEDS_CEILING` | The demand is above the org's hosting ceiling (CPU/memory/storage), or its monthly hosting budget is reached | Reduce resources, or raise the budget or ceiling |
| `STATIC_SITE_LIMIT_REACHED` | The org has billing and the deploy would take it past its live static-site limit: 100 by default, every environment's site counting as one. A soft cap. Refused, never queued | Delete a site or environment you no longer need, or ask support for a higher limit (the fix line says how) |
| `SPEND_CAP_REACHED` | The org's spend cap is reached | Raise the cap |
| `PLAN_UNKNOWN` | The org's hosting billing could not be determined | Retry; it is never read as "no billing" |

A static release has a size cap too, checked when the bundle is recorded or
promoted (the pre-flight cannot see sizes): 1 GiB stored per free site
(three copies of a release), 10 GiB stored across the free hosting
organization's sites, and 5 GiB per release, as pushed, with billing.
Over it, `RecordBundle` / `Promote` fail with ResourceExhausted, reason
`static_release_too_large` on a billed org; the message names the size, the
cap and how to ask for more.

A plan too small, a met spend limit and an unknown plan are still REFUSED: a
ceiling breach is usually a config fix, a spend limit also clears unattended at
the next billing period, and "could not look" is never a reason to queue.

A control plane that predates the check answers Unimplemented: forge prints a
one-line WARNING ("capacity could not be pre-checked") and continues. A
network or auth failure fails the deploy.
