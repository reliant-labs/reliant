---
name: observability/query
description: The feedback loop from the CLI — forge obs status for what is wrong now, forge obs check after a deploy, then issues, traces, logs, endpoints and metrics; forge env releases for what was live when; forge db query for the rows. Backends (local ClickStack, the control plane), the JSON contract, suggested next commands, and the exit codes a CI gate branches on.
---

# Querying the running system: `forge obs`

After a change, four questions decide what to do next:

- Did it work?
- What broke?
- What is slow?
- What is in the database?

`forge obs` answers the first three against an env's telemetry. `forge env releases` says which release was live when. `forge db query` answers the fourth (`db/query`).

## The loop

```bash
forge env deploy prod
forge obs check prod --wait --json          # 0 PASS · 1 FAIL · 2 cannot tell · 8 wait timed out
# on FAIL, the document's findings[] and suggested[] name the next commands:
forge obs issue ISS-7f3a prod --json        # culprit, latest event, trace id, suspect commits, the release live
forge obs trace 0af7651916cd43dd8448eb211c80319c prod
forge obs logs prod --trace 0af7651916cd43dd8448eb211c80319c
forge db query prod "select count(*) from jobs where crew_id is null" --confirm-env prod --reason ISS-7f3a
# fix, deploy, check again; then
forge obs issue resolve ISS-7f3a prod --in next
```

No deploy to judge, just "something is wrong"? Start with `forge obs status prod`: the services, the new and regressed issues, the alerts, whether telemetry is arriving and the release in force, in one document.

## Backends

`[env]` defaults to `dev`. The env's KCL decides where telemetry lives:

| Env declares | Backend |
|---|---|
| no destination (host processes, the local ClickStack) | **local**: queries the ClickStack `forge env up` started. Down: exit 2 with `forge env up <env>` |
| hosted workloads, or `destination.token_from_control_plane` | **cloud**: the control plane's ObservabilityService / IssuesService, with your credential (under Reliant, the helper; never `forge login`) |
| a third-party destination | exit 2: forge queries only the local ClickStack or the control plane |
| observability off | exit 2 |

`--backend local|cloud` overrides the choice. Every document names `scope.env`, `scope.backend` and, for cloud, `scope.env_id`.

Local and cloud compile through ONE query compiler (`forge/pkg/obsquery`), so a query that works locally means the same against prod. Local issues are grouped at query time with the same fingerprint (sfp1) the control plane computes at ingest. They carry no state: resolve and ignore exist only on cloud, and `--since-release` / `--since-deploy` need a hosted env.

## Verbs

