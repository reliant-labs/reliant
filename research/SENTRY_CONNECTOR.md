# Sentry connector, and credentials for daemons

Status: design, plus a draft manifest (`internal/integrations/catalog/sentry/manifest.yaml`,
actions and the `issue.created` trigger declaration). No receiver, poller or
schema change is built yet. Branch `feat/sentry-connector`, written 2026-10-07.

What the branch changes besides this document:

- **The manifest.** It loads under the catalog's strict decoder and
  validation.
- **Two registry mirrors** that every exposed action must appear in:
  `names.AllToolNames` and the regenerated
  `toolcatalog/catalog_generated.go`.
- **One test fixture** that enumerates every gated integration
  (`integration_access_test.go`).

The actions have **not** been run against a live Sentry organization. Before
merging, add an `httptest` fake per the checklist in
`INTEGRATIONS_V1_PROVIDERS.md` §7, and settle the open questions in §10.

The example workflows in §4.1 and §7 parse, and pass
`validation.StaticAnalysisWithOptions` with no findings.

This document answers two questions:

1. **Sentry connector.** How should workflows trigger on Sentry so that an
   agent reviews every error? (§1–§7)
2. **Credentials for daemons.** To give the machine that runs an agent's tools
   access to things like cluster logs, should we extend forge's secret model,
   have users put credentials in the daemon terminal, or use something else?
   (§8)

