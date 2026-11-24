# Agent analytics: Agents › Insights, per-chat traces, tenant export

Status: **draft for owner review.** The direction is settled (owner, 2026-11-22):
emit standard `gen_ai` OpenTelemetry, do not build a competing OTEL LLM backend,
and build the analytics only Reliant can compute. Agents › Insights and a
per-chat Trace/Cost tab are the approved placements; Usage & billing keeps the
money. Everything else here is a design for the owner to accept or change.
Sections 6 and 7 are **recommendations pending the owner's confirmation**
(asked 2026-11-22); nothing in them is decided, and the PR plan marks the phases
that wait on them.

Code references are `file:line` at **reliant `3b8d3b81f`** (paths without a repo
prefix are reliant's), **control-plane `cda41ff24`** (marked `cp:`) and the forge
release reliant pins, `v1.10.1-0.20261121231614-3b80ee2f5755` (marked `forge:`).
Where a claim could not be confirmed by reading code it says **unverified**.

## 1. Goals and non-goals

Reliant already runs the whole agent loop: the chat, the workflow run and its
steps, the human approvals and questions, the model calls, the tool calls, the
pull request the work produced and, through forge, the release that shipped it.
Every other product in this space reconstructs that picture from SDK spans, and
none of them can join it to a merged PR or a release. The goal is to put that
join in front of people, in the product they already use, without becoming a
second observability vendor.

The goals are four. First, a **per-chat Trace/Cost tab**: for one chat or run,
what happened, how long each step took, what each model call cost. Second,
**Agents › Insights**: org and team analytics that answer "is the agent work
worth it" with numbers computed from first-party keys (§3). Third, **real cost**
in reliant, which today has no price at all (§4). Fourth, **standards in, standards
out**: emit gen_ai-conformant spans from forge's own library (§5), let an org
send them to its own backend (§6), and accept the OTEL that Claude Code, Codex
and Gemini CLI already produce (§8).

The non-goals matter as much. Reliant will **not** build a standalone, competing
OTEL LLM backend: tracing, token counting and LLM-as-judge are commodity
and OSS-heavy, OTLP-in everywhere, and the vendors have consolidated around them
(ClickHouse–Langfuse, ServiceNow–Traceloop, CoreWeave–W&B, Vercel–Traces). It will
not ship a prompt CMS, an eval-dataset product, or tracing for agents that are
not on Reliant as a product in its own right (§8 ingests their telemetry for
spend visibility only). It will not rank individual people: nothing here is a
leaderboard (§3.9).

**What dedicated vendors will still do better**, and where an org should keep
using them (this is why §6 exists):

- Prompt management with versioned deploys, and eval datasets with experiment
  diffing (Braintrust, Langfuse, LangSmith).
- Human annotation queues and LLM-as-judge on live traces at volume.
- Browser-agent session replay synced to the trace (Laminar, AgentOps, PostHog).
- Correlation of LLM traces with the rest of an org's APM and infrastructure
  (Datadog, Sentry), for agents Reliant did not run.
- Free-form SQL notebooks and long retention over traces (Logfire, Laminar). Our
  traces live seven days (§2.2); an exporting org can keep them as long as it likes.
- Prompt and preset optimisation loops.

The rule is: Reliant is the best place to ask "what did my agents cost and did
the work ship", and a good citizen that hands everything else to the tool built
for it.

## 2. The per-chat Trace/Cost tab

### 2.1 What exists

Reliant emits the right tree today, on the worker only
(`internal/telemetry/ai_spans.go:15-30`):

```
RunWorkflow:DynamicWorkflow     gen_ai.operation.name=invoke_workflow
└ workflow.node <id>            StartNodeSpan (ai_spans.go:42)
  └ invoke_agent <agent>        StartAgentTurn (ai_spans.go:58)
    ├ chat <model>              StartGenAISpan (internal/llm/genai_span.go:40)
    └ execute_tool <tool>       StartToolCall (ai_spans.go:101)
```

A tool call is parented to the turn that asked for it through a traceparent
carried in the workflow's data (`TraceParentOf`, `ai_spans.go:163`). The daemon
exports no spans at all (`internal/observability/propagation.go` installs a
propagator only, on purpose: a local daemon sends nothing off the machine), so
tool timing is the worker's view of the call, transport included. The chat span
carries tokens, cache and reasoning tokens, a first-token event
(`genai_span.go:81`), retries, and the gateway-reported cost under both
`forge.ai.cost.usd` and `reliant.cost_usd` (`genai_span.go:131-136`;
`telemetry/ai_attrs.go:65-66`). Run identity is `forge.ai.run.id`,
`forge.ai.workflow.name`, `forge.ai.node.id` and the rest of the `forge.ai.*` set
(`ai_attrs.go`); `gen_ai.conversation.id` is the chat id and every span is meant to
carry `org.id` (`ai_attrs.go:69`, `run_scope.go:137`, which leaves out an empty
field, so a span opened outside a run scope has no org: §6.3).

The frontend side is ready too. `TraceWaterfall.tsx` and `traceTree.ts`
(`frontends/web/src/components/Forge/Observability/waterfall/`) already draw a
waterfall for a customer's forge app. The chat view has only a context-window
indicator (`components/Chat/ContextUsageIndicator.tsx`): no per-chat cost,
tokens or latency anywhere.

What is missing is a way for a person to *read* their own chat's spans. The spans
go to Reliant's own ClickStack (`cp: deploy/kcl/prod/main.k:2006-2007`,
`obsprod.reliant_otel_env`), which only staff can query, and no RPC takes a chat id.

