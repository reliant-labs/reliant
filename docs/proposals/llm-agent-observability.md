# LLM and agent observability: traces, evals, prompts as Changes, release gates

**Status:** proposal. The owner asked on 2026-11-26 how to compete with Langfuse
on LLM and agent monitoring when selling to agentic companies, and approved
writing this proposal. Nothing here beyond [Where it stands](#where-it-stands-2026-11-26)
is built. The owner accepted every recommendation of section 15 on 2026-11-26
("agreed with your recs"); they are the
[Settled (owner, 2026-11-26)](#settled-owner-2026-11-26) list, and
[section 15](#15-owner-decisions-decided-2026-11-26) keeps the options that were
weighed. Slices land as their own pull requests ([section 17](#17-rollout-plan)).
**Date:** 2026-11-26
**Related:** [changes.md](changes.md) (prompts as Changes, the readout),
[ml-workloads.md](ml-workloads.md) (the evals object model this reuses, decision 8
on continuous metrics, decision 22 on eval gates), [runtime-context.md](runtime-context.md),
[product-events.md](product-events.md), [start-anywhere.md](start-anywhere.md),
forge#2888 (the Python SDK track, docs only and open), reliant
`dev-docs/design/agent-analytics.md` (Reliant's own agent analytics, §7 content
capture), `docs/benefits.md` (`builtin-ai-hub`, status in-progress).

## Where it stands (2026-11-26)

More exists than the pitch usually says. A row is "released" only when a final
tag carries it: forge v1.11.0, control-plane v1.33.0, reliant v1.32.0.

| Piece                                                                                                                                                                                              | State                                                                                                                                                                                                             |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| GenAI span helpers, Go: `StartWorkflow` → `StartNode` → `StartAgent` → `StartChat` / `StartTool`, `SetUsage`, `SetResponse`, `FirstToken`, `RetryWait` (`pkg/observe/genai/spans.go`)              | Released in forge v1.7.0 (#2226). Span `Links` on the starters are on main only (#2751).                                                                                                                          |
| The attribute contract: `gen_ai.*` plus `forge.ai.*` (workflow, run, node, cost), declared semver-1 (`pkg/observe/genai/attrs.go:3-6`)                                                             | Released, v1.7.0.                                                                                                                                                                                                 |
| A sampler that never drops an AI span (`genai.KeepSampler`, wired at `pkg/observe/setup.go:173`)                                                                                                   | Released, v1.7.0.                                                                                                                                                                                                 |
| Six AI hub dashboards over raw `otel_traces` (`pkg/obsbuiltin/ai.go:23-29`): overview, workflows, models, users, errors, tools                                                                     | Released, hosted only (sql panels local HyperDX cannot draw, `ai.go:32-35`). Only the env is a filter; the rest are breakdowns.                                                                                   |
| Content capture (prompts, completions, tool I/O)                                                                                                                                                   | **None, on purpose.** `pkg/observe/genai/doc.go:82-86`: "Content capture is a later, opt-in phase with its own storage and redaction". The `llm` skill forbids content on spans.                                  |
| Cost                                                                                                                                                                                               | Only where a span reports one (`forge.ai.cost.usd`, `ai.go:94`). The benefit says "No price table yet" (`internal/benefits/benefits.yaml:4924-4945`).                                                             |
| Monitoring for an app forge never built, any language: `forge obs init` prints the standard `OTEL_*` variables and a per-env ingest token (`internal/cli/obs_init.go:3-25`)                        | Released, v1.5.0 (#2096). Sign-in required.                                                                                                                                                                       |
| Release health, `forge obs check` (`pkg/releasehealth`): error rate, latency, new and regressed issues, alerts, restarts, browser, probes, changed ops                                             | Released. **No AI signal.**                                                                                                                                                                                       |
| Hosted OTLP gateway with OTTL transforms and custom OTTL functions (control-plane `tools/otelcol-reliant/relianttransformprocessor`, `transform/browser_scrub`, `docs/observability/data-path.md`) | Released. No GenAI transform; forge's local collector has log transforms only (`internal/templates/project/otel-collector.yaml.tmpl:132-143`).                                                                    |
| Changes: flags, cohort rollouts, A/B with a readout (`pkg/changes`, `pkg/experimentstats`, control-plane `ChangesService`, `StopChange` as the kill switch)                                        | Released, beta ([changes.md](changes.md)). A variant can be `json` or `string`, **at most 8 KiB** (`docs/changes.md:42-43`). The goal is a product-event conversion only (control-plane `changes.proto:506-527`). |
| LLM gateway: LiteLLM behind `internal/llmproxy`, per-env `rlek_` keys for hosted apps declaring `forge.llm_env()` (control-plane #2005), budgets, spend caps, model policy                         | Released. Cost is LiteLLM's cost map, passed through (`internal/llmproxy/billing.go:9-17`). List-price estimates for BYO keys: `internal/llmlistprice`. The gateway's span has HTTP attributes only.              |
| Issues inbox, `LinkIssueChat`, alerts → the autopilot receiver → reliant's built-in `autopilot-oncall` workflow                                                                                    | Released (control-plane #1181, #1106; reliant #1216, #1259).                                                                                                                                                      |
| Product events (`observe.Track`, web `track()`), masked session replay, runtime code context                                                                                                       | Product events and replay released; runtime context "in implementation" ([runtime-context.md](runtime-context.md)).                                                                                               |
| Reliant's own agents emit GenAI spans at every level, with tokens and reported cost (reliant `internal/telemetry/ai_spans.go`, #2732, #1083); a per-chat trace API (`GetChatTrace`, reliant #3492) | Released. No content on spans; no Trace tab UI yet (generated mocks only). Full transcripts live in reliant's Postgres.                                                                                           |
| Trace waterfall and session replay UI (reliant `frontends/web/src/components/Forge/Observability/`)                                                                                                | Released, not GenAI-aware.                                                                                                                                                                                        |
| Scores, user feedback, LLM-as-judge, annotation queues, datasets, offline evals, prompt playground, Python or TypeScript GenAI helpers                                                             | **None.** Reliant's workflow scenarios are deterministic assertions, not scored evals.                                                                                                                            |

## Summary

**Don't compete on standalone tracing. Close the loop.** Langfuse, LangSmith,
Phoenix and Braintrust all trace an agent well, and Langfuse is open source with
about 38k GitHub stars. A thirteenth trace viewer wins nobody. What none of them
own is what happens _after_ the trace: the release that caused it, the rollout
that exposed it, the gate that should have stopped it, the alert, and the fix.
Forge already owns every one of those for ordinary services.

The proposal is one loop, made of parts that mostly exist:

```
 trace (any SDK, any convention)  ──►  score (user feedback, LLM judge, human queue)
        ▲                                         │
        │                                         ▼
 agent fix (Reliant workflow: PR,          quality SLIs in release health
 or stop the bad prompt arm)                      │  (`forge obs check`, promotion gates)
        ▲                                         ▼
 Inbox alert  ◄──  A/B readout per prompt variant (Changes, cohort rollout, kill switch)
```

To get a Langfuse user in the door, we do three things: accept their
instrumentation as it is, at ingest, so moving is an endpoint change; show them a
transcript-grade agent trace; and give them scores and evals. To keep them, we
put those scores in the release verdict, run their prompt changes as Changes, and
have Reliant fix what the alert finds. Langfuse cannot follow there without
building an APM, a deploy system and a flag platform.

## Settled (inputs to this proposal)

1. **Monitoring requires sign-in** (owner, recorded in the project context).
   There is no anonymous or claimable observability path. That covers every
   read of AI traces and content, and every score write except the end-user
   feedback that already travels as a product event.
2. **forge is usable piece by piece in any repo** ([start-anywhere.md](start-anywhere.md)).
   An agentic company's Python service adopts AI monitoring with `forge obs init`
   and nothing else: no rewrite onto forge, and no deploy through it.
3. **Changes covers cohort rollouts**; canary traffic splits are not built
   (owner, 2026-11-25). Prompt rollouts therefore ride Changes.
4. **Roll forward, never auto-rollback** (forge's deploy design).
5. **The Python SDK comes first on the ML track** (owner, 2026-11-26, recorded in
   forge#2888). The AI helpers in [section 10](#10-python-and-typescript-helpers)
   share it.
6. **forge never stands in for another tool** (forge `reliant.md`, Philosophy).
   We read other conventions; we do not re-implement OpenInference's or
   OpenLLMetry's instrumentors, or Langfuse's REST API.

## Settled (owner, 2026-11-26)

The owner accepted every recommendation of
[section 15](#15-owner-decisions-decided-2026-11-26) ("agreed with your recs",
2026-11-26 06:01Z). Item N is decision N, the `DN` of the
[rollout plan](#17-rollout-plan), and the sections above and below describe the
chosen option.

1. **Conventions are normalised at ingest, additively**: canonical keys are
   added and the originals kept, from one forge table compiled to OTTL, with
   golden vectors ([section 3.1](#31-one-canonical-shape-written-at-ingest)).
2. **Langfuse compatibility is the OTLP path alias**, Basic → token at that
   path, and the `langfuse.*` attributes, plus a migration guide and dual-run.
   Their REST API is not emulated ([section 3.2](#32-accepting-langfuses-otel-export)).
3. **Content capture is off by default**, declared per env in the release/env
   document and enforced at the gateway, in a separate table with its own
   retention; tool I/O is a second switch, and the org has a kill switch
   ([section 4](#4-opt-in-content-capture)).
4. **Content is read with a new scope, `obs:content:read`**, per env and not
   implied by `obs:read`. Every read is audited, and an agent reads content only
   with a person's grant.
5. **Online evaluators:** LLM judges run in a control-plane evaluator worker,
   code evaluators in the customer's code, and a Reliant workflow per batch
   comes later for agentic trace review ([section 6.2](#62-online-evaluators)).
6. **Evaluators are declared in the repo** (`evals.yaml` or a `forge.yaml`
   section), with their live state (on, sample, budget) in the control plane,
   like Changes.
7. **Prompts are files in the release**, and a Change picks among them; console
   edits open a PR as the edit path. A control-plane prompt store comes only for
   a design partner who needs it ([section 7](#7-prompts-as-changes)).
8. **The price table is LiteLLM's model cost map vendored into forge**, read
   through a query-time dictionary, with overrides shared with the ML track's
   price sheet ([section 9](#9-cost-a-price-table)).
9. **Quality gates on promotion warn first**: new findings warn, and offline
   eval gates are displayed first and block per env on opt-in, matching
   ml-workloads decision 22 ([section 8](#8-quality-in-release-health-and-promotion-gates)).
10. **One evals model with ML**: the ML spec's model, with its subject widened to
    a release and a Change variant ([section 6.4](#64-datasets-and-offline-evals)).
11. **One Python package shared with the ML track** (forge#2888), as a `genai`
    module, and a TypeScript subpath export
    ([section 10](#10-python-and-typescript-helpers)). The names are still to be
    chosen with the package.
12. **Open instrumentation and data, hosted loop**, with BYOC later as an
    enterprise offer ([section 13](#13-open-source-and-self-host)).
13. **AI data is billed on the flat GiB meter**, with no seats and no per-span
    unit, and judge calls at the gateway's passthrough price, once bytes per
    span are measured on the dogfood ([section 14](#14-pricing-angle)).
14. **Autopilot stopping a prompt arm is opt-in per change**
    ([section 12](#12-alerts-the-inbox-and-reliant-fixing-it)).

## 1. The market and where it stops

The full research, with a URL for every claim, is in the chat's scratch notes
(`llmobs/a-langfuse.md`, `b1-…`, `b2-…`). What matters for the design:

| Product                   | Strength                                                                                                                                                                                                                                                      | Where it stops                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Langfuse                  | MIT core, self-hostable with evals, playground and annotation free since June 2025. OTLP is its only write path in v4. Prompt management with labels. Online LLM-as-judge and code evaluators. Datasets, experiments, a CI action. Threshold alerts since v4. | Not an APM: its SDK drops non-LLM spans by default. Release and version are filter labels; no verdict on production traffic. Prompt A/B is two labels the app picks between at random, with no allocation and no statistics. Alerts capped at 2 to 100 per plan. Self-host is five stateful components; the enterprise parts (per-project access roles, retention, audit logs, server-side masking) are paid. Acquired by ClickHouse in January 2026; Cloud forced a v3 → v4 cutover on 2026-11-16. |
| LangSmith                 | Agents and environments first-class; pytest and Vitest eval plugins; alerts; Engine (failure clustering).                                                                                                                                                     | Self-host is enterprise only; per-seat pricing ($39 per seat); 14-day base retention.                                                                                                                                                                                                                                                                                                                                                                                                               |
| Arize Phoenix / AX        | Owns OpenInference, the most adopted LLM span convention (cost, retriever, guardrail, evaluator kinds, feedback on the span). Phoenix self-hosts free.                                                                                                        | Phoenix is ELv2 (no managed offering by anyone else); AX is per span with 15 to 30 day retention.                                                                                                                                                                                                                                                                                                                                                                                                   |
| Braintrust                | Eval-first; a GitHub Action posts eval diffs on PRs; unlimited users; six SDK languages; ingests OTLP logs and traces.                                                                                                                                        | Self-host moved to "legacy" in favour of BYOC; $249/month to leave the free tier.                                                                                                                                                                                                                                                                                                                                                                                                                   |
| Datadog LLM Observability | The widest eval set, attached to a real APM; reads GenAI, OpenInference and even `langfuse.observation.*` attributes.                                                                                                                                         | No free tier, no self-host, $160/month for 100k LLM spans.                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| Helicone                  | Proxy-based logging.                                                                                                                                                                                                                                          | **In maintenance mode** since Mintlify acquired it (2026-03-03), helping customers migrate away. A ready list of prospects.                                                                                                                                                                                                                                                                                                                                                                         |
| Traceloop / OpenLLMetry   | Apache-2.0 instrumentors for many providers.                                                                                                                                                                                                                  | Acquired by ServiceNow (March 2026); OpenLLMetry stays open. Its convention is one we must read.                                                                                                                                                                                                                                                                                                                                                                                                    |

Two facts shape everything below. First, **every serious product now accepts OTLP
and reads several conventions at once** (Datadog and LangSmith read three or
four). Ingest compatibility is table stakes, not a moat. Second, **nobody ties
quality to a release or a rollout on production traffic.** Langfuse's own docs
position Datadog as the complement for "infrastructure". We are both halves.

## 2. Experience

What an agentic company sees, from the first command to the loop:

1. **Day 0, no code.** `forge obs init prod` in their Python repo (sign-in
   required) prints `OTEL_EXPORTER_OTLP_ENDPOINT` and a token. They already use
   OpenInference, OpenLLMetry, the Vercel AI SDK, or Langfuse's SDK; they change
   the endpoint (Langfuse users: `LANGFUSE_BASE_URL` and the two keys) and
   redeploy. The AI hub fills in: runs, agents, tools, models, tokens, errors,
   p95, and **cost**, estimated from a price table where the span reports none.
2. **Day 1, content.** They turn on content capture for `staging` in one
   declaration ([section 4](#4-opt-in-content-capture)). A trace opens as a
   transcript: system prompt, messages, each tool call with arguments and result,
   nested agent steps, tokens, cost and latency per step, and a link to the
   browser session replay that started it.
3. **Week 1, quality.** Their app's thumbs-up/down becomes a score. They declare
   an LLM-as-judge evaluator ("did the answer cite a source?") on 5% of traffic.
   Bad traces go to an annotation queue; good and bad ones go into a dataset.
4. **The loop.** A new system prompt ships as a Change: 10% of users, sticky. The
   readout says the thumbs-up rate is better or worse with an interval, not a
   bare winner. A release that drops the judge score or doubles cost per run
   fails `forge obs check`. An alert on the new arm reaches the Inbox; Reliant's
   autopilot stops the arm in under a minute (stop never waits for approval) and
   opens a PR with a fix and a dataset case that reproduces the failure.

Steps 1 and 2 are where Langfuse is today. Step 4 is the pitch.

## 3. Ingest compatibility

### 3.1 One canonical shape, written at ingest

Four conventions describe the same call. The hosted gateway normalises every one
of them onto the canonical `gen_ai.*` and `forge.ai.*` keys the dashboards,
evaluators and release health already read, and **keeps the original
attributes**: it adds a canonical key only when that key is absent, and never
overwrites or deletes.

| Concept        | Canonical (OTel GenAI)                                                                            | OpenInference                                                                            | OpenLLMetry                                                                | Langfuse                                                                                   |
| -------------- | ------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| Span kind      | `gen_ai.operation.name` (`chat`, `invoke_agent`, `execute_tool`, `embeddings`, `invoke_workflow`) | `openinference.span.kind` (`LLM`, `AGENT`, `TOOL`, `CHAIN`, `EMBEDDING`, `RETRIEVER`, …) | `traceloop.span.kind` + `llm.request.type`                                 | `langfuse.observation.type`                                                                |
| Model          | `gen_ai.request.model`, `gen_ai.response.model`                                                   | `llm.model_name`                                                                         | `gen_ai.request.model`                                                     | `langfuse.observation.model.name`                                                          |
| Provider       | `gen_ai.provider.name` (`gen_ai.system` read only)                                                | `llm.provider`, `llm.system`                                                             | `gen_ai.system`                                                            | —                                                                                          |
| Tokens         | `gen_ai.usage.input_tokens`, `…output_tokens`, cache, reasoning                                   | `llm.token_count.prompt`, `.completion`, `…_details.*`                                   | `gen_ai.usage.prompt_tokens`, `.completion_tokens`                         | `langfuse.observation.usage_details` (JSON)                                                |
| Cost           | `forge.ai.cost.usd`                                                                               | `llm.cost.total`                                                                         | —                                                                          | `langfuse.observation.cost_details` (JSON)                                                 |
| Input / output | `gen_ai.input.messages`, `gen_ai.output.messages`, `gen_ai.system_instructions`                   | `llm.input_messages.{i}.message.*`, `input.value`, `output.value`                        | `gen_ai.prompt.{i}.*`, `gen_ai.completion.{i}.*`, `traceloop.entity.input` | `langfuse.observation.input`, `.output`                                                    |
| Agent, tool    | `gen_ai.agent.name`, `gen_ai.tool.name`, `gen_ai.tool.call.id`                                    | `agent.name`, `tool.name`                                                                | `traceloop.entity.name` on an agent or tool span                           | the observation name with type `agent` / `tool`                                            |
| Workflow       | `forge.ai.workflow.name`, `forge.ai.run.id`                                                       | `graph.node.*`                                                                           | `traceloop.workflow.name`                                                  | `langfuse.trace.name`                                                                      |
| Session, user  | `gen_ai.conversation.id`, `user.id`                                                               | `session.id`, `user.id`                                                                  | association properties (key names to verify)                               | `langfuse.session.id`, `langfuse.user.id`                                                  |
| Prompt version | `forge.change.key`, `forge.change.variant` ([section 7](#7-prompts-as-changes))                   | `llm.prompt_template.version`, `prompt.id`                                               | —                                                                          | `langfuse.observation.prompt.name`, `.version`                                             |
| Release, env   | `service.version`, `reliant.env.id` (resource)                                                    | —                                                                                        | —                                                                          | `langfuse.release`, `langfuse.environment` (kept as attributes; the token decides the env) |

The Vercel AI SDK's `ai.*` attributes are a fifth source with the same concepts;
its mapping is part of slice 1 and needs verifying against its current release.
The canonical span also gets `forge.ai.convention` (`genai`, `openinference`,
`openllmetry`, `langfuse`, `vercel-ai`), so a dashboard can show where data came
from and a mapping bug can be found.

**Where it runs.** The table is data in forge, `pkg/observe/genai/conventions`
(a versioned mapping plus golden vectors: an input span per convention and the
canonical span it must become). Two consumers compile it: the hosted gateway, as
OTTL statements in a `transform/genai_normalize` processor beside
`transform/browser_scrub`, and forge's local collector template. Indexed message
lists (`llm.input_messages.0.message.content`, `gen_ai.prompt.3.role`) cannot be
reassembled into `gen_ai.input.messages` JSON with stock OTTL; that needs one
custom function in `relianttransformprocessor`, which already ships custom
functions (`delete_bytes_values`). The local template is the user's file, so a
project picks up the processor through `forge project upgrade`, never a rewrite.

Why at ingest and not at query time: today `ai.go` already coalesces two
spellings of cost and cache tokens per query (`ai.go:60-105`). Five conventions
times every reader (six dashboards, evaluators, release health, the trace view,
the readout, `forge obs` CLI verbs) is a matrix that drifts. Normalising once
costs a few attributes per span (billed bytes) and makes every reader simple.
The alternative is [decision 1](#15-owner-decisions-decided-2026-11-26).

**Trace-level attributes propagate for free.** Langfuse asks users to copy
`user.id`, `session.id` and tags onto every span with baggage processors, or its
v4 single-table model loses them. We store spans and join by trace id, so a
session or user set on the root span applies to the whole trace in every reader.
That is one less migration step to advertise.

### 3.2 Accepting Langfuse's OTel export

Langfuse's v4 SDKs (Python v5, JS/TS v6) are OpenTelemetry under a thin layer,
and they export over OTLP/HTTP to `<base>/api/public/otel/v1/traces` with HTTP
Basic auth (`public key:secret key`) and an `x-langfuse-ingestion-version`
header. Three depths are possible:

| Depth                            | What a Langfuse user changes                                                                                    | What works                                                                                 | Cost to us                                                                                                                                       |
| -------------------------------- | --------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| (a) The OTLP path and attributes | `LANGFUSE_BASE_URL` to our edge; public key = anything (or the env id), secret key = the `forge obs init` token | All tracing, sessions, users, tags, metadata, usage, cost, input and output, prompt links  | One route alias on the OTLP edge, Basic → bearer translation at that path only, the `langfuse.*` rows above. The header is accepted and ignored. |
| (b) (a) plus their REST API      | Nothing more                                                                                                    | Also `langfuse.get_prompt()`, `create_score()`, datasets and experiments through their SDK | Emulating a moving third-party API (v3's APIs were deleted from their Cloud ten days ago). A second product to maintain, against principle 6.    |
| (c) Neither                      | Swap SDKs                                                                                                       | —                                                                                          | No code; a real migration project for every prospect.                                                                                            |

Settled: **(a)**, with a migration guide that maps each remaining Langfuse
call to its equivalent: `get_prompt` → a Change evaluation, `create_score` →
`score()` in our helpers, datasets → [section 6](#64-datasets-and-offline-evals).
(a) is reading a convention, the same as reading OpenInference; (b) is standing
in for another tool. **Dual-run** is the honest trial: an OTel Collector, or two
span processors, sends the same spans to Langfuse and to us, so a prospect
compares without switching. Importing historical Langfuse data (their blob
export) is out of v1.

## 4. Opt-in content capture

Content is table stakes for a Langfuse user and a liability for us. The rule in
`genai/doc.go` stands: no helper records content unless asked. What follows is
the "own storage and redaction" phase that comment promised.

**Declaration, per env.** One block in the release's observability declaration
(KCL, beside `ObservabilityRetention`), and in the env document
(`deploy/envs/<env>.yaml`) for monitoring-only envs that `forge obs init`
declared:

```
genai_content = forge.GenAIContent {
    messages = "on"          # prompts, system instructions, completions
    tool_io = "off"          # tool arguments and results: a second, separate switch
    sample = 0.25            # fraction of traces whose content is kept, per trace
    max_attribute_bytes = 65536
    retention_days = 14      # content's own retention, at most the env's traces_days
    redact = ["(?i)ssn:\\s*\\d{3}-\\d{2}-\\d{4}"]
}
```

| Concern     | Rule                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ----------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Default     | Off in every env, including dev.                                                                                                                                                                                                                                                                                                                                                                                     |
| Enforcement | **At the gateway, server side.** With capture off, the gateway deletes every content attribute of every convention (the input/output rows of the table in 3.1, and `gen_ai.*_ref`), whatever the SDK sent. A misconfigured client cannot put a prompt into storage. Langfuse sells server-side masking as an enterprise feature; here it is the default.                                                             |
| Tool I/O    | Its own switch. Tool results carry file contents, query results and command output, which is the data most likely to hold secrets and personal data. One switch never implies the other (reliant `agent-analytics.md` §7 reached the same rule).                                                                                                                                                                     |
| Redaction   | The gateway runs the platform's secret patterns (the `_BROWSER_SECRET_PATTERNS` used by `transform/dev_scrub`), then the env's `redact` patterns, before anything is stored. Our Python and TS helpers also take a client-side mask function, for data that must never leave the process. Named-entity PII detection is out of v1.                                                                                   |
| Sampling    | Deterministic on the trace id, so a trace's content is all there or not there. Spans themselves are never sampled away (`KeepSampler`). A span with an error status keeps its content regardless of `sample`, because that is the trace someone will open.                                                                                                                                                           |
| Size        | Per-attribute cap (default 64 KiB, maximum 1 MiB), truncation marked `forge.ai.content.truncated`, plus the edge's request limit. Images and audio are referenced, never inlined (out of v1).                                                                                                                                                                                                                        |
| Storage     | **A separate table**, `genai_content` in the tenant database: `(TraceId, SpanId, Kind, Role, Index, Body, Truncated, Sha256, Timestamp, RetentionDays)`, its own TTL, ZSTD. The span keeps a reference. Separate so that content has its own retention, its own grant, its own deletion (by `user.id`, for an erasure request), and so that scanning `otel_traces` for a dashboard never reads megabytes of prompts. |
| Read access | A person signed in, in the org, holding a content grant for that env. Settled: a new scope `obs:content:read`, not implied by `obs:read`, on the precedent of `deploy:config:read` ("ITS OWN SCOPE, NOT A DEPLOY READ", `pkg/accesstoken/scope.go`). Every content read is audited. An agent reads content only with a person's grant ([decision 4](#15-owner-decisions-decided-2026-11-26)).                        |
| Browser     | The anonymous browser pipeline (`/_otel`) never carries content: the gateway strips it there unconditionally.                                                                                                                                                                                                                                                                                                        |
| Billing     | Content bytes are ingest bytes on the same meter. Sampling and the size cap are the customer's cost controls.                                                                                                                                                                                                                                                                                                        |
| Staff copy  | Content never reaches tenant zero's staff view of a customer org. For Reliant's own product, the Trace tab joins spans to the chat's messages under the chat's ACL and needs no capture (`agent-analytics.md` §7).                                                                                                                                                                                                   |

Argued against: per-env declaration in the release means turning capture on
for prod is a deploy, not a click. That is deliberate (the declaration is
reviewed in git, and capture is a data-handling decision), and the `sample`
value is the live knob a console could later change within the declared
ceiling, as Changes does with `percent_bp`. An org-level kill switch that turns
capture off everywhere at once is in v1, because off must never wait for a
deploy.

## 5. The agent trace and session view

Reuse, don't fork: the console's `TraceDetail.tsx` and `waterfall/TraceWaterfall.tsx`
gain a **GenAI lens** that turns on when a trace carries `gen_ai.operation.name`,
and the same component becomes Reliant's per-chat Trace tab (the API,
`GetChatTrace`, already exists; only mocks render it).

- **Transcript.** The conversation in order: system instructions, user and
  assistant messages, each tool call as a collapsible step with its arguments and
  result, retries and their waits, the finish reason. Content comes from
  `genai_content` when captured; without it the lens shows the same steps with
  metadata only and says why.
- **Steps.** The nested tree (workflow → node → agent → model call / tool call),
  each with latency, time to first token, tokens in, out, cached and reasoning,
  and cost (reported, or estimated and labelled so).
- **Session.** Traces grouped by `gen_ai.conversation.id` (or `session.id` after
  normalisation), in order, with per-session totals: the "thread" view Langfuse
  and LangSmith have. Users: the same grouping by `user.id`.
- **Links out.** The browser session replay when the trace began in a
  browser session forge's web runtime recorded; the release
  (`service.version`) and the commit runtime context names; the Change and
  variant the call ran under; scores; "Add to dataset"; "Annotate"; and the
  console's existing Ask Reliant panel with the trace attached.
- **CLI and agents.** `forge obs trace <id> --transcript` prints the same
  structure as JSON, so a coding agent debugging the customer's app reads what a
  person sees. The reads are vetted `pkg/obsquery` templates, so the local and
  hosted answers agree (the `ExperimentReadoutQuery` precedent).

## 6. Scores, feedback, evaluators, annotation queues, datasets

### 6.1 Scores

A **score** is one judgment about one target:

| Field   | Values                                                |
| ------- | ----------------------------------------------------- |
| target  | a span, a trace, a conversation, or an eval sample    |
| name    | `helpfulness`, `user_feedback`, `cites_source`        |
| type    | numeric, boolean, categorical, text (Langfuse's four) |
| value   | number, or label, or text                             |
| source  | `user_feedback`, `llm_judge`, `code`, `human`, `api`  |
| author  | the person, or the evaluator key and its version      |
| comment | a rationale, from a judge or a reviewer               |

Stored in the tenant ClickHouse as an append-only `scores` table (an edit is a
newer row with the same id), so every reader joins scores to spans in one
database. Three ways in, one table:

1. **OTLP.** The GenAI convention's evaluation event (`gen_ai.evaluation.*`,
   Development stability; verify the field names when building) and
   OpenInference's `annotations.*` attributes are normalised into score rows at
   ingest. An SDK that already emits evaluations needs nothing new.
2. **API.** `ScoresService.CreateScore` on the control plane, under a scoped
   token: a backend that has its own reviewers, or an offline eval run.
3. **End-user feedback as a product event.** The app's thumbs-up calls
   `track("ai_feedback", {trace_id, value})` in the browser or
   `observe.Track` in the backend. It travels the product-event pipeline, with
   its consent and PII guards, and is materialised into a score. The bonus is
   large: **a feedback event is already a Changes goal metric**, so "thumbs-up
   rate by prompt variant" works with the readout as it ships today.

### 6.2 Online evaluators

An evaluator is a rule (which spans, by filter), a sample rate, and a scorer: an
LLM judge (a rubric prompt, a judge model, an output score), or code. Like a
Change, its shape lives in the repo and its live state in the control plane: an
`evals.yaml` (or a `forge.yaml` section) declares the evaluator key, rubric,
score name and type; the control plane decides on or off, the sample rate and a
monthly judge budget ([decision 6](#15-owner-decisions-decided-2026-11-26)). A rubric change is
reviewed and versioned like code, and every score names the evaluator version
that produced it.

Where it runs is [decision 5](#15-owner-decisions-decided-2026-11-26):

| Option                               | Fits                                                                                                                                    | Cost                                                                                                       |
| ------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| (a) A control-plane evaluator worker | LLM judges: read a batch of sampled spans and their content, one judge call each through the LLM gateway, write scores                  | Simple and cheap per sample. The judge call bills the org's LLM wallet at the gateway's passthrough price. |
| (b) A Reliant workflow per batch     | Agentic review that needs tools: cluster failures, explain a regression, propose a dataset case (LangSmith's Engine, Langfuse's Topics) | Heavier per sample; the right engine for judgment that is more than one call, and it dogfoods Reliant.     |
| (c) The customer's own code          | Code evaluators and judges they already have: run anywhere, write scores through the API or OTLP                                        | Nothing for us; always supported.                                                                          |

Settled: (a) for LLM judges, (c) for code evaluators in v1 (running a
customer's code on our side needs a sandbox we do not have for this), (b) for
agentic trace review as a later slice.

### 6.3 Annotation queues

A queue is a list of targets (filled from a filter, a low score, or by hand), a
rubric, and reviewers. Items, assignments and decisions are control-plane
Postgres rows; a decision writes a `human` score. The console page is a review
inbox with the GenAI lens. Agreement between reviewers, and between a judge and
people (to calibrate a judge), come after the first version.

### 6.4 Datasets and offline evals

**Reuse the ML evals object model** (the ML product spec, `00-object-model.md`
§3, which refines [ml-workloads.md](ml-workloads.md) section 2) rather than
inventing a Langfuse-shaped second one. The model already has every piece; the
LLM-app case only widens what an eval runs against:

| ML spec entity  | For an LLM app                                                                                                                                                                                                |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| DatasetVersion  | A dataset of items (input, optional expected output, metadata), versioned by content digest. Items come from production traces ("Add to dataset", needs content), files, or the API.                          |
| EvalSuite, Task | A named, versioned set of tasks, each a dataset plus a scorer configuration ("support-bot-gate-v3").                                                                                                          |
| EvalRun         | One execution against a **subject**. The ML spec's subjects are a checkpoint, a ModelVersion or a serving revision; this adds **a release** (the app's code at a digest) and **a Change variant** (a prompt). |
| EvalSample      | Per item: input, expected, output, scores, judge rationale, tokens, latency, cost, and the **trace id** of the run that produced it, so a failing sample opens as a transcript.                               |
| EvalResult      | One aggregate metric per task (and slice), with an interval and n.                                                                                                                                            |
| Baseline, Gate  | The comparison anchor (what prod runs, or a pinned run) and the declared promotion rule ([section 8](#8-quality-in-release-health-and-promotion-gates)).                                                      |
| OnlineEval      | The scores of 6.1 and 6.2, per release and per Change arm.                                                                                                                                                    |
| HumanEval       | The annotation queue's decisions.                                                                                                                                                                             |

Entities live in control-plane Postgres; samples, which are small for LLM apps,
in a tenant ClickHouse table (the ML spec keeps large samples in object storage;
both shapes can coexist). Offline runs execute in the customer's code or CI, the
way Braintrust's `Eval()` and Langfuse's experiment runner do: our Python and TS
helpers run a task function over a dataset, emit a trace per item, and record the
EvalRun. `forge evals run` and `forge evals record` are the CLI, matching
`forge ml eval record` in ml-workloads. `forge evals check --suite <s> --baseline
env:prod` exits 0 (pass), 1 (regressed) or 2 (cannot prove: too few items), the
same contract as `forge obs check`, so CI gates without a vendor action. Sharing
the model is [decision 10](#15-owner-decisions-decided-2026-11-26), settled for
one model; the ML track's own decisions of the same day (forge#2888) agree.

## 7. Prompts as Changes

A prompt version is a value; deciding which users get which value, sticky, with
a ramp, a readout and a kill switch, is exactly what a Change does. So **a prompt
is a Change**, not a second store:

```yaml
# changes.yaml
changes:
  support-system-prompt:
    type: json
    unit: user
    default: v3
    variants:
      v3:
        {
          prompt: prompts/support/v3.md,
          model: claude-sonnet-5,
          temperature: 0.2,
        }
      v4:
        {
          prompt: prompts/support/v4.md,
          model: claude-sonnet-5,
          temperature: 0.2,
        }
    surfaces: [server]
```

```python
p = changes.json("support-system-prompt", default=FALLBACK, ctx=user_ctx)  # local, no network
with genai.chat(model=p["model"], prompt=p) as span: ...
```

- **Linking.** The helpers stamp `forge.change.key`, `forge.change.variant` and
  `forge.change.run` on the chat span; Langfuse's `prompt.name`/`version` and
  OpenInference's `llm.prompt_template.version` normalise onto them. Every trace,
  score and cost figure can then be cut by prompt version.
- **Rollout and kill switch.** Preview to the team, experiment, ramp, everyone,
  stop: unchanged from [changes.md](changes.md), including the production
  approval and "stop never waits".
- **Readout.** Binary goals work today: thumbs-up rate (6.1), task-success
  events, conversion. Mean judge score, cost per run and latency per arm are
  continuous metrics, which `experimentstats` does not compute yet; that is
  ml-workloads decision 8 (b), and one implementation serves both tracks.
- **The size limit.** A variant value is capped at 8 KiB, and real system prompts
  exceed it. The variant therefore **names a prompt file the release carries**
  (`prompts/support/v4.md`, content-addressed in the release shape, the way
  Changes already refuses a variant the deployed release does not declare), and
  the 8 KiB holds parameters only.

**What this gives up, and the decision.** Langfuse's most loved prompt feature is
that a non-engineer edits a prompt in the UI and it is live without a deploy.
With prompts in the release, an edit is a commit. Three answers
([decision 7](#15-owner-decisions-decided-2026-11-26)):

| Option                                                                             | Edit without deploy              | Reviewed in git | Cost                                                                                                                                                                         |
| ---------------------------------------------------------------------------------- | -------------------------------- | --------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| (a) Prompt files in the release, Changes picks among them                          | No                               | Yes             | None beyond the above. Shipping a prompt is a deploy; with forge's build-once releases that is minutes.                                                                      |
| (b) A control-plane prompt store; a variant references a stored, revisioned prompt | Yes                              | No              | A second versioned store with its own audit, delivered in the snapshot (size budget), and a variant the release never declared, which changes.md's validation refuses today. |
| (c) (a), plus "edit in console" opens a PR                                         | Near: a person or Reliant merges | Yes             | The console's playground saves to a branch; Reliant opens the PR; the release train ships it.                                                                                |

Settled: **(a) now, (c) as the edit path**, and (b) only when a design
partner's prompt editors cannot work through a PR.

**A playground needs:** a console page that renders a prompt variant with
variables taken from a captured trace (needs content capture), runs it on one or
more models through the LLM gateway under the signed-in person's identity (the
org's wallet, or its own provider keys), compares variants side by side, runs a
variant over a dataset (that is an offline EvalRun with a Change-variant
subject), and saves as (c). Every dependency is an earlier slice, so the
playground is last.

## 8. Quality in release health and promotion gates

`forge obs check` already judges a release by comparing the window after a
promotion with the window before it. AI signals join as new findings, all
**warn-only** until a project opts in through `fail_on` (the same discipline as
a new lint rule: nobody's gate turns red on the release that ships them), with
thresholds as new optional fields on `forge.ReleaseHealth`:

| Finding          | Fires when                                                                                                     | Needs       |
| ---------------- | -------------------------------------------------------------------------------------------------------------- | ----------- |
| `ai_error_rate`  | The share of failed model and tool calls (excluding `canceled`) rose past a threshold, with a minimum of calls | Spans only  |
| `ai_latency`     | p95 of model calls, or time to first token, grew by a ratio                                                    | Spans only  |
| `ai_cost`        | Cost per run (per workflow) or per call grew by a ratio                                                        | Price table |
| `ai_tool_errors` | A tool's error rate rose (names the tool)                                                                      | Spans only  |
| `ai_score`       | A score's mean, or a boolean score's pass rate, dropped past a threshold with a minimum of scored samples      | Scores      |
| `ai_feedback`    | The thumbs-up rate dropped                                                                                     | Feedback    |

Adding codes to `release.HealthFindings` is additive (semver minor). The honest
limit: online scores are sampled, and a 15-minute window may hold too few scored
samples; then the verdict is inconclusive (exit 2, "not proven good"), never a
pass. The first three need no new data and are the fastest credible
differentiator: "your release made each agent run 40% more expensive" is a
sentence Langfuse cannot say about a deploy.

**Offline gates on promotion.** A release can carry EvalRuns recorded against
its digest (`forge evals record --release v1.4.0`). A declared gate
(`forge.EvalGate {suite, baseline = "env:prod", max_regression = 0.01}`) is shown
on the promotion plan from the first version and blocks the promote only when an
env opts in ([decision 9](#15-owner-decisions-decided-2026-11-26); ml-workloads decision 22
recommends the same display-first order).

## 9. Cost: a price table

Cost is the first question an agentic company asks, and today we answer only
where the gateway priced the call. The price data already exists in our stack:
the LLM gateway bills with LiteLLM's model cost map, and control-plane
`internal/llmlistprice` prices BYO-key calls at the same list rates. Settled:

- **One source.** LiteLLM's model price map (MIT), vendored into forge as
  versioned data (`pkg/llmprice`) and refreshed by a bot PR, so the dashboard's
  estimate for a call through a customer's own key equals what our gateway would
  have charged for it. Prices per usage type: input, output, cache read, cache
  write, reasoning.
- **Computed at query time** through a ClickHouse dictionary in each tenant, so a
  corrected price fixes history without a rewrite. Langfuse fixes the price at
  ingest; a self-hosted Langfuse gets new prices only on upgrade.
- **Reported cost wins.** A span's own `forge.ai.cost.usd`
  (or `llm.cost.total`, or Langfuse's `cost_details`) is used as is; the
  estimate is shown only where none was reported, labelled "estimated", with the
  price-table version.
- **Overrides.** Negotiated prices declared per project, shared with the ML
  track's customer-declared price sheet (ml-workloads decision 21) rather than a
  second format ([decision 8](#15-owner-decisions-decided-2026-11-26)).
- **No tokenizer.** A call with no usage reported shows no cost and says so.
  Langfuse retired its tokenizer on 2026-11-06 for the same reason.

When this lands, `builtin-ai-hub` drops "No price table yet" from its `why`.

## 10. Python and TypeScript helpers

Most agentic companies write Python or TypeScript; the only GenAI helper today is
Go. The helpers are thin layers over upstream OpenTelemetry, never a tracing
library of our own:

- **Spans:** `start_workflow`, `start_agent`, `chat`, `tool` as context managers
  and decorators, mirroring `pkg/observe/genai` name for name and emitting the
  same semver-1 attributes; a keep-AI-spans sampler; `user` and `session` set
  once for a trace.
- **Auto-instrumentation stays upstream.** The helpers turn on OpenInference or
  OpenLLMetry instrumentors when present; ingest normalisation makes either
  correct, so we never fork an instrumentor.
- **Content:** off unless the env allows it (the gateway enforces it anyway), plus
  a client-side mask hook.
- **Scores and feedback:** `score(...)`, `feedback(...)`.
- **Changes:** a Python port of the dependency-free evaluation core, held to the
  same golden vectors as Go and TypeScript (changes.md Appendix A), so prompts as
  Changes work in Python.
- **Evals:** a runner for 6.4.

Python ships as part of the one Python package the ML track builds first
(forge#2888), as a `genai` module beside its training callbacks; TypeScript as a
subpath export of `@reliantlabs/forge-web-runtime` for Node and edge runtimes,
so the browser barrel stays free of server dependencies (the reason changes.md
kept `changes.ts` pure). Packaging and names are [decision 11](#15-owner-decisions-decided-2026-11-26).

## 11. Joins nobody else has

Each of these exists for ordinary services; the GenAI lens only has to draw it.

| Join                 | Key                                                                        | What it answers                                                                                              |
| -------------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| Product events       | `user.id`, `org.id` (the same keys, `attrs.go:194-199`)                    | Did users who hit agent errors churn; which agent feature drives retention.                                  |
| Session replay       | the browser session that started the request                               | What the user did and saw before the bad answer.                                                             |
| Runtime code context | `forge.op`, `code.function.name` on the surrounding spans; release commits | Which code path called the model; "cost per run rose in the release that changed `ranker.go`".               |
| Releases             | `service.version`                                                          | Every AI metric per release, and the release verdict (section 8).                                            |
| Changes              | `forge.change.*`                                                           | Every AI metric per prompt variant (section 7).                                                              |
| APM                  | the same trace                                                             | The agent was slow because the vector database was slow, in one waterfall. Langfuse's SDK drops these spans. |

## 12. Alerts, the Inbox, and Reliant fixing it

Default AI alert rules join the default alerts every forge app gets: AI error
rate, a tool's error rate, cost per hour spike, judge score drop, feedback drop.
They land in the Issues inbox like any alert; the existing autopilot receiver
starts reliant's built-in `autopilot-oncall` workflow, which gets the trace
links, the transcript (only where a person with the content grant has allowed
that workflow to read content, [decision 4](#15-owner-decisions-decided-2026-11-26)), the
release and the Change involved. What it may do is already settled by
changes.md: **an unattended run may stop a Change** (stop never waits for
approval), and it may open a PR. So for a bad prompt arm the fix path is stop
the arm, add the failing trace to a dataset, and open a PR with a corrected
prompt that the offline gate then checks. Whether autopilot stops an arm without
asking is opt-in per change ([decision 14](#15-owner-decisions-decided-2026-11-26));
changes.md keeps platform-side automatic stop on a metric out of v1, and this
does not change that: the workflow is an agent calling `StopChange`.

## 13. Open source and self-host

Be precise about what is open, because Langfuse's pitch is "MIT, self-host it
all":

| Piece                                                                                                                               | Open                                          |
| ----------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------- |
| forge, `pkg/observe/genai`, the conventions table, the price data, the Python and TS helpers, local ClickStack under `forge env up` | Yes, Apache-2.0                               |
| `monitoring.endpoint`: send everything to a self-hosted OTLP backend instead of Reliant                                             | Yes, today (`obs_init.go:24-25`)              |
| Scores, evaluators, annotation queues, datasets, the readout, Inbox, the console                                                    | No: the control plane is a private repository |

So the honest positioning is: **open instrumentation, open data shapes, hosted
loop.** Everything a customer emits is standard OTel in standard ClickHouse
tables they can query with SQL and forward elsewhere at any time; leaving costs
an endpoint change, in both directions. What we do not offer is the loop on
their own servers. Options ([decision 12](#15-owner-decisions-decided-2026-11-26)): (a) that
positioning, with BYOC (the tenant ClickHouse in the customer's cloud, our control
plane) as an enterprise offer later, the shape LangSmith and Braintrust moved to;
(b) open-source a single-binary "forge obs server" with the loop; (c) say
nothing. Settled: (a). (b) is a second product and a support burden, and the
buyers we target at first (agentic startups) choose managed in the large
majority; Langfuse's own enterprise self-host is now bundled with a ClickHouse
commercial plan. One gap (a) still has to close is local: the AI hub's sql
panels are hosted-only, so `forge obs trace --transcript` and the CLI reads
must work against the local ClickStack.

## 14. Pricing angle

Our telemetry is billed flat per org by bytes: 10 GiB a month included, then
$0.10 per GiB, a $50 default monthly cap, with no seats and no per-span or
per-score unit (control-plane `internal/plansconfig/pricing.yaml`, pricing v2,
2026-11-06). Hosted traces keep 30 days by default. Langfuse bills units (traces

- observations + scores): free to 50k, then $29 a month for 100k and $8 per
  100k after. An illustration, **to be measured on Reliant's own spans before any
  number goes in a pitch**: if a metadata-only GenAI span is about 1 KB on the
  wire, 10 million spans a month fit in the free 10 GiB, where Langfuse charges
  about $820 for 10 million units; with content at about 10 KB a span, a million
  spans still fit. Judge calls bill at the gateway's passthrough price, as a
  customer's own LLM connection does at Langfuse.

The larger angle is the bundle: one bill and one product for APM, AI monitoring,
evals, flags and experiments, and hosting, instead of Langfuse plus Datadog plus
LaunchDarkly. Settled: keep AI data on the flat meter with no separate SKU
([decision 13](#15-owner-decisions-decided-2026-11-26)), because a per-span price is exactly
where we undercut everyone.

## 15. Owner decisions (decided 2026-11-26)

The owner accepted every recommendation this section proposed ("agreed with your
recs", 2026-11-26 06:01Z). They are recorded as the
[Settled (owner, 2026-11-26)](#settled-owner-2026-11-26) list, and the sections
above already describe the chosen option. The options that were weighed and not
chosen, for the record:

| #   | Decision                         | Chosen                                                                                                                                           | Not chosen                                                               | Unblocks                                                        |
| --- | -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------ | --------------------------------------------------------------- |
| 1   | Where conventions are normalised | At ingest, additive (canonical keys added, originals kept), from one forge table compiled to OTTL                                                | At query time in every reader; only our own convention                   | Slice 1, every later reader.                                    |
| 2   | Langfuse compatibility depth     | OTLP path alias, Basic → token at that path, `langfuse.*` attributes, plus a migration guide and dual-run                                        | Also emulating their REST API (prompts, scores, datasets); none          | Slice 1, the migration pitch.                                   |
| 3   | Content capture shape            | Off by default, per env in the release/env document, gateway-enforced, separate table and retention, tool I/O a second switch, org kill switch   | One org-wide switch; client-side only                                    | Slice 4, transcripts, datasets from traces, judges, playground. |
| 4   | Who reads content                | A new scope `obs:content:read`, per env, not implied by `obs:read`, every read audited, agents only with a person's grant                        | Anyone with `obs:read`                                                   | Slice 4, autopilot reading transcripts.                         |
| 5   | Where online evaluators run      | A control-plane evaluator worker for LLM judges; customer code for code evaluators; a Reliant workflow per batch later, for agentic trace review | Any one of the three alone                                               | Slice 8.                                                        |
| 6   | Where evaluators are declared    | Shape in the repo (`evals.yaml` or a `forge.yaml` section), live state (on, sample, budget) in the control plane, like Changes                   | Control plane only                                                       | Slice 8.                                                        |
| 7   | Prompt storage                   | Prompt files in the release, a Change picks among them; console edits that open a PR as the edit path                                            | A control-plane prompt store with live edits (only for a design partner) | Slice 9, the playground.                                        |
| 8   | Price table                      | LiteLLM's map vendored into forge, query-time dictionary, overrides shared with the ML price sheet                                               | Our own curated table; reported cost only                                | Slice 2, `ai_cost`.                                             |
| 9   | Quality gates on promotion       | New findings warn; offline eval gates displayed first, blocking per env on opt-in                                                                | Block from day one; display only                                         | Slices 7 and 10.                                                |
| 10  | One evals model with ML          | The ML spec's §3 model, subject widened to a release and a Change variant                                                                        | A separate LLM-app model                                                 | Slice 10.                                                       |
| 11  | SDK packaging                    | One Python package shared with forge#2888 (a `genai` module) and a TS subpath export                                                             | Separate AI packages                                                     | Slice 6.                                                        |
| 12  | Self-host story                  | Open instrumentation and data, hosted loop, BYOC later for enterprise                                                                            | Open-sourcing a single-binary obs server; saying nothing                 | Positioning, the compare skill.                                 |
| 13  | Pricing                          | AI data on the flat GiB meter, no seats or per-span unit, judge calls at gateway passthrough, after measuring bytes per span on the dogfood      | A separate AI SKU                                                        | Pitch numbers.                                                  |
| 14  | Autopilot stopping a prompt arm  | Opt-in per change                                                                                                                                | On by default for experiments; never, propose only                       | Slice 12.                                                       |

## 16. Alternatives considered

| Alternative                                                                        | Why not                                                                                                                                                                                                                                                                                                                                        |
| ---------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Embed or partner with Langfuse (forward AI spans to it, link out from our console) | Cheapest. But the loop's joins (release, Change arm, Inbox, autopilot) need the spans and scores where release health and the readout run; a link-out gives a prospect two products and two bills, and Langfuse is now ClickHouse's roadmap, not ours. Dual-run (3.2) keeps the friendly version: customers may keep Langfuse while trialling. |
| A standalone LLM observability product                                             | Competes where the market is crowded, trace for trace, against an MIT incumbent, and throws away the one asset nobody else has: the deploy, rollout and APM context.                                                                                                                                                                           |
| Do nothing beyond the AI hub                                                       | The hub is real, but without content, scores or a price table an agentic company's first evaluation ends in "where are my prompts?". Agentic companies are the customers most likely to need AI monitoring on day one.                                                                                                                         |
| A prompt store beside Changes                                                      | Two systems answering "who gets which version", one without stickiness, statistics or approval. Langfuse's A/B is exactly that weakness.                                                                                                                                                                                                       |
| Emulate the full Langfuse API                                                      | Section 3.2 (b).                                                                                                                                                                                                                                                                                                                               |
| Normalise at query time                                                            | Section 3.1.                                                                                                                                                                                                                                                                                                                                   |

## 17. Rollout plan

Each slice ships alone. Order follows dependencies and the pitch.

| Slice | What                                                                                                                                                                                                                   | Depends on   | Repos                         |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------ | ----------------------------- |
| 1     | Ingest normalisation: `pkg/observe/genai/conventions` with golden vectors, `transform/genai_normalize` plus one custom OTTL function, the Langfuse OTLP path alias, the migration guide, AI hub reading canonical keys | D1, D2       | forge, control-plane          |
| 2     | Price table: `pkg/llmprice`, tenant dictionary, estimated cost in the hub, the benefit's `why` updated                                                                                                                 | D8           | forge, control-plane          |
| 3     | GenAI lens on the trace view, sessions and users, `forge obs trace --transcript` (metadata only); the same component as Reliant's Trace tab                                                                            | —            | reliant, forge, control-plane |
| 4     | Content capture: declaration, gateway enforcement and redaction, `genai_content`, the scope, audited reads, transcripts in the lens                                                                                    | D3, D4       | forge, control-plane, reliant |
| 5     | Scores: the table, `ScoresService`, OTLP evaluation events, feedback as a product event, scores in the lens                                                                                                            | —            | forge, control-plane, reliant |
| 6     | Python and TS helpers, with forge#2888                                                                                                                                                                                 | D11          | forge                         |
| 7     | Release health: `ai_error_rate`, `ai_latency`, `ai_tool_errors`, then `ai_cost`, `ai_score`, `ai_feedback`, warn-only; default AI alerts                                                                               | 2, 5, D9     | forge, control-plane          |
| 8     | Online LLM-as-judge evaluators                                                                                                                                                                                         | 4, 5, D5, D6 | forge, control-plane          |
| 9     | Prompts as Changes: prompt files in the release shape, `forge.change.*` on spans, readout on feedback; continuous metrics with ml-workloads decision 8                                                                 | 5, D7        | forge, control-plane          |
| 10    | Datasets and offline evals: the shared model, `forge evals run/record/check`, eval gates on the promotion plan                                                                                                         | 4, 6, D10    | forge, control-plane          |
| 11    | Annotation queues; then the playground                                                                                                                                                                                 | 4, 5, 9, 10  | control-plane, reliant        |
| 12    | AI alerts to autopilot with transcript access; opt-in arm stop                                                                                                                                                         | 4, 7, D14    | reliant, control-plane        |

**Dogfood: Reliant's own agents.** Reliant already emits GenAI spans at every
level into tenant zero and has a per-chat trace API with no UI. It is the
design partner for most slices: the Trace tab is slice 3's component; the price
table is checked against what the gateway actually billed for the same calls
(the estimate must agree within a stated tolerance before it is shown to
customers); thumbs-up/down on agent replies is slice 5's feedback; a judge on
sampled Reliant workflow runs is slice 8 (reading content in-product through the
chat's ACL, never through capture on the staff copy); a Reliant prompt (title
generation, say) ramped as a Change with unit `user` is slice 9, once backend
delivery reaches Reliant's servers (changes.md slice 8). Content capture itself
(slice 4) is dogfooded on a customer-shaped app instead: a hosted example agent
in the reliant-labs org, in Python with OpenInference, onboarded with
`forge obs init` exactly as a prospect would be. That app is also the bytes per
span measurement for section 14.

**The fastest credible pitch.** Slices 1, 2, 3 and the spans-only part of 7 are
a demo of "point your existing instrumentation at us, see cost per agent and per
user, and get a release verdict and alerts Langfuse doesn't have", with no
content and no SDK. That opens the conversation, especially with Helicone's
migrating users. A Langfuse user will not switch without prompts on screen and
scores, so **the minimum to replace Langfuse is slices 1 to 5 plus 7**. The
demo that wins is slice 9 on top: a prompt shipped as a Change to 10% of users,
a readout on thumbs-up, and autopilot stopping a bad arm and opening the PR.

**Explicitly out of v1:** emulating Langfuse's REST API; importing historical
Langfuse data; a control-plane prompt store with live edits; named-entity PII
detection; running customer code evaluators on our side; multimodal content
(images, audio) beyond references; platform-side automatic stop on a metric;
self-hosting the loop.