Companion docs: `research/INTEGRATIONS.md` §8 (Sentry is provider #5),
`research/TRIGGERS.md`, `research/INTEGRATIONS_V1_PROVIDERS.md` (the
Twilio/GitHub receivers this design reuses), `research/DELEGATED_CREDENTIAL.md`
and `research/TOOL_PLACEMENT.md`.

Facts marked **UNVERIFIED** were not confirmed against a fetched official page
or against code. Everything else was checked against docs.sentry.io (fetched
2026-10-07) or against the code paths cited.

---

## 0. Recommendations

**Sentry connector**

- **Trigger on `issue.created` (one event per new issue), not on
  `error.created` (one per event).** Sentry already groups repeated events
  into issues, and that grouping is the deduplication "review every error"
  needs. `error.created` requires a Business plan and would launch one run per
  occurrence.
- **Connect with an internal integration token.** It is org-wide, never
  expires, and is a plain bearer token. The draft manifest expresses it with
  existing manifest features and passes the catalog tests.
- **Ship triggers in phases:**

  | Phase | Mechanism | Platform work |
  |---|---|---|
  | **P0, today** | The generic webhook trigger already verifies Sentry's signature. Use it with the new Sentry actions. | None |
  | **P1** | A Go **poller** for `issue.created`. It gives a typed payload, `match` filters and per-issue dedupe, and the user does no setup in Sentry. | One file, like `gmail/poller.go` |
  | **P2** | A native **webhook** provider. It adds lower latency, `event_alert.triggered` (Sentry alert rules do the filtering and per-issue throttling) and `issue.unresolved` (regressions). | Two platform additions; see §9 |

- **Two missing capabilities matter most:**
  - **G1 blocks P2.** A connection cannot hold a second secret, and Sentry's
    webhook signing secret is not its API token.
  - **G3 matters in every phase.** Event-driven triggers have no concurrency
    or rate cap, so an error storm launches one run per issue.

  §9 lists every gap.

**Daemon credentials (§8)**

- **Don't extend forge's secret store to daemons.** It would hand long-lived
  secrets to every process on the machine, it serves only cloud workspaces,
  and it breaks the store's write-only invariant.
- **Treat "read logs" as a server-side integration action first.** For
  example, Cloud Logging `entries.list` covers GKE container logs without
  cluster credentials. Server-side actions are unattended-safe, scoped,
  audited, and keep the rule that connection secrets never reach a daemon.
- **Option B (creds typed into the daemon terminal) is acceptable now**, for
  attended work, with a dedicated read-only identity rather than a personal
  `gcloud auth login`.
- **Later, for native CLIs**, use a daemon credential helper on forge's
  `cloudcred` model: short-lived, scoped, minted at use.

---

## 1. What "review every error" means in Sentry's model

Sentry's integration-platform webhooks carry a resource header
(`Sentry-Hook-Resource`) and an `action`. These are the candidates:

| Resource / action | Granularity | Plan | Fit for "review every error" |
|---|---|---|---|
| `issue` / `created` | Once per **new issue** (a new fingerprint). Sentry sends it only for issue categories ERROR, OUTAGE and FEEDBACK. | All | **Primary.** Deduplicated by Sentry's grouping, so one investigation per distinct problem. |
| `issue` / `unresolved` | The issue came back: `substatus` is `regressed` (resolved, then seen again) or `escalating` (archived, then spiking). | All | Strong follow-up: "the fix didn't hold". P2. |
| `event_alert` / `triggered` | An **alert rule** fired with action "Send a notification via <integration>". Sentry evaluates the conditions (new issue, frequency, users affected), the filters (environment, level, release, tags) and the per-issue action interval. | All. The integration needs "Alert Action" enabled, which in turn needs a webhook URL. | **Best filtering and storm control**, configured in Sentry by people who already know it. P2. |
| `error` / `created` | Every event. | Business/Enterprise only | **Avoid.** Noisy, and it costs a run per occurrence. |
| `installation`, `comment`, `metric_alert`, `seer`, … | Lifecycle and other | n/a | `installation.deleted` matters for connection health in P2. |

Verified wire facts (docs.sentry.io, integration-platform webhooks):

- **Headers:** `Content-Type: application/json`, `Request-ID` (a unique id per
  request), `Sentry-Hook-Resource`, `Sentry-Hook-Timestamp`,
  `Sentry-Hook-Signature`.
- **Signature:** `Sentry-Hook-Signature` is the **hex HMAC-SHA256 of the body,
  keyed with the integration's Client Secret**. It has no prefix and does not
  cover the timestamp header, so the timestamp gives no replay window and
  dedupe must carry that job.
  - Sentry's JavaScript sample HMACs `JSON.stringify(body)`.
  - We verify the raw bytes. **UNVERIFIED** that those two are always
    byte-identical; pin it with a fixture captured from a real delivery.
- **Body:** `{action, installation: {uuid}, data: {...}, actor: {type, id, name}}`.
  - `data.issue` uses the same serializer as the issue API: `id`, `shortId`,
    `title`, `culprit`, `level`, `status`, `substatus`, `priority`,
    `issueCategory`, `issueType`, `project{id,slug,name,platform}`,
    `metadata`, `count`, `userCount`, `firstSeen`, `lastSeen`,
    `isUnhandled`, `web_url`/`permalink`.
  - `event_alert` carries `data.event` (with `issue_id`, `issue_url`,
    `web_url`, `level`, `tags`, `release`, …) and `data.triggered_rule`.
- **Ack budget:** "Webhooks should respond within 1 second." That is tighter
  than GitHub's 10 s or Slack's 3 s.
- **Retries: UNVERIFIED.** I found no documented retry policy. Design as if a
  delivery is at-most-once; the P1 poller doubles as a reconcile backstop
  (§4.3).

---

## 2. Connection

### 2.1 Auth: internal integration token (draft manifest)

- Created at *Settings > Developer Settings > New Internal Integration*.
- It installs itself on the organization and issues an **org-wide token that
  does not expire**. You can have up to 20 tokens per integration, revocable
  by hand.
- It is sent as `Authorization: Bearer <token>`, which is
  `api_key {in: header, name: Authorization, prefix: "Bearer "}` in the
  manifest.
- A user auth token (Personal Tokens) works the same way.
- The same internal integration later gives P2 its webhook URL and its Client
  Secret, so a user who sets it up once has everything.

Scopes to grant:

- Organization: Read (for the probe)
- Project: Read
- Issue & Event: Read, plus Write if `issue.update` is used

Nothing admin.

### 2.2 Other auth methods considered

- **Standard OAuth2 + PKCE** (`https://sentry.io/oauth/authorize/`,
  `https://sentry.io/oauth/token/`, documented under docs.sentry.io/api/auth).
  - The token is scoped to the one organization the user picks. It expires
    after 30 days and comes with a refresh token.
  - It **is** expressible as an `oauth2` auth method:
    `RELIANT_OAUTH_SENTRY_CLIENT_ID/SECRET`, and the same probe.
  - It is the right "one-click connect" for P3.
  - It brings **no webhooks**: those come only from integration-platform apps.
    So it pairs with the poller, not with P2.
  - **UNVERIFIED:** where an operator registers the OAuth client, and whether
    such tokens can call every issue endpoint used here.
- **Public integration** (a marketplace app; the operator holds one Client
  Secret for all customers). Its "OAuth" is **not** RFC 6749:
  - Install is `https://sentry.io/sentry-apps/<slug>/external-install/`.
  - The redirect carries `code` and `installationId`.
  - The token exchange is a JSON POST to
    `/api/0/sentry-app-installations/{installationId}/authorizations/`.
    So the token URL depends on a callback parameter.
  - The response is camelCase `token`/`refreshToken`, and tokens expire every
    **8 hours**.
  - `OAuth2Auth` cannot express this (its `token_url` may interpolate only
    connection params). It would need a Go broker, in the shape of
    `delegated`.
  - Its advantage is one deployment-wide signing secret (GitHub-App-like
    routing by `installation.uuid`). It needs Sentry's publication review.
    Revisit only if marketplace distribution matters.

### 2.3 Base URL, region, self-hosted

- `base_url` is
  `https://{{ connection.params.region }}.sentry.io/api/0/organizations/{{ connection.params.organization }}`.
  - `region ∈ {us, de}`, default `us`. Sentry documents `us.sentry.io` and
    `de.sentry.io` as the region domains and recommends them for
    region-resident data, which issues and events are.
  - The org slug is a path parameter. Its pattern forbids `/`, `.` and `..`.
- **Self-hosted Sentry is not expressible (gap G5).** A curated manifest fixes
  the domain, which is the SSRF rule in `manifest.checkCatalogURL`. A
  self-hosted install has an arbitrary host, and the only arbitrary-host
  mechanism (`allow_any_public_host`) is reserved for the generic HTTP
  integration and forbids `base_url` and probes.
  - Interim: `http/request` with an `api_key` connection pinned by the user.
  - Real fix: a per-connection, user-declared origin, held to the same
    public-address and credential-pinning rules the HTTP integration already
    enforces at runtime.

### 2.4 Probe

The probe is `GET /api/0/organizations/<slug>/`. External id is
`string(response.id)` (the numeric org id, stable across slug renames) and
label is `response.name`.

**UNVERIFIED** that an internal-integration token may call it:

- The endpoint's own page asks only for `org:read`.
- But docs/api/auth lists "Retrieve an Organization" among endpoints that need
  a *user* token.
- If it refuses, probe `/projects/` and take the org id from a project.

Check this with a real token before P1.

---

## 3. Actions (in the draft manifest)

All actions are server-placed, so the token never leaves the worker
(`connections.ErrDaemonPlacement`).

| Action | Request | Notes |
|---|---|---|
| `project.list` | `GET /projects/` | For pickers and for finding a slug to `match`. |
| `issue.list` | `GET /issues/?query=&sort=&limit=&project=&environment=&statsPeriod=&shortIdLookup=1` | Uses Sentry search syntax (default `is:unresolved`); a short id as the query finds that issue. One page, ≤100 results (gap G6). |
| `issue.get` | `GET /issues/{id}/` | Counts, releases, assignee, tag summary. |
| `event.get` | `GET /issues/{id}/events/{latest\|oldest\|recommended\|<event id>}/?llmFormat=markdown` | Covers the prior research's `event.latest` (default `latest`) and also offers `recommended`, Sentry's pick of the most useful event. **`llmFormat=markdown` returns a `formatted` field** with the exception, frames, breadcrumbs, request and contexts already rendered for a model. Raw `entries` come back only when `formatted` is absent. |
| `issue.update` | `PUT /issues/{id}/` | Resolve / resolveInNextRelease / unresolve / ignore (± minutes), assign, set priority. `mutates: true`. |

Deferred:

- **`issue.comment`** was in the prior research's list. Sentry's notes endpoint
  (`GroupNotesEndpoint`) is `ApiPublishStatus.PRIVATE` in Sentry's source,
  meaning it is not part of the public API. Do not build on it.
- **Linking a Reliant PR back onto the issue.** Use the integration platform's
  external-issue API (`/sentry-app-installations/{uuid}/external-issues/`),
  which needs the installation uuid. P3, after P2 records it.

---

## 4. Trigger delivery

### 4.1 P0: works today, zero code (the generic webhook trigger)

The generic webhook trigger's HMAC options (`internal/triggers/triggerspec`,
`webhook.VerifyHMAC`) already match Sentry's scheme:

