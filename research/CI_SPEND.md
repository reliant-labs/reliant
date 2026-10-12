# GitHub Actions spend — reliant-labs (measured 2026-10-12)

Research only. No workflow was edited, triggered, cancelled or rerun.

## TL;DR

1. **Only `control-plane` costs money.** `reliant` and `forge` are **public** repos, so their
   standard-runner minutes (Linux *and* Windows) are free; billing shows `netAmount: 0` for them.
   `control-plane` is **private** on the **free org plan** (2,000 included min/month), so every
   Linux minute past that bills at $0.006.
2. control-plane Linux spend is growing fast: Aug **$57** → Sep **$199** → Oct 1–11 **$300**
   (52,023 min in 11 days, ~4.7k min/day, Oct 11 alone 5,345 min = **$32/day**). Oct is on pace
   for ~$850 if uncapped.
3. **An Actions budget is already being hit.** Recent failed main runs of control-plane `CI`
   have every job annotated *"The job was not started because an Actions budget is preventing
   further use."* (e.g. run 38186940889). Of the last 100 `CI` push runs: 65 cancelled, 33
   failed, **2 succeeded**. So the spend is now also blocking CI verdicts.
4. Public repos are free in dollars but **not free in capacity**: the free plan caps the org at
   20 concurrent standard jobs (5 macOS). reliant (15.4k job-min / 7.8h sampled, incl. queued-
   then-cancelled) and forge (4.4k) share that pool with control-plane; ci.yml's own comment notes
   runner queueing "upwards of 35 minutes". Trimming them is a latency fix, not a dollar fix.

## Billing data (`gh api /organizations/reliant-labs/settings/billing/usage`)

Legacy `/orgs/.../settings/billing/actions` → 410 (moved). New endpoint works.

| Month | Repo | SKU | Minutes | Gross | **Net** |
|---|---|---|---|---|---|
| 2026-08 | control-plane | Linux | 11,617 | $69.62 | **$57.32** |
| 2026-08 | forge | Linux | 12,726 | $76.29 | $0 (public) |
| 2026-08 | reliant | Linux / macOS / Win | 5,706 / 212 / 121 | $48.52 | $0 (public) |
| 2026-09 | control-plane | Linux | 35,351 | $211.97 | **$199.49** |
| 2026-09 | forge | Linux | 55,598 | $333.48 | $0 |
| 2026-09 | reliant | Linux | 7,342 | $43.97 | $0 |
| 2026-10 (1–11) | control-plane | Linux | 52,023 | $312.09 | **$299.84** |
| 2026-10 (1–11) | forge | Linux / Windows | 70,760 / 8,392 | $508.41 | $0 |
| 2026-10 (1–11) | reliant | Linux | 27,752 | $166.45 | $0 |

Storage (artifacts/caches) is cents. macOS is negligible (homebrew-reliant / reliant, public).
Gross figures matter only if forge/reliant ever go private: then forge alone is ~$1.2k/month.

## Per-workflow cost from run history