### 2.2 The API

A new `ChatTraceService` in reliant (additive; new service and messages, v1-safe),
one read RPC, `GetChatTrace(chat_id, optional run_id, optional time bounds)`. Its
authorization is **the chat's own**, not the observability service's: it calls
`authz.ReadChatAccess` (`internal/authz/authz.go:466`) once and acts on the answer.

| Caller | Answer | What they get |
|---|---|---|
| Owner (and a live org member for an org chat, the same predicate `CanWriteChat` uses) | `AccessOwner` | Full spans: structure, timing, status, model, token counts, tool name, cost |
| Shared-chat viewer (`AccessViewer`, `authz.go:435`) | stubbed | **Stubbed spans**, below |
| Anyone else, a removed member, a credential bound to another org | `AccessNone` | NotFound, never PermissionDenied, so a chat's existence is not disclosed (as every other chat rpc) |

A **stubbed span** keeps what the viewer can already infer from the transcript
they are allowed to read and drops the rest: span kind, operation name, start and
duration, status code, the tool *name* (tool inputs are visible to viewers; tool
results are elided, cp `docs/design/org-sharing.md` D2), and for model calls the model and token
counts. It drops error text (spans carry none anyway), every id except the span
id, `forge.ai.cost.usd` and any `reliant.*` attribute. This mirrors what
`chatproto.ForViewer` does to the chat itself (`internal/chatproto/chatproto.go:105`).
The viewer test matrix (`internal/viewertest/matrix.go`) gains this rpc, so a
future field cannot reach a viewer without a test naming it. A machine credential
is refused for a viewer (person sessions only, as for every viewer read).
Whether a viewer sees dollar cost is decision D4; the recommendation is no.

The response also carries the **Cost half** of the tab from Postgres, independent
of the span store: one row per model call from `llm_call_usage` (purpose, model,
tokens, latency, outcome, `error_class`) and the totals, with `completeness`
(`COMPLETE`, or `PARTIAL` with a named reason such as `trace_expired` or
`spans_missing`), the same honesty `GetOrgLLMUsage` already has
(`internal/handlers/org_usage/rpc_get_org_llm_usage.go:35`).

### 2.3 Where the spans are read from

The span store is ClickStack, and **traces there live seven days**
(`cp: proto/services/observability/v1/observability.proto:68`, "the traces TTL").
`llm_call_usage` lives 90 days (`internal/db/llm_usage.go:25`). So the tab is a
trace waterfall for a chat touched in the last week and a cost table for 90 days;
the design must say so on screen (`trace_expired`) rather than show an empty chart.

Three ways to read spans, and the one to choose is an architectural decision (D1):

| Option | Shape | For | Against |
|---|---|---|---|
| **A. Reliant reads tenant zero's ClickStack through a narrow control-plane RPC** | cp adds `GetConversationTrace(org_id, conversation_id, time_range)` run as a service identity. Reliant authorizes the caller (§2.2) and passes only the chat's own id and org. The query filters spans by `gen_ai.conversation.id` **and** `org.id`, never by trace id | Works today with no routing change; one hop; reuses the 7-day store and the ClickHouse the console already uses | The tenant boundary here is code, not the database: ObservabilityService runs each query as the org's own ClickHouse user (`EXECUTE AS r_<org>`, `observability.proto:29-30`), and this path would not. One mistake reads another org's spans. It must be a query that cannot take a free filter |
| **B. Route the org's spans into the org's own ClickStack tenant (§6 phase 2) and call the existing `ObservabilityService.GetTrace` with `scope.org_id`** | The database is the tenant boundary, as for a customer's forge app | Strongest isolation, and reuses an RPC and its tests | `GetTrace` authorizes on org membership or an `obs:read` grant, not on the chat. A member who may not see a chat could read its spans; viewer stubbing cannot happen in the database. It also makes the tab depend on §6 shipping first |
| **C. No span store: build the tab from Postgres** | `step_executions` (`internal/db/core/workflow.go:100`, `duration_ms`, `success`, `attempt`, `run_id`) plus `llm_call_usage` plus messages | No new infrastructure, 90 days, trivially owner-scoped | No tool durations (only spans have them), no first-token time, no retry structure inside a call; not a waterfall |

**Recommendation: C for the Cost tab in the first release, and A for the Trace
tab with the query shape fixed in the control-plane RPC** (typed, conversation id
and org id required, a time range of at most seven days, and the response
containing only span fields in an allowlist). B is the destination for orgs that
export (§6), and the same `ChatTraceService` can read from it later without an API
change. A is chosen over B because the viewer-stub requirement is a property of the
*chat*, which only reliant knows, and over "give staff-level read to reliant" because
an allowlisted typed RPC fails closed. Argued against: A keeps customers' traces
in the staff store, which is the status quo today, and §6 should be read as the
plan to stop depending on it.

One defect to carry into the RPC's contract: today **one trace can span several
orgs**. forge's `temporalotel` interceptor
(`internal/temporal/external_client.go:97`) parents a launched run under the trigger
fire activity's span, so an integration fan-out puts one trace across several orgs.
`GetChatTrace` therefore reads by `gen_ai.conversation.id`, drops every span whose
`org.id` is not the chat's org, and treats a span with no `org.id` as not the chat's.
The fix (a launched run starts a new root trace with a span link to the firing span)
is separate work in this line; the API is correct without it and merely cleaner
with it.