```yaml
triggers:
  - name: sentry-new-issue
    webhook:
      hmac:
        header: Sentry-Hook-Signature
        algorithm: sha256
        encoding: hex
    filter: >-
      trigger.payload.headers['Sentry-Hook-Resource'] == 'issue' &&
      trigger.payload.body.action == 'created'
    prompt: "Investigate Sentry issue {{ trigger.payload.body.data.issue.shortId }}"
```

At activation, paste the internal integration's **Client Secret** as the HMAC
secret. Then set the integration's Webhook URL to the trigger's
`/hooks/{trigger_id}` URL and subscribe it to "issue".

Limits:

- One Sentry internal integration has one webhook URL, so you get one trigger
  per integration.
- The payload is untyped (`trigger.payload.body…`), and there are no `match`
  attributes; filtering is CEL only.
- Dedupe falls back to a body hash per minute, because Sentry's `Request-ID`
  is not in `webhook.idempotencyHeaders` (which knows `Idempotency-Key`,
  `X-Request-Id`, …). **Gap G7:** add `Request-ID` to that list. It is a
  one-line fix, and generic webhooks from other senders that use the same
  header benefit too.

### 4.2 P1: the `issue.created` poller (recommended first build)

Why polling first, even though Sentry's rate-limit page says "polling the API
for updates is likely to quickly trigger rate limiting; we recommend webhooks":

- **No schema or UI change.** The plain `api_key` connection is enough. The
  trigger, cursor, baseline, dedupe and health plumbing already exist
  (`triggers.Poller`, `TriggerPoller`, Gmail as the precedent).
- **Nothing to set up in Sentry**, beyond the token the actions already need.
- **Per-issue idempotency for free.** The poll dedupe key is
  `<trigger id>:<item id>`; with item id `issue:<issue id>`, an issue can
  never fire a trigger twice. The first poll is a baseline, so enabling a
  trigger never replays the backlog.
- **Backpressure instead of loss.** A poller emits at most N items per poll
  and advances the cursor only past what it emitted, so a storm drains
  gradually rather than all at once.

Design:

- **Request.** Each poll calls the manifest's own `issue.list` request through
  `httpaction.Runner`, as Gmail does:
  - `query="firstSeen:>=<cursor - 2m>"` plus category terms (below)
  - `sort=new`, `limit=100`
  - plus whatever the trigger's `match` lets us push down: `project=` from
    `match.project`, and `level:` from `match.level`. This cuts results and
    API load.
- **Ingestion lag.** The 2-minute overlap absorbs Sentry's search-index lag.
  Dedupe makes the overlap free.
- **Categories.** Mirror the webhook's categories (error, outage, feedback) so
  the two mechanisms deliver the same set. Performance issues never get an
  `issue.created` webhook.
- **Ordering.** Reverse the page to oldest-first. Emit up to
  `MaxItemsPerPoll` (say 20) and set the cursor to the last emitted
  `firstSeen`.
