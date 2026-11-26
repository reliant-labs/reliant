---
name: observability/query
description: The feedback loop from the CLI — forge obs status for what is wrong now, forge obs check after a deploy, then issues, traces, logs, endpoints, ops, blast radius and metrics; forge env releases for what was live when; forge db query for the rows. Backends (local ClickStack, the control plane), the JSON contract, suggested next commands, and the exit codes a CI gate branches on.
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
forge obs check prod --wait --json          # 0 PASS · 1 FAIL · 2 cannot tell (incl. low traffic) · 8 check again later (wait timed out, quota spent)
# on FAIL, the document's findings[] and suggested[] name the next commands:
forge obs issue ISS-7f3a prod --json        # culprit, latest event, trace id, suspect commits, the release live
forge obs trace 0af7651916cd43dd8448eb211c80319c prod
forge obs logs prod --trace 0af7651916cd43dd8448eb211c80319c
forge db query prod "select count(*) from jobs where crew_id is null" --confirm-env prod --reason ISS-7f3a
# fix, deploy, check again; then
forge obs issue resolve ISS-7f3a prod --in next
```

No deploy to judge, just "something is wrong"? Start with `forge obs status prod`: the services, the new and regressed issues, the alerts, the suspended or unhealthy workloads, whether telemetry is arriving and the release in force, in one document.

## Backends

`[env]` defaults to `dev`. The env's KCL decides where telemetry lives:

| Env declares | Backend |
|---|---|
| no destination (host processes, the local ClickStack) | **local**: queries the ClickStack `forge env up` started. Down: exit 2 with `forge env up <env>` |
| hosted workloads, `destination.token_from_control_plane`, or cluster-only workloads with a declared `control_plane` | **cloud**: the env's control plane's ObservabilityService / IssuesService, with your credential (under Reliant, the helper; never `forge login`). A cluster runtime remains self-managed, not hosted |
| a third-party destination | exit 2: forge queries only the local ClickStack or the control plane |
| a local env whose `forge env up` sent telemetry to the hosted store (`dev_telemetry`, recorded in `.forge/obs/backend-<env>.json`) | **cloud**, bound to the record's org, env and instance |
| observability off | exit 2, naming the KCL to add |

Choose the env, not a backend: env names are arbitrary (`prod` does not imply hosting). A missing runtime/remote endpoint, or mixed local and cluster workloads without a destination or explicit local/ephemeral lifecycle, exits 2 rather than guessing local. A local lifecycle with host processes and local k3d workloads keeps using the local ClickStack. `--backend local|cloud` remains a diagnostic override. Every document names `scope.env`, `scope.backend` and, for cloud, `scope.env_id`.

A hosted-dev env is shared by every `forge env up` stack of it (your worktrees, your teammates'), so its reads default to THIS checkout's instance: `scope.instance {id,label,user}`. `--instance <id|label>` reads another (an id is the control plane's `scope.instance.id`; a label resolves only through the records `forge env up` wrote on this machine), `--all-instances` the whole env, `--user <user>` every instance of one user (`--instance-user` on `sessions`). `suggested[]` repeats a non-default choice. A control plane that does not acknowledge the narrowing is exit 2, never the whole env shown as the instance. Raw SQL (`query --sql`, sql panels) cannot be narrowed: it is refused until you pass `--all-instances`, and `status` lists the env-wide alerts and deliveries under `not_evaluated`. `obs changes dev` still reads the local ledger.

Local and cloud compile through ONE query compiler (`forge/pkg/obsquery`), so a query that works locally means the same against prod. Local issues are grouped at query time with the same fingerprint (sfp2) the control plane computes at ingest. They carry no state: resolve and ignore exist only on cloud, and `--since-release` / `--since-deploy` need a hosted env.

## Verbs

| Verb | Answers |
|---|---|
| `forge obs status [env]` | What is wrong right now: per-service rate, errors and p95; new and regressed issues; alerts firing or pending (cloud); the workloads suspended or needing a look, and how many there are (cloud: a suspended env serves no traffic and emits no telemetry, so this is what tells it from a quiet one; suggests `forge env start`); when the newest log and span arrived, and whether the telemetry token reached the cluster (cloud); the release in force and the last 5 changes |
| `forge obs workloads [env]` | Restarts, crash loops, last termination reason (OOMKilled, Error, ...), ready/desired replicas and the latest Kubernetes warning per workload, plus failed Job runs — the cluster state a hosted user has no kubeconfig for (cloud; `--all`, `--jobs failed\|all\|none`, default 24h, max 7d). Suggests the `obs logs` command narrowed to the worst workload |
| `forge obs check [env]` | Did the deploy work? Before/after the promotion: error rate, p95 (server spans, else platform ingress), browser errors and page loads, new and regressed issues, alerts, restarts |
| `forge obs changes [env]` | What changed: promotions, rollbacks, failed deploys and activity, newest first (locally: this machine's ledger and `forge env up` starts) |
| `forge obs issues [env]` | Grouped server failures: ERROR records and WARN ones with `error_class=server` (not `deadline=caller`) (`--status`, `--since-release`, `--since-deploy`, `--platform server\|browser`, `--assignee <user-id\|me\|none>`, `--sort last\|first\|events`, `--page-token`). Issues that share a cause and first fired together are one INCIDENT and collapse under the first (`+N related`); `--expand-incidents` lists them all, `--incident-window` sets how close "together" is (default 10m). Nothing is merged | Cloud rows carry `assignee`, `incident_key`, `related_count`.
| `forge obs issue <id> [env]` | Everything needed to fix one issue: culprit, frames, latest event and its trace, events per version, co-occurring issues, the other issues of its incident, the release live when it was last seen, suspect commits from local git (`--events N`); on a cloud env also its assignee and the newest `--timeline-limit N` (default 10) timeline entries, oldest first: status changes, comments, assignment, linked chats |
| `forge obs issue resolve\|ignore\|unresolve <id> [env]` | Triage (cloud; needs `obs:triage`): `resolve --in next\|<version>`, `ignore --until 72h` |
| `forge obs issue comment <id> <text> [env]` | Add a comment (cloud; needs `obs:triage`) |
| `forge obs issue edit-comment <id> <comment-id> <text> [env]` | Replace one of your comments (cloud; needs `obs:triage`) |
| `forge obs issue delete-comment <id> <comment-id> [env]` | Remove a comment: its author, or an org owner or admin (cloud; needs `obs:triage`) |
| `forge obs issue assign <id> <user-id\|me\|none> [env]` | Set or clear the assignee (cloud; needs `obs:triage`); sends the version it read, exits 1 `changed since you read it` if the issue moved, never retries |
| `forge obs sessions [env]` | Real-user browser sessions that started in the range, newest first (`--has-error`, `--has-replay`, `--user`, `--path`, `--since` up to 30d, `--limit`, `--page-token`). Hosted envs only |
| `forge obs users [env]` | Users over the range (`--since`, default 7d, up to 90d) and the one before: active, new, returning, per env, with `--series N` daily counts and `--seen-total`. Hosted envs only |
| `forge obs session <id> [env]` | One session as a text timeline: pages, clicks on masked selectors, failed requests, errors with their issue, custom events, trace ids (`--limit`, `--page-token`). No replay bytes. Exit 1 not found, 2 could not look |
| `forge obs logs [env] [query]` | Search and tail (`--trace`, over the trace's own time range; `--severity`, `--oldest`, `--page-token`; `--follow` polls, every `--interval`, default 2s) |
| `forge obs traces [env]` | Find traces (`--slow`, `--min-duration`, `--errors`, `--route`, `--page-token`) |
| `forge obs trace <trace-id> [env]` | One trace as a capped tree; error spans and the critical path are always expanded (`--full` lifts the caps; `--logs N` linked log lines); carries the release live when it started. Each span line shows how it ended (`rpc=CANCELED`, `http=403`, `error.type`, a statement's summary, ClickHouse `read_rows` and `query_id`) and a failed span's exception; a log that warns, fails or carries an `error` is printed under its span with its error and cause; a span whose children cover little of it gets `⋯ uninstrumented 9.99s of 10.00s`: the time went to code that emitted no span, so look there. Past 10,000 spans the read stops and says so (`158,864 spans (10,000 read)`): time past it is `⋯ not read`, runs carry the whole trace's count, and its spans by name follow |
| `forge obs endpoints [env]` | Busiest, slowest or most failing endpoints (`--sort requests\|p95\|errors\|rate`, `--top`, default 10); CLIENT P95 (`client_p95_ms`, `client_calls`) is the browser's view of the rpc, from `forge.rpc.client` spans, `client_note` when unreadable |
| `forge obs ops [env]` | Component ops (`<pkg>.<Method>`) and RPCs by traffic, with each op's code location (`--sort calls\|p95\|errors\|rate`, `--top`, default 20, `--kind rpc,endpoint,component`, `--name <op>` for named ops only) |
| `forge obs blast-radius [env]` | The diff (`--base`, default the merge-base with origin/main; `--head`, default the working tree) mapped to the ops it changes, with their traffic (`--since`, default 7d); no env: the mapping alone. Which to run when: `observability/query/ops` |
| `forge obs metrics list [env]` | The metric catalog (`--contains`): kind, monotonic, temporality, services, last seen, series estimate; locally, merged with the declared schema (`declared`, `seen`) |
| `forge obs metrics describe [env] <metric>` | One metric: interval, attribute keys with value counts, suggested breakdowns, the declared spec |
| `forge obs metrics query [env] <metric>` | A metrics panel, as the console explorer runs it: `--agg`/`--quantile`, `--query NAME=metric[:agg[:q]]`, `--where NAME:key=value`, `--filter`, `--group-by` (repeatable), `--formula`, `--kind line\|number\|table\|bar`, `--top`, `--compare 24h\|prev-release`, `--step`, `--panel-out FILE` |
| `forge obs metrics exemplars [env] <metric>` | Sampled observations with their traces (`--at T`, `--from/--to`, `--min-value`, `--where`, `--group KEY=VALUE` for one series of a grouped panel); `trace_stored` says whether `forge obs trace` will find the trace |
| `forge obs metrics declared` | The metrics the project's code declares |
| `forge obs query [env] --sql "…"` | Anything else: raw SQL behind the read-only guard (`--sql-file`, `--panel FILE`, `--param name=value`, `--step`) |
| `forge obs profile`, `forge obs alerts check`, `forge obs dashboards check` | Flamegraphs, alerts-as-code and dashboards (`observability/profiles`, `observability/alerts`, `observability/dashboards`) |

Common flags of the telemetry verbs: `--since 2h` (or `--from/--to`), `--service` (repeatable), `--filter k=v` / `k!=v` (repeatable; `k` is an allowlisted key such as `http.route`, or any attribute named by its map: `span.attr.<k>` on traces, `log.attr.<k>` on logs, `resource.attr.<k>` anywhere, so `forge obs traces prod --filter span.attr.chat.id=<id>` finds every trace one of whose spans carries that chat), `--backend local|cloud`, `--limit` (list verbs), `--json`, `--fields a,b.c`, and `--explain` (alias `--print-sql`), which adds the compiled `statements[]{purpose,sql,params}` to the document instead of running them. Copy a statement into `forge obs query --sql` when the structured verbs can't express what you need. `FORGE_OBS_DEBUG=1` logs each raw query's literal-free shape to stderr. `status` and `changes` take the time, backend and output flags but not `--filter` or `--explain`; `check` has its own (below).

**Which ops, when** (`observability/query/ops`): before editing a component or handler, `forge obs ops <env>` or `forge obs blast-radius <env>` says how hot and fragile the code is; reviewing a PR, `forge obs blast-radius <env> --json`; after a deploy, `forge obs check` (its `changed_op_*` findings).

## Issue assignment filters

`forge obs issues <env> --assignee me|none|<user-id>` and `forge obs issue assign <id> me|none|<user-id> <env>`. `me` is the verified credential's person (an organization automation token is refused, exit 2); how each value reaches the API and the local behaviour: `observability/query/issues`.

## What is wrong now: `forge obs status`

Each section is read on its own and concurrently. A section the backend cannot answer is **absent**, named in `unread`, with the reason in `warnings`; a section the backend does not have (alerts and workloads, locally) is named in `not_evaluated`. A section that was read and is empty is `[]`: "none", not "unknown". The exit status is **2** (`ok: false`) when any evidence could not be read, including partial sections reported in `warnings`; readable sections and the release are preserved. A complete read exits **0**: status reports, it does not judge health. Gate a release with `check`, not `status`.

## Release health: `forge obs check`

`forge obs check <env> --wait --json` after every deploy proves the release good (exit 0), regressed (1), not proven (2) or not yet judgeable (8). Its flags, the `release_health` policy (`fail_on`, `warn_on`), the exit codes and the INCONCLUSIVE `reason_code`s are in **`observability/check`**; how each finding is judged is in `observability/release-health`.

## Investigate a user session

"A user hit a bug": `forge obs sessions <env> --has-error`, then `forge obs session <id> <env>` for what they did, in order. Reading the timeline and following its ids is `observability/sessions`.

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

- **One document on stdout.** It has `ok`, `exit_code` and `error`, computed from the same error the process exits with, plus `schema`: `forge.obs.<verb>/v1` (`status`, `check`, `changes`, `issues`, `issue`, `sessions`, `session`, `logs`, `traces`, `trace`, `endpoints`, `ops`, `blast_radius`, `metrics_list`, `metrics_query` (v2), `metric` (describe), `metric_exemplars`, `query`). A failure is `{schema, ok: false, exit_code, error, suggested[]}`. Progress and warnings go to stderr. `logs --follow --json` is NDJSON and ends with an envelope line.
- **`query_mode`** is `structured` or `raw` on every document: which path answered.
- **`scope`** echoes env, backend, env id, time range and services.
- **Bounded by default.** Lists carry `truncated`, `next_page_token`, and `omitted` where a cap cut items.
- **`suggested`:** up to 3 complete, runnable next commands. Run them; don't paraphrase them.
- **`--fields`** projects the document (dotted paths) to save tokens. Each verb's `--help` lists its schema's fields; a misspelled one is refused with the real list.
- **No placeholders.** A missing key means unknown.

Exit codes are shared by every verb: **1** = you asked for something wrong (bad query, guard refused, policy refused); **2** = forge could not look (unreachable, auth, quota, telemetry off, the control plane does not serve the call yet). A gate must treat 2 as "not proven good". Except in `obs check`, where a spent quota is **8** (above).

## Machine credentials

CI tokens read telemetry with `obs:read`; a person mints the CI token in the web app (Settings → API tokens, https://forge.reliantlabs.io/settings/tokens). Triage needs `obs:triage`, and `forge env releases` against a cloud env also needs `deploy:read`. Under Reliant, the credential helper grants these from the person's own authority.

## One cause, many issues

The fingerprint (sfp2) folds a normalized error class into records with no `exception.type`, and issues that share a cause and first fired together collapse into one incident without being merged: `observability/query/incidents`.