## 3. Agents › Insights

Insights is a page under **Agents** (`agentsArea`,
`frontends/web/src/components/Layout/sidebar/navConfig/items.tsx:439`), beside the
chat list and the inbox; it is not in Usage & billing. It answers the owner's
questions with numbers, each of which names its source and the join that makes it
exact. "Exact" is meant literally: where a number can only be approximated, the
table says so and the page shows the basis.

### 3.1 Why Insights needs its own store

Every raw source decays. `llm_call_usage` rows are deleted at 90 days and keep
only a per-user daily rollup with no latency (`db/migrations/20261030225528_llm_call_usage.up.sql`);
internal `step_executions` are pruned for completed root runs
(`internal/db/ledger_retention.go:126`); traces live seven days. A chart of
"success rate by model, last six months" cannot be computed from any of them. So
Insights reads **its own rollups, written at the time the thing happens**, the
pattern `llm_call_usage_daily` already uses (maintained in the same transaction as
the per-call row). Two rollup tables, deliberately separate: an **org/team** table
keyed by `(day, org_id, project_id, workflow_name, model, funding_class, …)`
holding sums and latency histogram buckets, and a **member** table that adds
`user_id`. The team page and the admin page read different tables, so a bug in the
team query cannot print a person: the privacy rule (§3.9) is enforced by the schema
and not only by a handler `if`.

### 3.2 Join keys, and the gaps in them