- **Full page.** If the page is full (100 results and still inside the
  window), the poller cannot see the oldest items. It re-baselines to now and
  records `PollResult.Gap`, for example "more than 100 new issues since
  <cursor>; see Sentry". This is a storm, and the storm controls in §5
  apply.
- **Item contents:**
  - `ID = "issue:" + id`, `Type = "issue.created"`, `OccurredAt = firstSeen`
  - attributes as declared in the manifest
  - `Data = {"issue": <trimmed issue>}`
  - `Sender` = Sentry (kind integration, verified, id `sentry`). An issue has
    no human sender.
- **Interval.** `poll_interval: 1m` is the floor (`triggerspec.MinPollInterval`).
  The default is 5 minutes.
- **Rate-limit budget.** Limits are per *caller and endpoint* and cannot be
  dodged with more tokens. N triggers on one org poll the same endpoint, so
  1/min each is fine for a handful.
  - Later optimization: one poll per *connection* fanned out to that
    connection's triggers. This needs a per-connection poll schedule, which
    `triggers.Poller` does not have today.

### 4.3 P2: the native webhook (`/integrations/sentry/events`)

This follows the `ConnectionSigned` pattern built for Twilio
(`webhook/twilio.go`).

- **Routing.**
  - The user sets the internal integration's Webhook URL to
    `<PUBLIC_URL>/integrations/sentry/events?account=<org id>`. The trigger
    API shows this URL, with the account filled in.
  - `SignedAccount` returns the `account` query value. It is only a lookup
    key, like Twilio's `AccountSid`: a forged value finds candidate
    connections whose secrets cannot sign the forgery.
  - `installation.uuid` cannot be used, because a token cannot discover its
    own installation, so no connection can record it.
- **Verification.** `VerifyWith` computes the hex HMAC-SHA256 of the raw body
  under the connection's **signing secret** and compares in constant time.
  Usually there is a single candidate. That matters for the 1-second budget,
  since each candidate is one vault open.
- **Event mapping.** The webhook and the poller produce the same
  `DeliveryID`/item id, so a trigger fed by both fires once per issue. That
  lets the poller run as a reconcile backstop for deliveries the 1 s timeout
  drops.

  | Resource / action | Event type | DeliveryID (dedupe) |
  |---|---|---|
  | `issue` / `created` | `issue.created` | `issue:<id>` |
  | `issue` / `unresolved` | `issue.unresolved` (attribute `substatus`) | `issue:<id>:unresolved:<Request-ID>`, so each regression is distinct |
  | `event_alert` / `triggered` | `event_alert.triggered` | `alert:<triggered_rule>:<event_id>` |
  | `installation` / `deleted` | none; mark the connection as needing attention | n/a |

- **Two missing capabilities (§9):**
  - **G1, a second connection secret.** Twilio works because its Auth Token
    both authenticates the API and signs webhooks. Sentry's API token and
    Client Secret are different values. Proposal:
    - Add `ConnectionSpec.webhook_secret {label, description}`. The connect
      form shows a second sealed field, stored as its own vault blob with
      its own AAD.
    - Add `ConnectionSecrets.WebhookSecret(ctx, user, conn)`, opened only by
      the events receiver and never visible to actions.
    - Rejected: per-trigger `webhook_hmac_secret`, which exists for generic
      webhooks. The user would paste the same secret on every trigger, and
      the app-level route must verify *before* it knows which triggers
      match. Also rejected: squeezing both values into `basic`, which would
      send the Client Secret to the API in a Basic header.
  - **G2, a per-connection user-configured URL.** `UserConfigured` today
    shows one URL to everyone (Twilio). Sentry needs the `?account=` hint in
    the displayed URL.
- **Manifest additions for P2** (append to `triggers:` when the provider
  lands, not before; see the note below):

  ```yaml
  - id: issue.unresolved
    display_name: Issue came back
    summary: A resolved issue regressed, or an archived one started escalating.
    events: [issue.unresolved]
    attributes: [project, project_id, level, priority, issue_category, platform, unhandled, substatus]
    data: {issue: …same as issue.created…}
  - id: event_alert.triggered
    display_name: Alert rule fired
    summary: A Sentry alert rule whose action notifies Reliant fired.
    events: [event_alert.triggered]
    attributes: [rule, project_id, level, environment, release, issue_id]
    data: {event: {issue_id, title, level, environment, release, tags, web_url, …}, triggered_rule}
  ```

  `environment` comes from the event's `environment` tag. Sentry's published
  sample shows no top-level field, so the provider reads `tags`. `release` is
  the event's `release`, which is null when the SDK sets none.

  Why only `issue.created` is in the draft manifest now: once a Sentry source
  is registered, `HasInboundSource("sentry")` is true and a trigger is
  accepted for **any** declared event type. If the manifest declared
  `event_alert.triggered` while only the poller existed, that trigger would be
  accepted and then silently never fire.

### 4.4 P3

- One-click OAuth2 connect (§2.2).
- External-issue links back onto Sentry issues (§3).
- Self-hosted origins (G5).
- `seer` webhooks, if Sentry's own autofix should hand off to Reliant.

---

## 5. Dedupe, idempotency, storms and filters

**Three levels of "once":**