| Verb | Answers |
|---|---|
| `forge obs status [env]` | What is wrong right now: per-service rate, errors and p95; new and regressed issues; alerts firing or pending (cloud); when the newest log and span arrived, and whether the telemetry token reached the cluster (cloud); the release in force and the last 5 changes |
| `forge obs check [env]` | Did the deploy work? Before/after the promotion: error rate, p95, new and regressed issues, alerts, restarts |
| `forge obs changes [env]` | What changed: promotions, rollbacks, failed deploys and activity, newest first (locally: this machine's ledger and `forge env up` starts) |
| `forge obs issues [env]` | Grouped errors (`--status`, `--since-release`, `--since-deploy`, `--platform server\|browser`, `--sort last\|first\|events`, `--page-token`) |
| `forge obs issue <id> [env]` | Everything needed to fix one issue: culprit, frames, latest event and its trace, events per version, co-occurring issues, the release live when it was last seen, suspect commits from local git (`--events N`) |
| `forge obs issue resolve\|ignore\|unresolve <id> [env]` | Triage (cloud; needs `obs:triage`): `resolve --in next\|<version>`, `ignore --until 72h` |
| `forge obs logs [env] [query]` | Search and tail (`--trace`, `--severity`, `--oldest`, `--page-token`; `--follow` polls, every `--interval`, default 2s) |
| `forge obs traces [env]` | Find traces (`--slow`, `--min-duration`, `--errors`, `--route`, `--page-token`) |
| `forge obs trace <trace-id> [env]` | One trace as a capped tree; error spans and the critical path are always expanded (`--full` lifts the caps; `--logs N` linked log lines); carries the release live when it started |
| `forge obs endpoints [env]` | Busiest, slowest or most failing endpoints (`--sort requests\|p95\|errors\|rate`, `--top`, default 10) |
| `forge obs metrics list [env]` | Metric names (`--contains`) |
| `forge obs metrics query [env] <metric>` | One metric over time (`--agg avg\|sum\|min\|max\|count\|increase\|rate\|p50\|p95\|p99`, `--group-by` repeatable, `--step`) |
| `forge obs metrics declared` | The metrics the project's code declares |
| `forge obs query [env] --sql "…"` | Anything else: raw SQL behind the read-only guard (`--sql-file`, `--panel FILE`, `--param name=value`, `--step`) |
| `forge obs profile`, `forge obs alerts check`, `forge obs dashboards check` | Flamegraphs, alerts-as-code and dashboards (`observability/profiles`, `observability/alerts`, `observability/dashboards`) |

Common flags of the telemetry verbs: `--since 2h` (or `--from/--to`), `--service` (repeatable), `--filter k=v` / `k!=v` (repeatable), `--backend local|cloud`, `--limit` (list verbs), `--json`, `--fields a,b.c`, and `--explain` (alias `--print-sql`), which adds the compiled `statements[]{purpose,sql,params}` to the document instead of running them. Copy a statement into `forge obs query --sql` when the structured verbs can't express what you need. `FORGE_OBS_DEBUG=1` logs each raw query's literal-free shape to stderr. `status` and `changes` take the time, backend and output flags but not `--filter` or `--explain`; `check` has its own (below).

## What is wrong now: `forge obs status`

Each section is read on its own and concurrently. A section the backend cannot answer is **absent**, named in `unread`, with the reason in `warnings`; a section the backend does not have (alerts, locally) is named in `not_evaluated`. A section that was read and is empty is `[]`: "none", not "unknown". The exit status is **0** whenever anything was read: status reports, it does not judge. It is **2** only when nothing could be read. Gate a release with `check`, not `status`.

## Release health: `forge obs check`

The subject is a promotion: the env's current one, or `--release v` / `--promotion id`. Two equal windows are compared, before and after it. Each window stops at the neighbouring promotion and at now. Error rate is weighted for tail sampling, so it is not inflated by dropped OK spans. A local env has no promotions: `--since 20m` (or the start of the last `forge env up`) names the instant to judge from, and regressed issues, alerts and restarts are listed under `not_evaluated`.

The env's KCL declares the thresholds and which findings fail (`forge.ReleaseHealth` on the Bundle; the defaults are below). They are recorded in the release shape, so the console, the CLI and alerting judge alike. `--threshold k=v` overrides one for one call, `--fail-on` the failing set, `--window` the width, and the document says where each threshold came from.

```
release_health = forge.ReleaseHealth {
    window = "15m"                      # 1m to 6h
    min_requests = 50                   # below this a service is not judged
    max_error_rate_increase = 0.01      # a fraction: one percentage point
    max_p95_ratio = 1.5
    new_issue_min_events = 3
    fail_on = ["new_issue", "regressed_issue", "error_rate", "alert_firing"]   # latency, workload_restarts warn
}
```

| Exit | Verdict |
|---|---|
| 0 | PASS (also INCONCLUSIVE with `--allow-inconclusive`) |
| 1 | FAIL: a `fail_on` finding fired |
| 2 | INCONCLUSIVE (the after window has not finished, or no service reached `min_requests`), or forge could not look |
| 8 | `--wait` timed out (`--timeout`, default 30m) before the after window finished |

`--gate-json FILE` writes the evidence `forge gate record --from` attaches to the promotion.

## What was live when: `forge env releases`

```bash
forge env releases list prod --since 7d      # promotions, rollbacks, failed deploys: version, when, who, commits, gates, health
forge env releases show prod v1.4.2          # commits and PRs, the shape diff vs the previous release, migrations, gates, health
forge env releases show prod prom-7f3a     # the same, by promotion id
forge env releases at prod 2026-10-21T14:05:00Z   # the release live then, and the ones bracketing it
forge env releases at prod 0af7651916cd43dd8448eb211c80319c   # …at that trace's start (control-plane envs)
forge env releases at prod 2h                # …two hours ago (or `now`)
```

`list` and `show` take `--json`, `--fields` and `--limit` like the `forge obs` verbs. The health verdict needs `obs:read`; without it the release is listed with no verdict and a warning, never refused.

`forge obs issue`, `forge obs trace` and `forge db query` documents carry the live `release` (`version`, `promotion_id`, `promoted_at`, `unsettled`), so an agent can go from an error to the change that shipped it. An unreadable ledger leaves `release` out with a warning on stderr; it never fails the verb.

## The JSON contract

- **One document on stdout.** It has `ok`, `exit_code` and `error`, computed from the same error the process exits with, plus `schema`: `forge.obs.<verb>/v1` (`status`, `check`, `changes`, `issues`, `issue`, `logs`, `traces`, `trace`, `endpoints`, `metrics_list`, `metrics_query`, `query`). A failure is `{schema, ok: false, exit_code, error, suggested[]}`. Progress and warnings go to stderr. `logs --follow --json` is NDJSON and ends with an envelope line.
- **`query_mode`** is `structured` or `raw` on every document: which path answered.
- **`scope`** echoes env, backend, env id, time range and services.
- **Bounded by default.** Lists carry `truncated`, `next_page_token`, and `omitted` where a cap cut items.
- **`suggested`:** up to 3 complete, runnable next commands. Run them; don't paraphrase them.
- **`--fields`** projects the document (dotted paths) to save tokens. Each verb's `--help` lists its schema's fields; a misspelled one is refused with the real list.
- **No placeholders.** A missing key means unknown.

Exit codes are shared by every verb: **1** = you asked for something wrong (bad query, guard refused, policy refused); **2** = forge could not look (unreachable, auth, quota, telemetry off, the control plane does not serve the call yet). A gate must treat 2 as "not proven good".

## Machine credentials

CI tokens read telemetry with `obs:read` (`forge cloud token create --env prod --scopes obs:read`). Triage needs `obs:triage`, and `forge env releases` against a cloud env also needs `deploy:read`. Under Reliant, the credential helper grants these from the person's own authority.