The keys that already exist are `chat_id` (on `llm_call_usage`, on the spans as
`gen_ai.conversation.id`, on `approvals`, `questions`, `workflows`, and as
`ChatID` on the gateway's `UsageEvent`, `cp: internal/db/models.go:727`) and the
Temporal workflow id. The gaps, each fixed additively:

- **`llm_call_usage.run_id` is Temporal's run id, not `forge.ai.run.id`**
  (`internal/workflow/runtime/activities/handlers/llm_usage_record.go:92` writes
  `info.WorkflowExecution.RunID`). It cannot join to `step_executions.run_id` or
  `workflows.run_outcome`, which use the forge run id. A call also has no
  `workflow_id`, `thread_id`, workflow name, node, preset or project. The
  recorder already holds all of these in the run scope it opens spans from
  (`telemetry.RunScopeFromContext`), so PR 1 adds them as nullable columns.
- **No provider request id.** The exact join between a call row, its span
  (`gen_ai.response.id`) and the gateway's `UsageEvent.ProviderRequestID` is the
  provider's request id; `llm_call_usage` does not store it. PR 1 adds
  `provider_request_id`. Whether the span's `UpstreamRequestID` and the gateway's
  `ProviderRequestID` are the same value is **unverified**; PR 1's first test
  settles it, and the fallback join is `chat_id` plus a time window.
  They are not the same value: the gateway's ledger stores LiteLLM's own
  `x-litellm-call-id`, while the span's `gen_ai.response.id` is the upstream
  `x-oai-request-id`. So a gateway-routed `chat` span also carries the gateway's
  id as `reliant.gateway.call_id`, on every call, failed ones included (only the
  `reliant` driver sets it), and a nullable `llm_call_usage.gateway_call_id`
  column follows A1 so the call row joins the ledger exactly too.
- **No chat → PR link.** `pr_records` (`db/migrations/20261121122326_pr_records.up.sql`)
  mirrors GitHub pull requests with `head_ref`, `repository_id`, `merged_at` and
  `merge_commit_sha`, and nothing names a chat or worktree. A worktree has a
  `Branch`, but its own comment says it is a creation-time label, "NOT a source of
  truth" (`internal/db/core/project_worktree.go:331-333`). `CreateWorktreePR`
  returns a `pr_url` and persists no link (`internal/handlers/worktree/rpc_create_worktree_pr.go:101`).
  PR 3 adds an explicit `chat_pr_links(chat_id, run_id, repository_id, pr_number, source)`,
  written where the PR is created (that rpc, and a workflow's GitHub PR step) and,
  as a fallback, when a `pr_records` row first appears whose `head_ref` and
  repository match a chat's worktree. Explicit beats inferred: a chat that pushes
  to a branch it did not create still links.
- **No tool table.** Tool calls are spans only. PR 2 adds a count-only
  `tool_call_usage` in the same spirit as `llm_call_usage` (§3.5).
- **Approvals have no actor yet.** `approvals` and `questions` record that
  something was decided, not by whom. The chat collaboration work adds
  `approvals.decided_by` and `questions.answered_by` (NULL meaning the owner); until it
  ships, per-person intervention rates read the owner only.

### 3.3 The metrics

| Metric | Source | The join that makes it exact | Exact? |
|---|---|---|---|
| **Cost per merged PR** | Cost from §4 per `chat_id`; PR from `pr_records.state='merged'`, `merged_at` | `chat_pr_links` → `pr_records`; sum cost of every linked chat's runs up to `merged_at`; divide by merged PRs in the window. A PR with several chats counts all their cost; a chat with several PRs splits by PR count, shown | Exact for priced calls; the page shows the share of spend that was unpriced (§4) |
| **Cost per shipped release** | The above, per release | `pr_records.merge_commit_sha` ↔ the release ledger's `git_commit` (`cp: proto/services/delivery_insights/v1/delivery_insights.proto:119`, `ListReleases`). A release contains the PRs merged since the previous release's commit | **Not exact in v1.** "Merged since" is git ancestry, which neither table records. Order by `merged_at` between two promotions' commit times and label it approximate; exact needs ancestry (a compare call, which spends GitHub API quota, §3.8) |
| **Human-intervention rates** | `approvals` (created, resolved, status, `batch_id`), `questions` (created, resolved, `async`, withdrawn), user messages in a run's window | Per run: approvals requested, granted, denied; questions asked; user messages sent while the run was live. **Autonomy rate** = runs ending with none of those ÷ runs. **Time-to-approve** = `resolved_at − created_at`, the measured review bottleneck | Exact per run. Per person only after `decided_by` ships |
| **Workflow step success** | `step_executions` (`success`, `duration_ms`, `attempt`, `loop_iteration`) and `workflows.run_outcome` (`succeeded` / `failure_handled` / `failed`, `proto/reliant/v1/run.proto:250-257`) | `step_executions.run_id` = `forge.ai.run.id` = `workflows` run. Per workflow, per node, per loop; first-attempt success = attempt 1 succeeded; `failure_handled` is reported on its own, never counted a success | Exact; rolled up before step pruning removes the rows |
| **Model comparison on real traffic** | `llm_call_usage` (`model`, `outcome`, `error_class`, `latency_ms`, tokens) joined to `run_outcome` and, where linked, merged-PR | Same workflow name across models: call error rate and `error_class` mix, p50/p95 latency, tokens per run, run success, cost per success. Best of Three already produces a paired comparison | Exact; latency percentiles need the histogram the daily rollup lacks today (§3.1). Cache-read ratio is `cache_read ÷ (input + cache_read)` |
| **Tool and daemon error rates** | `tool_call_usage` (new): tool name, location (worker/daemon), outcome, closed `error_class`, `duration_ms`. Machine failures from the run placement and recovery records (`run_machine_placements`, migration `20261121043858`) | Tool row → run by `run_id`; machine failure → run by placement. Recovered vs lost runs ties to the "always recover" rule | Exact for what the worker saw. The daemon exports no spans, so a daemon-side tool failure is measured as the worker observes it (error class `transport` vs `tool`) |
| **Wasted spend** | `llm_call_usage` outcomes and attempts, `run_outcome`, archive state, links | Five labelled buckets, shown separately and never summed into one number: tokens of calls that ended `error` or `cancelled` (exact); spend of runs that ended `failed` (exact); spend on compaction and title calls (exact, from `purpose`); retried attempts beyond the first; spend of **archived chats with no linked PR or completed run** (a heuristic, labelled as one) | Mixed, and the page says which |
| **GitHub API spend per workflow** | `integration.call` spans from the workflow-observability line (not on main at the pinned commit): call count, status, `reliant.integration.rate_limit.*` | Span → run → workflow name, rolled into a daily `(workflow_name, resource)` count before the seven-day span TTL; the bot's per-machine `bot-gh.sh` JSONL is not queryable and must move onto integration steps | Call **counts** exact; remaining-budget burn is approximate (limits are per token and resource, shared with other callers) |

### 3.4 Cost, in brief

Cost has its own section (§4) because everything above that says "cost" depends
on it. Until §4's column exists, Insights shows tokens and rates and no dollars,
rather than inventing a number.

### 3.5 What a new table may hold

`tool_call_usage` follows `llm_call_usage`'s rule and extends its guard:
`TestLLMUsageTablesHoldNoContent` (`internal/db/llm_usage_test.go:120`) fails when a
column appears that it does not name. No tool input, output, command line,
file name or error text; `error_class` is a closed set enforced by `CHECK`. The
new `llm_call_usage` columns and both rollup tables are added to the same test.
New tables are added by hand to account purge
(`internal/accountpurge/accountpurge.go:259-272`) and project delete
(`internal/store/project_delete.go`), because there is no foreign-key cascade.

### 3.6 Who sees what

**Team level is the default.** The Insights page shows aggregates by project,
workflow, model, repo and day, and a small-group floor: a breakdown bucket with
fewer than five contributing members (D5) is shown merged into "other". Nothing on
the team page names a person.

**Per-person drill-down is for org admins only**, read from the member rollup,
using the org role read fresh (`orgs.Resolver.RoleFresh`, `internal/orgs/orgs.go:209`),
person credentials only (`human_only`, as org-sharing.md D8), and **audited**:
each drill-down writes an audit row (who looked, at whom, which window), visible to
org admins and, for their own row, to the member.

**"My analytics"** is a view for every member of their own numbers: their cost
(of org-funded calls), runs, approvals decided, and their PRs. It reads the member
rollup scoped to the caller, so a member can always see what an admin could see
about them.

Two more rules. Insights never shows transcript content; it links to the chat,
which stays governed by sharing ACLs. And **cost counts org-funded calls only**: a
member's personal subscription or key is excluded by design (the same rule as
`GetOrgLLMUsage`), so on an org chat paid personally the *outcome* counts but the
cost column is blank and the page says "personally funded" (D6).

### 3.7 Where it appears

A row "Insights" in `agentsArea`'s children (`items.tsx`), a route under Agents, and a
"My analytics" tab on the same page. The per-chat Trace/Cost tab lives in the chat
view; its exact host (the existing execution sidebar or a header tab) is chosen in
PR 5 after looking at `ChatHeader` and the sidebar, since the chat view has no tab
system today (**unverified** which is cheaper).

## 4. Real cost in reliant

reliant keeps no price table. The gateway's reply carries a `cost`, the reliant
driver passes it through, and a call the gateway did not price is **0, not an
estimate** (`internal/llm/drivers/reliant/driver.go:1100-1127`,
`internal/llm/types.go:95`). It reaches spans (`genai_span.go:131`), and it never
reaches Postgres: `llm_call_usage` has tokens, outcome and latency but no cost.
control-plane has billed cost (`UsageEvent.Actual/BilledCostUSDNanos`) only for
gateway-paid calls; BYOK, subscription and local calls bypass the gateway.
`GetOrgLLMUsage` estimates org-key spend at list price and labels it an estimate,
never `COMPLETE`.

| Option | What it is | For | Against |
|---|---|---|---|
| **1. Record the reported cost at write time** | `cost_usd_nanos` and `cost_basis` on `llm_call_usage`, filled by the CallLLM activity from `TokenUsage.Cost` | Priced once, at the moment of the call, so rollups are stable. Exact for managed calls. No cross-service read | Empty for BYOK, subscription and local |
| **2. Join control-plane's `UsageEvent` at read time** | `provider_request_id` or `chat_id` to the gateway ledger | One authoritative source; also reconciles | A cross-service join per page, and gateway-paid calls only. Wrong place for a hot analytics read |
| **3. Estimate unpriced calls from a list price** | LiteLLM's catalog, as `org_usage` already does | Gives BYOK a number | An estimate that can be summed with real cost by mistake; prices change and must be pinned to the call's date |
| **4. Leave unpriced calls unpriced** | tokens only | Never wrong | Cost per merged PR is silently low for any org on its own keys |

**Recommendation: 1 and 3 together, with 2 as a reconciliation job and never the
read path.** Every row gets `cost_basis` in a closed set: `reported` (the gateway
said so), `estimated` (list price at call time, stored with the catalog date) or
`unpriced` (subscription and local: tokens only; a seat is not a per-call price).
NULL means not yet written and is never read as zero. The UI never adds
`estimated` into a `reported` total: it shows both, with the unpriced share
("78% of tokens priced, 12% estimated, 10% subscription") on every cost figure and
on cost per merged PR. A nightly job compares reported cost against `UsageEvent`
for the managed calls it can match and files a mismatch rather than correcting it.
The honest alternative is option 4 until an owner-approved price source exists;
the cost of choosing it is that a BYOK org's headline number is understated.

## 5. Adopt forge `pkg/observe/genai`

reliant keeps its own copy of what forge now ships. The pinned forge has
`pkg/observe/genai` (`forge:pkg/observe/genai/{spans,attrs,sampler}.go`):
`StartChat`, `StartAgent`, `StartTool`, `StartWorkflow`, `StartNode`, `SetUsage`,
`SetResponse`, `FirstToken`, `RetryWait`, `End`, `WithScope`, the attribute names
(`forge.ai.cost.usd` is `AttrCostUSD`, `attrs.go:185`) and `KeepSampler`
(`sampler.go:31`), which records and keeps every AI span regardless of the base
sampler, because "token totals are sums over AI spans, so a sampled-away call is
a missing cost". reliant's copy is `internal/telemetry/ai_spans.go`,
`internal/telemetry/ai_attrs.go` and `internal/llm/genai_span.go`, and
`ai_attrs.go` says in its header that each constant "becomes an alias of forge's"
once reliant pins a release that has the package. It does now.

The change is a swap and not a redesign: constants become aliases, `StartGenAISpan`,
`StartNodeSpan`, `StartAgentTurn` and `StartToolCall` become thin wrappers over
forge's starters that add reliant's own attributes (`reliant.chat.id`,
`reliant.model_id`) through `WithScope` and the extra-attribute argument, and
`KeepSampler` wraps whatever sampler `observability.Init`
(`internal/observability/observability.go:107`) builds. Attribute and span names are
a public contract (customers' dashboards and, after §6, their backends read them),
so the PR's gate is a **golden test**: record the span tree of a scripted turn before
the swap, assert the same names, kinds and attribute keys after, and fail on any
key lost. `reliant.cost_usd` stays beside `forge.ai.cost.usd`. The daemon-side
behaviour, no spans, is unchanged. Doing this first removes two code paths for
every later PR in this document.

## 6. Tenant export (recommendation, pending the owner's confirmation)

The goal: an org can see its agents' telemetry outside Reliant. It is phased
because each step is useful alone and each adds a way to leak.

### 6.1 The three phases

1. **In product.** §2 and §3. No spans leave Reliant's own pipeline. This is the
   only phase that needs no routing change.
2. **The org's own forge obs.** Spans carrying `org.id = X` are routed into org
   X's ClickStack tenant, the one `ObservabilityService` already reads as that
   org's ClickHouse user. The org then has its agent traces in the same console as
   its apps, including the AI hub (`forge:pkg/obsbuiltin/ai.go`: models, workflows,
   failed runs, tools). This also makes §2.3 option B available.
3. **A bring-your-own OTLP endpoint.** An org admin configures a destination
   (endpoint, headers) and spans for that org go there: Langfuse, Datadog,
   Honeycomb, anything OTLP.

### 6.2 Mechanism

Spans already carry `org.id` and `gen_ai.conversation.id`, so the question is only
where the fork happens.

| Option | Shape | For | Against |
|---|---|---|---|
| **A. Per-org exporters in the worker** | The worker holds an exporter per configured org and fans out in-process | Simple; secrets stay in reliant | N live exporters in a latency-sensitive process; a slow customer endpoint becomes worker back-pressure; a leaked header lives in worker memory |
| **B. Collector routing by `org.id`** | The gateway's OTLP stage routes by the span attribute to a per-org exporter built from control-plane's config | Failure and egress isolated from the worker; one place to enforce fail-closed, size and rate caps; staff and tenant copies are separate pipelines | Dynamic per-org config in a collector; control-plane owns it |
| **C. Read-back exporter** | A job reads each org's tenant store and forwards | Perfect isolation: only what the org's own tenant holds can leave | Minutes of delay, a polling job per org, and phase 3 only |

**Recommendation: B**, with phase 2 as "route to the org's tenant" and phase 3 as
"register one more exporter for that org" on the same router. The BYO header is a
secret sealed in the **org vault tenant** (the pattern org-sharing.md D11 chose for
org connections), never in a column. The endpoint is validated beyond today's
scheme and host check (`validateOTLPEndpoint`, `observability.go:179`): https only,
no private or link-local addresses, resolved at connect time (SSRF), with a
"send a test span" action, a per-org size and rate cap, drop-and-count on
failure, and never retry that blocks the pipeline. **Fail closed**: a span with
no `org.id` is dropped from every tenant destination and counted
(`spans_dropped_no_org`); it is never delivered to "the default". Staff keep
their own copy as today, which is the status quo and should be stated plainly in
the privacy page.

### 6.3 What it depends on

The cross-org parenting defect (§2.3): until a launched run starts a new root trace
with a link to the firing span, one trace holds spans of several orgs. Routing by
**span** `org.id` is still safe and never leaks another org's span, but it delivers
a tree with missing parents. The fix makes every trace single-org and is a
**precondition for phase 2 and 3**. The second is **org coverage**: any span opened
outside a run scope has no `org.id` today (the integration-call span outside a run
is the known case, and an agent tool with no org in scope). Phase 2 needs an
audit that every span creation site stamps `org.id`, with a test that opens each
kind of span and asserts the attribute. The third is **content**: nothing in this
section sends content; see §7.

## 7. Content capture (recommendation, pending the owner's confirmation)

Spans carry no prompts, completions or tool arguments, and
`TestLLMUsageTablesHoldNoContent` keeps analytics tables free of them. That stays
the default and the rule for everything in §3.

Inside the product, **no capture is needed**: the Trace tab can show message
content by joining the span to the chat's messages (`gen_ai.conversation.id` plus
the call's position), under the chat's own ACL, so a viewer sees exactly what
they could already read and no more. The OTel conventions themselves allow this:
`gen_ai.input.messages`, `gen_ai.output.messages` and `gen_ai.system_instructions`
are opt-in and may be stored by reference.

Capture is therefore only a question for **export** (§6) and for §9's classifier.
The recommendation:

- **Off by default, per org.** Turned on by an **org admin** (role read fresh,
  person credentials, `human_only`), not a member and not a workflow.
- **Audited.** An `org_telemetry_settings` row and an append-only audit row for
  every change: who, when, which scope (prompts and completions; tool arguments),
  and the destination it applies to. Capture without a destination does nothing.
- **Tool results are a separate, second opt-in.** Tool results embed file and
  command output read off the owner's disk and are exactly what a viewer is not
  allowed to see (cp `docs/design/org-sharing.md` D2). One switch must never imply the other.
- **Never on the staff copy.** Content attributes are added only on the tenant
  destination's branch of the pipeline and stripped from the staff copy by a
  processor, with a test that a content attribute never reaches the staff
  exporter. This is the single most important property of the feature: capture must
  not turn Reliant's own ClickStack into a store of customer prompts.
- **Redacted and bounded.** The same secret patterns as `internal/telemetry/redact.go`
  run first; a per-attribute size cap; truncation is flagged.
- **A disclosure.** Exported content bypasses the chat's sharing ACL, so the
  setting says that an org admin enabling it makes transcripts readable by whoever
  holds the destination.

Argued against: an org that wants content in Langfuse gets nothing from
"reference only". That is the real cost of the default, and the owner may prefer
an org-wide switch with no second opt-in for tool results; the document recommends
the stricter shape because the failure it prevents (secrets in a third-party log)
cannot be undone.

## 8. Ingesting Claude Code, Codex and Gemini CLI OTEL

People run these CLIs on their own machines and on Reliant machines, and each
exports OTEL that already says what was spent: Claude Code's metrics and events
(`claude_code.token.usage`, `cost.usage`, `pull_request.count`, `commit.count`,
`tool_decision`, `api_request`) and optional traces carrying `gen_ai.*`; Codex's
`[otel]` log events; Gemini CLI's traces, metrics and logs. The goal is one spend
and activity view, an "External agents" series beside Reliant-run agents, so an org
sees all its agent spend. It is not a tracing product for them (§1).

Where it lands. forge already has the edge: `otlp.reliantapi.com` takes OTLP with a
per-environment ingest token (`cp: deploy/kcl/lib/otlp_public.k`), and the AI hub
reads `gen_ai.*`. The pieces to add are an org-scoped token per member, a mapper
from each CLI's names into a small `external_agent_usage` table (source, session,
model, token counts, reported cost, lines, commits, PRs, tool-decision counts), and
the Insights series.

Three rules follow from what the CLIs send. **Identity comes from the token, never
from attributes**: Claude Code stamps the user's email; Insights ignores it and
attributes to the member the token was minted for (opaque ids only, the same rule
as Reliant's own spans). **Content is dropped at ingest by default**: Gemini CLI's
`logPrompts` defaults to true once telemetry is on, so the ingest path strips
prompt and tool-content attributes unless §7's opt-in is set. **Never summed into
org spend silently**: external usage is not Reliant-paid and not on Reliant's
wallet or keys; it is a separate series with the CLI's reported cost, labelled.

Two ways to get the telemetry in. A member or an admin sets the CLIs'
`OTEL_*` variables to the ingest endpoint (admins can push Claude Code's through its
managed settings, which also lock the destination). For a CLI that a **Reliant
workflow runs on a daemon**, the workflow's shell step sets the variables itself;
this needs the shell tool's `env` input, because `toolexec.ToolRequest.Environment`
never reaches any daemon (known defect). The conventions are still experimental
(`gen_ai_latest_experimental`), so the mapper is versioned and an unknown attribute
is kept as-is rather than failing the batch. This phase is later and independent of
§2 to §7.

## 9. Behaviour monitors (later phase)

Raindrop's "signals" are plain-language behaviours labelled over production runs:
"claimed tests pass without running them", "looped on the same failing command".
Reliant has the structured data to start without a model, and the transcript to go
further.

**9a, deterministic signals from structured data**, no new model cost: the same
command run more than N times failing (repeated failing `step_executions` for one
node), a run that hit `rate_limit` or `usage_limit` and ended `failed`, a run
ending `failure_handled` with a gate skipped, an approval left pending past a
threshold, a tool failing repeatedly in one run. Each is a `signal_hits` row
(`signal`, `run_id`, `chat_id`, count), charted in Insights and alertable through
forge's alerting. **9b, classified signals**: an org-written description labelled by
a cheap model over a run's transcript. This is a new use of transcripts, so it is
org-opt-in, runs as an org-funded call (the org pays for the classifier, visible in
Insights as its own `purpose`), reads content under §7's rules, and exposes only
hits and counts, never the quoted text. The honest limit is classification cost and
precision, which is why it comes last.

## 10. Phased PR plan and tests

Each PR is independently shippable, additive (semver 1), and verified with
`task test:short -- ./internal/<pkg>/...` for the packages it touches, `go build ./...`
and `forge lint --scope <paths>`. Migrations are backward-compatible
(`-- forge:backward-compatible`) and made with `task db:migration`.

| PR | Repo | Change | Tests |
|---|---|---|---|
| **A0** | reliant | §5: adopt `forge/pkg/observe/genai`, `KeepSampler`, constants as aliases | Golden span-tree test (names, kinds, attribute keys before and after); every kind of AI span carries `org.id`; `reliant.cost_usd` kept beside `forge.ai.cost.usd` |
| **A1** | reliant | `llm_call_usage` gains `workflow_id`, `thread_id`, `workflow_name`, `node_id`, `preset`, `project_id`, `provider_request_id`, `ttft_ms`, `cost_usd_nanos`, `cost_basis`; recorder fills them from the run scope | `TestLLMUsageTablesHoldNoContent` extended; one test per column through a scripted activity; `provider_request_id` equals the span's `gen_ai.response.id` (settles the §3.2 unverified join); NULL cost never reads as zero; purge covers new columns |
| **A2** | reliant | `tool_call_usage` (count-only, closed `error_class`), written from the tool paths (`execute_tools.go`, `invoke_tool.go`) | No-content guard covers it; outcome and class per tool kind; duration matches the tool span; account purge and project delete include it |
| **A3** | reliant | `chat_pr_links`, written by `CreateWorktreePR` and the workflow PR step, with the `head_ref` fallback | Explicit link wins over inferred; a chat with two PRs and a PR with two chats; a force-pushed head; viewer and outsider never read links; purge |
| **A4** | reliant | Insights rollups: org/team table and member table, written transactionally with the source row; latency histograms; backfill from the 90 days that exist | Rollup equals a direct sum over fixture rows (property test); a retried attempt is counted once; team table has no `user_id` column (schema test); backfill is idempotent |
| **B1** | control-plane | Typed `GetConversationTrace` (conversation id and org id required, ≤ 7 days, allowlisted fields) | A span of another org with the same conversation id is never returned; a span with no `org.id` is never returned; a free filter is not accepted; the 7-day cap |
| **B2** | reliant | `ChatTraceService.GetChatTrace`, cost half from Postgres, `completeness` | Viewer matrix: owner / viewer / outsider / removed member / machine credential / other-org credential; viewer spans contain no cost, no `reliant.*`, no results; `trace_expired` is `PARTIAL`, not empty; opening does not touch `unread` or `last_active` |
| **B3** | reliant web | Chat Trace/Cost tab reusing `TraceWaterfall`; owner and viewer states | Vitest per component; `web-styling` rules; browser pass on `forge env up dev` as owner, viewer, outsider |
| **C1** | reliant | `InsightsService`: team RPC (org role), member drill-down (admin, fresh role, audited), self RPC | Team RPC never returns a person; floor merges buckets under five contributors; drill-down writes an audit row, is refused for a machine credential and a non-admin; a member reads their own row; personal-funded cost is blank, not zero |
| **C2** | reliant web | Agents › Insights page and "My analytics" tab, with the unpriced-share disclosure | Vitest; browser pass for member, admin and non-member; empty and partial states |
| **C3** | reliant | Cost per merged PR and per release (approximate label), wasted-spend buckets, GitHub-API series | Cost-per-PR fixture with managed, BYOK and subscription calls and the displayed split; release join marked approximate; buckets are never summed |
| **D1** | reliant, control-plane | §6 phase 2: route by `org.id` to the org's tenant; fail closed; org-coverage audit. *Pending owner confirmation; needs the cross-org parenting fix* | Spans of org A never reach org B; no-`org.id` span dropped and counted; every span creation site stamps `org.id`; staff copy unchanged |
| **D2** | control-plane, reliant | §6 phase 3: BYO OTLP destination, vault-sealed headers, SSRF-safe validation, caps, test-send. *Pending owner confirmation* | Private and link-local targets refused; header never in a column or a log; slow endpoint does not block the pipeline; per-org rate cap |
| **D3** | reliant, control-plane | §7: capture settings, audit, tenant-branch-only content, redaction. *Pending owner confirmation* | A content attribute never reaches the staff exporter; tool results need their own switch; every change audited; capture without a destination is inert |
| **E1** | reliant, control-plane | §8: member ingest tokens, mapper, `external_agent_usage`, "External agents" series | Identity from the token, not the email attribute; Gemini prompts stripped by default; external cost never in org spend; unknown attributes kept |
| **F1** | reliant | §9a deterministic signals, alerts | One test per signal on fixture runs; thresholds configurable; no content read |
| **F2** | reliant | §9b classified signals. *Later; needs §7* | Org-funded and attributed to its own `purpose`; hits and counts only, no quoted text |

Order: A0, A1, A2, A3, A4 are the data line and are independent of the UI. B1, B2,
B3 are the per-chat tab (B3 after B2; B2 after A1 for the cost half). C1 to C3
need A4 and, for cost, A1. D1 to D3 wait for the owner's confirmation and for the
cross-org parenting fix. E1 and F1 are independent of D. A first useful release is
A0 to A4 plus C1 and C2 (team Insights, tokens and rates, no dollars beyond
`reported`), then B1 to B3.

## Decisions for the owner

The direction (§1) is settled. These are the choices inside it. Items 7 and 8 are
the two asked on 2026-11-22 and are recommendations, not decisions.

1. **Where the Trace tab reads spans (§2.3).** Recommend a typed, allowlisted
   control-plane RPC over tenant zero's ClickStack, with the Cost tab from Postgres,
   and the org's own tenant (§6) as the later source behind the same API.
   Alternatives: wait for tenant routing (B), or Postgres only (C). Trade-off:
   fastest to ship and chat-ACL-correct, against a tenant boundary that is code
   rather than the database, on a seven-day trace TTL.
2. **How cost gets into reliant (§4).** Recommend recording the gateway-reported
   cost at write time plus a stored, labelled list-price estimate, with a closed
   `cost_basis` and the unpriced share shown on every figure. Alternative: tokens
   only until a price source is approved. Trade-off: a usable cost per merged PR now,
   against estimates that the page must keep from being summed with real cost.
3. **Insights reads its own rollups (§3.1).** Recommend two rollup tables (team
   and member) written transactionally, so history outlives the 90-day and 7-day raw
   sources and the privacy rule is in the schema. Alternative: query the raw tables
   and live with short windows. Trade-off: two tables and a backfill, against charts
   that stop at three months.
4. **Do shared-chat viewers see dollar cost (§2.2).** Recommend no: stubbed spans
   show structure, timing, model and tokens. Trade-off: a reviewer cannot see what a
   chat cost, against spend being disclosed to everyone the chat is shared with.
5. **Privacy floor (§3.6).** Recommend team-level default, a five-contributor floor
   for any breakdown, admin-only audited per-person drill-down, and "My analytics"
   for each member. Trade-off: small orgs see merged "other" buckets, against a leader
   being able to identify one person by slicing finely.
6. **Personally funded calls on an org chat (§3.6).** Recommend cost counts
   org-funded calls only, and outcome counts include the chat with a blank cost.
   Trade-off: cost per merged PR is "org-funded cost per PR", against disclosing
   what a member spent on their own subscription.
7. **Tenant export mechanism and phasing (§6), pending confirmation.** Recommend
   collector routing by `org.id` (B), in-product first, then the org's tenant, then
   BYO OTLP, fail closed on org-less spans, and the parenting fix first. Alternative:
   per-org exporters in the worker. Trade-off: isolation and one enforcement point, at
   the cost of dynamic per-org config in control-plane.
8. **Content capture (§7), pending confirmation.** Recommend off by default,
   org-admin opt-in, audited, tool results a separate second opt-in, never on the
   staff copy, and no capture needed in product. Alternative: one switch for both.
   Trade-off: an exporting org must opt in twice for tool results, against secrets
   read off a disk landing in a third-party log by one click.
9. **Who administers (§3.6, §6, §7).** Recommend the org role (admin or owner),
   person credentials only, read fresh, as org-sharing.md D8 decided for org
   connections. Trade-off: no new forge scope, against a role check in place of a grant.
10. **The chat → PR link is explicit (§3.2).** Recommend a `chat_pr_links` table
    written where PRs are created, with the branch match only as a fallback. Alternative:
    infer from `head_ref` alone. Trade-off: one more table and write path, against links
    that survive a renamed or reused branch.
11. **Cost per release is approximate in v1 (§3.3).** Recommend shipping it labelled
    approximate (merge-time ordering between promotions) and making it exact when the
    release record carries ancestry. Trade-off: a usable number now, against one that
    is wrong at the edges.
12. **External-agent telemetry is a separate series (§8).** Recommend it never
    sums into org spend, and identity comes from the token. Trade-off: two figures on
    the page, against an org's "AI spend" including money Reliant did not charge.