| Level | Guarantee | Mechanism |
|---|---|---|
| Event | A Sentry issue fires a trigger at most once | Dedupe key `<trigger>:issue:<id>`, the same from webhook and poller (§4) |
| Run | One run per event | Already true: an event's fire workflow id is the event's (`TriggerEventFireWorkflow`) |
| Work | No second PR for the same issue, even across triggers or workflows | The workflow's job. Triage checks `issue.get` `assignedTo` / `status`, and on taking the issue calls `issue.update assigned_to: <bot user/team>` (P3: the external-issue link). A fix workflow resolves with `resolvedInNextRelease` once its PR merges, so a later event registers as a regression. |

**Storms.** A bad deploy can mint hundreds of new issues in minutes. Defenses,
cheapest first:

1. Sentry's grouping. One fingerprint is one issue.
2. `match` and `filter`: `issue_category: error`, `project`, `level` in
   (error, fatal), `unhandled: "true"`.
3. Sentry alert rules (P2, `event_alert.triggered`). Conditions such as "new
   issue AND seen by > N users" plus the per-issue **action interval** are
   Sentry-side throttles that users already understand.
4. **Reliant trigger limits. This is gap G3: I found no concurrency or rate
   cap for event-driven triggers.** `overlap` applies to schedules only
   (`Schedule.SkipOnOverlap`). Proposal:
   - Add `limits: {max_in_flight: 3, max_per_hour: 20}` on any trigger kind.
   - `EventFirer.Fire` checks the limits before launching. Overflow settles
     the event as `skipped` with detail "rate limited: 3 runs in flight",
     using the outcome that already exists.
   - Trigger health shows the count, and the redriver or a manual fire can
     replay skipped events later.
   - For a poller, prefer backpressure: hold the cursor.
5. A digest pattern for very noisy orgs:
   - A `schedule` trigger every 15 minutes runs one agent over
     `sentry/issue.list query: "is:unresolved firstSeen:-15m"`.
   - That is one run per window instead of one per issue. It complements
     per-issue triggers rather than replacing them.

**Filters**, mapped to where they apply:

| Want | How |
|---|---|
| One project | `match: {project: backend}` (pushed into the poll query in P1) |
| Errors only, not perf/feedback | `match: {issue_category: error}` |
| Severity | `filter: "trigger.payload.attributes.level in ['error','fatal']"` |
| Unhandled only | `match: {unhandled: "true"}` |
| Title text | `filter: "!trigger.payload.data.issue.title.contains('ChunkLoadError')"` |
| **Environment / release** | Not issue properties (an issue spans environments and releases). Use an alert rule filter (P2 `event_alert.triggered` carries `environment`/`release` attributes), or check in the workflow with `sentry/event.get environment: production`. |

---

## 6. Security

- **Payloads are attacker-controllable.** Anyone who can make the application
  fail chooses the error message, title, culprit, request URL, headers and
  breadcrumbs, and that text reaches an agent that may hold write tools.
  - The fire path already seeds the event as labelled, untrusted data
    (`seedWithEvent`). The triage step should still be read-only: Sentry
    actions plus read-only file tools.
  - The fix step should run in a worktree and open a **draft** PR for human
    review.
  - The manifest's `mutates` flag is recorded but not yet enforced for
    triggered runs (proto comment on `ActionSpec.mutates`). That enforcement
    is what makes "unattended agent with write tools" safe; track it as a
    dependency of auto-fix.
- **PII.** Events can carry user emails, IPs and request bodies.
  - `event.get`'s select drops the raw `user` object, but `formatted` may
    still render it.
  - Recommend Sentry's server-side data scrubbing, and say so in the
    integration docs.
- **Token scope.** Grant the internal integration the scopes in §2.1 only.
  Its token is org-wide, so prefer a dedicated integration per Reliant
  connection.

---

## 7. Composition: the Sentry flow, and chaining with `workflow_event`

### 7.1 Recommended shape: one workflow, branch inside it

Typed node outputs exist only *within* a workflow (`nodes.<id>…`), so the
triage → fix → notify pipeline belongs in one workflow:

```yaml
name: sentry-triage
description: Investigate each new Sentry error and propose a fix
entry: [issue]
triggers:
  - name: new-sentry-error
    integration:
      integration: sentry
      events: [issue.created]
      poll_interval: 1m
      match: {project: backend, issue_category: error}
    filter: "trigger.payload.attributes.level in ['error', 'fatal']"
    prompt: "Sentry issue {{ trigger.payload.data.issue.shortId }}: {{ trigger.payload.data.issue.title }}"
nodes:
  - id: issue
    type: action
    uses: sentry/issue.get@1
    with: {issue_id: "{{ trigger.payload.data.issue.id }}"}
  - id: event
    type: action
    uses: sentry/event.get@1
    with: {issue_id: "{{ trigger.payload.data.issue.id }}", event_id: recommended}
  - id: triage                     # read-only tools; answers through a response tool
    type: workflow
    ref: builtin://structured-agent
    args:
      system_prompt: Find the root cause in this repository. Do not edit files.
      response_schema:
        type: object
        required: [verdict, summary]
        properties:
          verdict: {type: string, enum: [fix, needs_human, noise]}
          summary: {type: string}
          suspected_files: {type: array, items: {type: string}}
  - id: fix                        # worktree, draft PR
    type: workflow
    ref: builtin://agent
  - id: notify
    type: action
    uses: slack/message.post@1
    with:
      channel: C0123ABCD
      text: "{{ nodes.issue.shortId }}: {{ nodes.triage.response.summary }}"
edges:
  - {from: issue, default: event}
  - {from: event, default: triage}
  - from: triage
    cases:
      - {to: fix, condition: "nodes.triage.response.verdict == 'fix'", label: fix}
    default: notify
  - {from: fix, default: notify}
```