Method: `gh run list --limit 200` per repo, then `/actions/runs/<id>/jobs` for every run
(1,960 jobs), summing per-job wall time rounded up to the minute (GitHub's billing rule). The
`/timing` endpoint returns `total_ms: 0` for everything in this org (billable is 0 for public
repos, and it is not populated for control-plane either), so jobs API is the source.
All runners are `ubuntu-latest` except forge `CI :: Windows` (`windows-latest`, 2x). No macOS, no
larger runners, no self-hosted labels anywhere.

Sample windows: control-plane 21:51→01:53 UTC (4h), forge 19:08→02:02 (7h), reliant 18:17→02:03.

### control-plane (the one that bills) — 3,624 job-min in 4h of sampled runs

| Workflow / event | Job-min | Share |
|---|---|---|
| CI / pull_request | 1,802 | 50% |
| CI / push (main) | 641 | 18% |
| Pin Drift / PR+push | 319 | 9% |
| CI Extras / PR+push | 314 | 9% |
| Dev Deploy (k3d smoke) / PR+push | 216 | 6% |
| pre-commit / PR+push | 155 | 4% |
| E2E Tests / push | 91 | 3% |
| license-gate / PR+push | 86 | 2% |

Top jobs (avg per run, success/failure only): CI::Integration Tests 25.2 min, CI::Test 12.7,
Validate KCL 9.8, Verify Generated Code 9.8, License Compliance 8.9, Feature Gate Inertness 9.0,
Vulnerability Scan 8.2, Build 8.4, Lint 8.4, Dev Deploy k3d 8.5, CI Extras Docker Build 7.9,
pre-commit 6.0, Pin Drift (3 jobs) ~4 each.

**Key observation:** `CI` fans out into **10 independent jobs** each paying the same 2–4 min
fixed setup (checkout, "Free runner disk space" 133 s, setup-go 23–32 s, restore ~1.5 GB build
cache 23–29 s, install forge/tools 45 s). E.g. run 38185158974: License Compliance spends 77 s
installing+setting up to do 34 s of work; Lint spends 133 s freeing disk. With every job billed
to the minute and rounded up, ~40–50% of `CI` minutes is setup, not work. One CI run ≈ 90–110
billable min; ~24 CI runs/4h ⇒ the bulk of the $32/day.

Also: license compliance runs **twice** per change (`ci.yml` License Compliance *and*
`license-gate.yml`); Docker image builds happen on every PR (`ci-extras.yml`) and again on main
(`build-images.yml`); Pin Drift runs 3 jobs on every PR/push plus two crons.

### forge (public, free $; ~4.4k job-min / 7h — capacity cost)

E2E Suite 4 shards ~20 min each + compile (≈85 min/run, PR and push), CI::Windows 24.5 min
(windows-latest), Fixture Corpus 15 min, CI::Test 16 + Test(guards) 9, E2E Scaffold 6.
All of E2E Suite / Fixture Corpus / E2E Scaffold run on **every PR and every push to main**.

### reliant (public, free $; 15.4k job-min incl. cancelled)

PR CI ≈ 13.3k. 285 of 862 jobs **cancelled**, some after ~2.7 h (9,750 s) sitting on a runner
— these are push-to-main runs: `pr-ci.yml` gives each main SHA its own concurrency group
(`group: ...github.sha`) so main runs never cancel, pile up, and saturate the org's 20-slot pool.
Successful PR CI is cheap (~25 min/run) thanks to its "Detect what changed" job.

## Static analysis highlights

| Finding | Where |
|---|---|
| Every heavy workflow triggers on both `pull_request` and `push: main` → each merged change pays the full suite twice | cp `ci.yml`, `ci-extras.yml`, `dev.yml`, `e2e.yml`, `pre-commit.yml`, `license-gate.yml`, `pin-drift.yml`; forge `ci.yml`, `e2e-suite.yml`, `fixture-corpus.yml`, `e2e-scaffold.yml` |
| No path filtering / change detection in control-plane (reliant has a `changes` job; cp has none) — docs/KCL-only PRs run the Go test + integration + docker + k3d | cp `ci.yml`, `ci-extras.yml`, `dev.yml` |
| Main runs are never cancelled (deliberate, for verdicts) | cp `ci.yml` L36, forge `ci.yml` L22, `e2e-suite.yml` L75, reliant `pr-ci.yml` L31 (per-SHA) |
| Duplicate license scan | cp `ci.yml` `license-check` + `license-gate.yml` |
| Docker build on every PR, again on main | cp `ci-extras.yml` + `build-images.yml` |
| Missing `timeout-minutes` on most CI jobs (default 360 min) | cp `ci.yml` lint/test/build/verify/validate-kcl/vuln/license; reliant `pr-ci.yml`; forge `ci.yml` |
| Caching is good: setup-go module cache + explicit `~/.cache/go-build`, npm cache, docker `type=gha` | — not a lever |
| Crons: only cp `pin-drift.yml` (daily + weekly, ~10 min/day). db-snapshot cron already removed | negligible |
| Self-hosted runners: none in use (org runners API 403 for this token; no `self-hosted` labels in any workflow) | — |

## Ranked recommendations

Savings are for **control-plane $** (the only billed repo) unless stated. Baseline ≈ 4.7k min/day ≈ $850/mo at current pace.

### Quick wins

1. **Consolidate control-plane `ci.yml` from 10 jobs to ~3** (`checks`: lint+verify-generated+
   validate-kcl+license+vuln+feature-gate+migration-chain+build sequentially in one job sharing
   one checkout/cache/forge install; `test`; `integration-test`). Removes ~7× (disk-free + setup-go
   + cache restore + forge install) and 7× per-minute round-up. **Est. −35–45% of CI minutes ≈
   −25–30% of total ≈ $200–250/mo.** Trade-off: one serial job is slower wall-clock (~15 min) —
   acceptable given 35-min queue waits today.
2. **Add change detection to control-plane `ci.yml`, `ci-extras.yml`, `dev.yml`, `e2e.yml`**
   (copy reliant `pr-ci.yml`'s `changes` job — job-level `if:` keeps required checks reporting,
   unlike `paths-ignore`). Skip Go tests/integration/docker/k3d for docs/**, *.md, `deploy/kcl/**`-
   only (run Validate KCL only), `frontends/**`-only. **Est. −10–20% ≈ $80–150/mo** (depends on PR mix).
3. **Stop re-running the full PR suite on push to main** for cp `ci-extras.yml` (Docker Build PR
   check — `build-images.yml` already builds on main), `pre-commit.yml`, `license-gate.yml`,
   `pin-drift.yml` (keep the crons), `dev.yml`: change `on.push` off for these. Keep `ci.yml` on
   push (verdict on main is wanted) — or better, enable a **merge queue** (`merge_group:`) and
   drop `push` entirely so each change is tested exactly once pre-merge. **Est. −15–20% ≈
   $120–170/mo.**
4. **Delete the duplicate license scan**: drop `license-check` job from cp `ci.yml` (keep
   `license-gate.yml`, or vice versa). 9 min/run. **Est. −5% ≈ $40/mo.**
5. **Fold the three Pin Drift jobs into one and run on PR only when `go.mod`/`forge.yaml`/
   `package.json` change** (`pin-drift.yml` `pull_request.paths:`; it is not a required check
   per its own comment on warnings). 319 min/4h today. **Est. −8% ≈ $60/mo.**
6. **Add `timeout-minutes`** (e.g. 20 for lint/build/checks, 30 for test) to every job in cp
   `ci.yml`, reliant `pr-ci.yml`, forge `ci.yml`. Caps runaway cost (reliant shows 2.7 h jobs).
   Small $, insurance.
7. **Raise/confirm the Actions budget deliberately** (Org → Billing → Budgets). The current cap is
   silently failing main CI (run 38186940889, all 8 jobs "not started because an Actions budget is
   preventing further use"). Either raise it to the true need after 1–5, or accept failures knowingly.

### Capacity (public repos — $0, but they starve control-plane of the 20 shared slots)

8. **forge: run `e2e-suite.yml` (4×20 min shards), `fixture-corpus.yml` and `CI::Windows` only
   on main / label / nightly, not every PR** — or path-gate them to `internal/templates/**`,
   `internal/cli/**`, codegen dirs. Frees ~100 job-min per forge PR of shared concurrency.
9. **reliant `pr-ci.yml`: use `group: pr-ci-push-main` with cancel-in-progress false** (bounded
   queue, as cp ci.yml does) instead of per-SHA groups, so bursts of merges don't park dozens of
   runs on runners for hours (285 cancelled jobs, some 2.7 h).

### Structural

10. **Self-hosted runners via actions-runner-controller (ARC) on the GKE nonprod cluster**
    (`gke_reliant-nonprod-490701_us-central1_nonprod`) for control-plane (and optionally forge/
    reliant to escape the 20-slot cap). Use a spot/preemptible node pool with autoscale-to-zero
    (e.g. n2d-standard-8 spot ≈ $0.07/h ≈ $0.001/min vs $0.006/min hosted → **~80–90% cheaper per
    minute**, plus persistent Go build cache on a PVC removes most of the setup time measured above).
    Caveats: k3d smoke test (`dev.yml`) and docker builds need Docker-in-Docker / privileged pods or
    a dind sidecar; never run untrusted fork PRs on them (forge/reliant are public → restrict ARC
    runners to private control-plane or to non-fork events). Ops cost: one Helm release + node pool.
    **Est. control-plane Actions $ → ~$50–100/mo of GCP compute** after quick wins.
11. **Alternative: GitHub Team plan** ($4/user/mo × 4 = $16/mo) raises included minutes 2,000 →
    3,000 and concurrency 20 → 40. Small $ saving, meaningful queue relief; cheap to combine with 1–5.
12. **Merge queue across all three repos** (`merge_group` trigger) — the durable fix for
    "PR + push = double run" while keeping a verdict for every main SHA.

## Expected outcome

Quick wins 1–5 together: roughly **−55–70% of control-plane minutes** (~$450–600/mo at the
October pace), which also brings it back under the budget so main CI produces verdicts again.
ARC (10) drives the remainder toward GCP compute cost.

## Evidence reproduction

```bash
gh api "/organizations/reliant-labs/settings/billing/usage?year=2026&month=10"
gh run list -R reliant-labs/control-plane --limit 200 --json databaseId,workflowName,event,conclusion,createdAt
gh api repos/reliant-labs/control-plane/actions/runs/<id>/jobs   # per-job durations/labels
gh api repos/reliant-labs/control-plane/check-runs/<job_id>/annotations  # budget-block message
```