### 7.2 Where `workflow_event` fits: cross-cutting watchers

The `workflow_event` trigger kind already ships:

- **Source:** `{workflows, outcomes: [finished|failed|blocked]}`
- **Payload:** `run_id`, `chat_id`, `workflow_name`, `outcome`, `summary`,
  `error`, `blocked_on`, `prompt`, …
- **Scope:** root runs only, same owner only.
- **Loop guards:** lineage plus `MaxChainDepth` 5.
- **Dispatch:** exactly-once per (trigger, event).

It fits watchers that need no structured data from the watched run:

```yaml
name: sentry-triage-watchdog
entry: [alert]
triggers:
  - name: triage-stuck
    workflow_event:
      workflows: [sentry-triage]
      outcomes: [failed, blocked]
nodes:
  - id: alert
    type: action
    uses: slack/message.post@1
    with:
      channel: C0123ABCD
      text: "sentry-triage {{ trigger.payload.outcome }}: {{ trigger.payload.error }}{{ trigger.payload.blocked_on }} (chat {{ trigger.payload.chat_id }})"
```

`blocked` is the useful one when the fix step waits on an approval at 3am:
the approval request reaches Slack.

It is also possible to split the pipeline, with `sentry-triage` finishing and
then a `fix-and-pr` workflow triggered by `workflow_event outcomes: [finished]`.
**Today that is weaker than §7.1, for the three gaps already identified in
`workflow_event`:**

| Gap | Effect on the Sentry flow | Needed? |
|---|---|---|
| (1) The terminal dedupe key is per Temporal run, so every follow-up turn of an interactive chat emits its own `finished` | Someone opens a triage chat to ask a question, and each reply re-fires `fix-and-pr`. | **Yes, if split.** Fix: key outcomes per *root run* (chat + workflow), or expose a turn index so filters can say "first completion only". |
| (2) The payload doesn't say how the run was launched | A filter cannot say "only automated triage runs, never human turns", which is also the cheap mitigation for (1). | **Yes, recommended regardless.** Add `launched_by: {kind: chat\|schedule\|webhook\|integration\|workflow_event\|manual, trigger_id, trigger_name, event_id}`. The launch event kind is already recorded (`core.TriggerEventKind`), so this is projection, not new state. |
| (3) No structured outputs, only summary/error text | `fix-and-pr` cannot branch on a triage verdict, and must re-derive the issue id from `prompt` text. | Only if split. Add `outputs` (the root workflow's declared `outputs:` values, size-capped like `summary`) so `filter: "trigger.payload.outputs.verdict == 'fix'"` works. The issue id would ride along as `outputs.issue_id`. |

So for Sentry:

- Build §7.1 now, and use `workflow_event` only for failed/blocked watchers.
- Add `launched_by` (gap 2) soon. It is cheap and guards the watcher against
  noise from human follow-up turns.
- Gaps (1) and (3) only become needed if teams want independently owned
  triage and fix workflows.

---

## 8. Credentials for daemons: forge secrets, terminal, or existing plumbing

### 8.1 The need, restated

"Give the daemon cluster access to read logs" is a means. The end is: **let
the agent investigating an error read the production signals around it**
(application logs, pod state, traces).

Constraints from the architecture:

- **Who can hold a secret.** The api-server, worker and gateway have no
  filesystem access; only the daemon does. The daemon may be remote and
  multi-tenant.
- **Unattended runs.** A Sentry-triggered run at 3am has no human to type or
  refresh anything. It needs a daemon that can be woken (`automationcred`'s
  `daemon:resume` token) and credentials that are still valid.
- **Blast radius.** Anything on a daemon's disk or in its environment is
  readable by every chat, workflow and tool process on it.
  - `daemonpolicy.ChildEnv` strips the environment only for *confined*
    (third-party connector) callers.
  - First-party agents inherit everything. A prompt-injected Sentry payload
    (§6) runs as first-party.
- **Today's invariant.** Connection secrets never reach a daemon.
  `connections.Resolver` refuses every placement but server
  (`ErrDaemonPlacement`): "so a token cannot leave the server".

### 8.2 The options

**(A) Extend forge's secret-provider model to daemons.**

What forge's model is:

- A workload *declares* secret references (`sensitive` config fields, or
  `EnvVar.secret_ref`).
- Each env *binds* a provider: `FileSecrets` for dev, `ExternalSecrets` or
  `HostedSecrets` for prod.
- Forge injects values into the workloads that declared them.
- `HostedSecrets` for a persistent env is **write-only**: "no forge process
  ever reads a value back". Values materialize in-cluster. For a local env,
  `forge env up` pulls them into memory.

"For daemons" would mean treating the workspace pod as a workload that
declares, say, `KUBECONFIG_DATA`, with the control plane materializing it
into the pod, as the workspace reconciler already does with `GIT_TOKEN`.

- **Pro:** declarative and reviewable (the forge philosophy), reuses an
  existing store and the `forge secret set` UX, and keeps values out of git.
- **Con:**
  1. It distributes the **long-lived root secret** to every process on the
     daemon. Even the best variant, an `env up`-style pull into memory,
     still lands in every tool subprocess's environment.
  2. It serves only cloud workspace daemons. Local and Mac daemons are not
     reconciled.
  3. Rotation means restarting the pod.
  4. There is no per-run or per-workflow scope, and no record of use.
  5. It conflates "secrets my deployed app needs" with "secrets my agent's
     machine holds". A project's prod database password must not become
     reachable from an agent sandbox because it sits in the same store.
  6. It needs the hosted store to become *readable* by daemons, which
     inverts its write-only invariant.

  The `GIT_TOKEN` precedent (written to `~/.git-credentials` at daemon start,
  `daemonruntime.setupGitCredentials`) shows the cost: a static token on
  disk, shared by every chat.

**(B) The user creates or downloads credentials in the daemon terminal**
(`gcloud auth login`, `gcloud container clusters get-credentials`, a pasted
kubeconfig).

- **Pro:** zero platform work, works today, and uses the vendor's own flows
  and refresh. On a local daemon it is already the status quo: the user's
  kubeconfig is just there.
- **Con:**
  1. It is interactive. It needs a human once per machine, and again
     whenever reauth policy expires the session. Unattended runs fail
     silently when it lapses.
  2. A personal `gcloud auth login` grants `cloud-platform`, everything the
     user can do, to every agent on that machine. That is the widest
     possible blast radius for a "read logs" need.
  3. It persists on disk across chats and workflows. **UNVERIFIED:** whether
     a cloud workspace's `$HOME` survives pod replacement.
  4. Reliant cannot see it, audit it, or revoke it.
  5. It is not reproducible for a teammate.

**(C) Existing Reliant plumbing.** Three pieces apply:

- **Connections plus server-placed integration actions** (what the Sentry
  connector uses).
  - The secret lives in the server vault, sealed per user. The worker applies
    it to one outbound request; the agent sees only the result.
  - Unattended-safe (the worker needs no daemon), scoped per connection,
    audited per call, revocable in one place.
  - For logs, this means a curated **`gcp_logging/entries.list`** (Cloud
    Logging `entries:list` with a filter such as
    `resource.type="k8s_container" AND labels.k8s-pod/app="api" AND severity>=ERROR`).
    GKE ships container stdout/stderr to Cloud Logging by default (verify per
    cluster), so **"read cluster logs" needs no cluster credential at all**.
    The same applies to Datadog, Loki and CloudWatch APIs as demand appears.
    Static-key backends (Datadog, Loki, Better Stack) already work today
    through `http/request` with an `api_key` connection.
  - Auth options:
    - Google OAuth with a read-only logging scope, on the deployment's
      existing Google client. **UNVERIFIED:** Google's sensitivity
      classification for `logging.read`.
    - Better, later: keyless. The user grants a Reliant-managed principal
      (Workload Identity Federation) `roles/logging.viewer` on their project,
      so no secret is stored anywhere.
  - The agent also already gets most of what it needs from Sentry itself:
    `event.get`'s `formatted` includes breadcrumbs, request and tags.
- **forge's credential-helper protocol** (`forge/pkg/cloudcred`, implemented
  by reliant's `internal/forgecred`).
  - The daemon exchanges its own session at the server for a token holding
    only the requested scopes, valid for at most an hour, minted at the
    moment of use. It never deposits the session.
  - Its package doc argues this case directly, under "WHY A HELPER AND NOT A
    DEPOSIT".
  - This is the right model for the cases where native CLIs are genuinely
    needed (`kubectl describe`, `kubectl get events`). A kubeconfig `exec`
    credential plugin pointing at `reliant auth cloud-credential
    --connection <id>` would mint a 1-hour, read-only GCP access token from
    a connection: IAM `generateAccessToken` on a service account the user
    granted Reliant impersonation rights to.
  - It is a deliberate, narrow exception to "connection secrets never reach a
    daemon": only connections marked daemon-exportable, only role templates
    that are read-only, only short-lived derived tokens (never the root
    secret), each mint audited, with a policy hook (for example, attended
    runs only, or named workflows only).
- **`automationcred`** (daemon-bound `daemon:resume` token). Any daemon-side
  option needs it so an unattended run can wake the daemon at all.

### 8.3 Comparison

| | A: forge secrets → daemon | B: terminal creds | C1: server-side action | C2: daemon cred helper |
|---|---|---|---|---|
| Works unattended at 3am | Yes, until rotated | Only while the session is valid | **Yes** | Yes (daemon session + `daemon:resume`) |
| Credential lifetime on the daemon | Long-lived | Long-lived refresh token | **None on the daemon** | ≤ 1 h, derived |
| Blast radius | Every process, every chat | Every process; often the user's full cloud access | **One call's result** | Read-only role, per mint |
| Local + cloud daemons | Cloud only | Both | **Both (no daemon needed)** | Both |
| Audit / revoke in Reliant | Partial | No | **Yes** | Yes |
| Native CLIs (`kubectl`) | Yes | Yes | No | **Yes** |
| Build cost | Medium, plus an invariant break | **None** | One manifest (+ a Google scope) | Medium (helper, mint API, policy) |

### 8.4 Recommendation and phasing

1. **Now:**
   - Ship the Sentry connector's read actions (this branch). They cover the
     error itself server-side.
   - If an agent must also read cluster logs today, use **B, deliberately**.
     Create a *dedicated read-only identity* (a GCP service account with
     `roles/logging.viewer`, and `roles/container.viewer` only if
     `kubectl get` is wanted; or a k8s ServiceAccount bound to a ClusterRole
     with `get` on `pods` and `pods/log`), and put that on the daemon, never a
     personal `gcloud auth login`.
   - Accept that this is attended-friendly and unattended-fragile. Write it
     down as a user-facing recipe, not a product feature.
2. **Next:** "read logs" as curated server-side integrations, `gcp_logging`
   first since GKE container logs land there (C1). This is the durable answer
   for unattended automation and keeps the daemon credential-free.
3. **Later, on demand:** the daemon credential helper (C2) for native-CLI
   needs. Fold `GIT_TOKEN` into it too (a git credential helper backed by
   `gitcredentialclient`), retiring the static `~/.git-credentials` deposit.
4. **Don't build** A as "daemons read the forge secret store". Borrow forge's
   *shapes* instead: declare references and bind per environment for
   configuration, and the `cloudcred` helper protocol for delivery.

---

## 9. Gaps and missing capabilities

| # | Gap | Blocks | Proposed fix |
|---|---|---|---|
| G1 | A connection holds one secret, and the API credential is always it; there is no webhook-signing secret alongside it | P2 webhook verification | `ConnectionSpec.webhook_secret` plus a receiver-only `ConnectionSecrets.WebhookSecret` (§4.3) |
| G2 | `UserConfigured` shows one webhook URL to everyone | P2 routing | Show the per-connection URL with `?account=<external id>` |
| G3 | No concurrency or rate cap on event-driven triggers | Storm safety for any integration trigger | `limits {max_in_flight, max_per_hour}`; overflow settles as `skipped` (§5) |
| G4 | `ActionSpec.mutates` is not enforced for triggered runs | Safe unattended auto-fix | The planned enforcement phase |
| G5 | A curated manifest cannot target a self-hosted Sentry origin | Self-hosted users | Per-connection user-declared origin with runtime host pinning (§2.3) |
| G6 | `link_header` pagination follows any `rel="next"`; Sentry always sends one, with `results="false"` on the last page | Multi-page list actions | Skip a next link carrying `results="false"` |
| G7 | `Request-ID` is not an idempotency header | P0 dedupe quality | Add it to `webhook.idempotencyHeaders` |
| G8 | `OAuth2Auth` cannot express Sentry's public-integration install exchange (the token URL depends on `installationId` from the callback; JSON body; camelCase tokens) | Marketplace distribution | A Go broker, only if pursued (§2.2) |
| G9 | `query_expr` must yield scalars, so a query key cannot repeat (`project=a&project=b`) | Multi-project `issue.list` | Allow list values to repeat the key, as `body_format: form` already does |
| G10 | `workflow_event` gaps (1)–(3) | Split triage/fix workflows | §7.2 |

## 10. Open questions (check before P1/P2)

- Can an internal-integration token call `GET /organizations/{slug}/`, the
  probe (§2.4)?
- Does Sentry retry failed webhook deliveries, and is the signature over the
  exact raw bytes sent (§1)?
- Does `us.sentry.io` serve every endpoint used here for US organizations?
  (The documentation says region domains serve region-resident data, which
  issues, events and projects are.)
- Search syntax for the poller: `firstSeen:>=<ISO timestamp>` and the
  issue-category term (`issue.category:error`). Pin both with a recorded
  fixture.

## 11. Sources

- https://docs.sentry.io/integrations/integration-platform/webhooks/ (headers,
  signature, 1-second response, resources)
- …/webhooks/issues/, …/webhooks/issue-alerts/, …/webhooks/errors/ (payloads;
  `error` needs Business/Enterprise; `issue.created` categories)
- …/integration-platform/internal-integration/ (token never expires, 20 per
  integration)
- …/integration-platform/public-integration/ (installation-scoped exchange,
  8-hour tokens)
- https://docs.sentry.io/api/auth/ (bearer tokens, OAuth2 + PKCE, 30-day
  tokens)
- https://docs.sentry.io/api/ (region domains),
  https://docs.sentry.io/api/ratelimits/ (per caller+endpoint; prefer
  webhooks)
- https://docs.sentry.io/api/permissions/ (scopes)
- API pages: retrieve-an-issue, list-an-organizations-issues,
  retrieve-an-issue-event (`llmFormat`), update-an-issue,
  retrieve-an-organization
- `getsentry/sentry` `src/sentry/issues/endpoints/group_notes.py` (notes
  endpoint is `PRIVATE`)
- Code: `internal/integrations/manifest/{manifest,auth,triggers}.go`,
  `internal/integrations/webhook/{contract,hooks,events,twilio,wiring}.go`,
  `internal/triggers/{poll,fire_event}.go`, `internal/connections/resolver.go`,
  `internal/daemonpolicy/env.go`, `internal/toolexec/daemonruntime/runtime.go`,
  `internal/forgecred`, `internal/automationcred`,
  `../forge/pkg/cloudcred/cloudcred.go`, forge `secrets` skill.
