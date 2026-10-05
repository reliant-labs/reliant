# Integrations v1: shared briefing for the build-out

Read this first. It records what is SETTLED (verified on 2026-10-04 against
reliant main `638a8d8f` and control-plane main, so do not re-derive it), what
each workstream owns, and the contracts between workstreams. The long-form
design is `research/INTEGRATIONS.md`; the trigger engine is
`research/TRIGGERS.md`. Where this file and those disagree, this file wins:
it carries the user's later decisions.

## 0. Goal

Get about five providers working end to end, so we know the framework holds
before widening to hundreds:

| # | Provider | Actions | Triggers |
|---|---|---|---|
| 1 | **HTTP / inbound webhook** (also covers Zapier both ways) | `http/request@1` (exists) | generic `webhook` |
| 2 | **Reliant workflows** | start a workflow (exists as `run`) | run `finished` / `failed` / `blocked` (approval or question pending) |
| 3 | **GitHub** | issue create/comment, PR comment/get | `issues`, `issue_comment`, `pull_request`, `push` (app-level webhook) |
| 4 | **Slack** | message post/reply | `message`, `app_mention`, `reaction_added` (Events API) |
| 5 | **Gmail** | send, list/get | new email (poll `history.list`; Pub/Sub later) |
| 6 | **Text** (Twilio SMS/WhatsApp) | send message | inbound message (Twilio webhook) |

CEL is the condition language everywhere: a trigger's `filter` is a CEL bool
over `trigger.payload`, and nodes branch on action outputs with CEL.

Later wishlist (do not build now): Sheets, Airtable, Notion, HubSpot,
Salesforce, Stripe, Shopify, Jira, Linear, Calendar, Typeform, Calendly.

## 1. Settled decisions (user, 2026-10-04)

- **One GitHub App, control-plane's.** Slug `reliant-labs`; client ids live in
  CP `deploy/kcl/{dev,prod}/config.k`. Permissions: contents, issues,
  pull_requests, actions, checks, statuses (write); administration (read).
  Events subscribed: issues, issue_comment, pull_request(+review), push,
  workflow_run, release, and others. **Never register another App.**
- **control-plane is the GitHub token authority.** CP's `git_credentials`
  already holds each user's GitHub App user token and refreshes it
  (`internal/gitcredential/gitcredential.go` `GetAccessToken`). Reliant asks CP
  for a user's current token at call time through a NEW internal-service RPC.
  It follows the `AccessTokenInternalService` pattern exactly: an
  internal-service JWT signed with `INTERNAL_SERVICE_SECRET`,
  `middleware.RequireInternalService` first in every handler, withheld from
  `proto/public-api.txt`, and user ids EXTERNAL (IdP subject). Reliant's
  existing client for that pattern is `internal/accesstokenclient`.
  - Consequence: reliant's own `RELIANT_GITHUB_APP_CLIENT_ID/SECRET` provider
    (`internal/connections/providers.go`, from #443) is for SELF-HOSTED only
    (no control plane). Hosted GitHub connections resolve through CP.
- **Apps per provider, not per connection.** We register ONE OAuth client per
  OAuth provider (Slack, Google), configured as deployment secrets keyed by
  integration id. API-key and basic-auth integrations need no app. External
  registrations (Slack app, Google OAuth client, Twilio) need the user's
  accounts, so build against `httptest` fakes and leave a clear "configure X"
  unavailable state.
- **Connections are per user.** Daemon-placed actions may not use connections.
- **Settings:** GitHub keeps its name in the UI. There is no Connections
  settings page until a second integration exists, which is now: Slack/Gmail
  make it real.
- **No down migrations.** Create migrations with
  `goose -dir internal/db/migrations/postgres create <name> sql`, and update
  `internal/db/postgres/schema.sql` plus `make sqlc`.
- **Process:** one PR per stream off main; merge once verified (tests shown
  failing first for new behaviour); `--body-file`; never `git stash`, never
  `git add -A`.

## 2. What exists (verified, do not re-derive)

**Integrations runtime** (reliant #438, merged):
- `internal/integrations/manifest`: YAML → `reliantv1.IntegrationManifest`
  (`proto/reliant/v1/integration.proto`). The loader REJECTS
  `connection.type` other than `"none"` (`manifest.go:154`) and any
  `triggers` (`manifest.go:146`). `optional_auth_kinds` accepts
  `api_key|basic`.
- `internal/integrations/catalog`: embedded manifests; only `http/manifest.yaml`.
- `internal/integrations/httpaction`: the declarative HTTP runner.
  `RunAuthenticated(ctx, manifest, action, params, CredentialSource, CallSite)`.
  `CredentialSource.Credential(ctx, CredentialRequest) (Credential, error)`,
  where `Credential` has `ConnectionID() / Apply(*http.Request) / Scrub(string)`.
- `internal/integrations/connauth`: adapts `connections.Resolver.ForCall`
  into a `CredentialSource`; pins the credential to the starting host; scrubs.
- `internal/integrations/tmpl`: CEL-backed templating for manifests.
- `internal/netguard`: the SSRF-guarded dialer. Every outbound integration call
  goes through it.
- Workflow `action` node (`handlers/action.go`) and the `http__request` tool.

**Connections and vault** (#440, #443, #444):
- Tables `connections`, `connection_secrets` (sealed), `oauth_flows`,
  `vault_keys`. `auth_kind IN ('oauth2','github_app_user','api_key','basic','none')`.
- `internal/connections`: `Resolver.ForCall` (owner from run id → connection
  → `Resolved` with `Apply`), `TokenSource` (OAuth refresh), `Registry` of
  `Provider`s. Providers are HAND-WRITTEN Go today (GitHub only), and
  `authenticator.go` has a closed header allow-list.

**Triggers** (`research/TRIGGERS.md`):
- Tables `triggers` (kind check: `schedule` only) and `trigger_events`, whose
  `UNIQUE (kind, dedupe_key)` gives exactly-once firing.
- `internal/triggers` (syncer, fire workflow, health); `internal/launch` is
  the ONE door that starts runs; `TriggerService` in `trigger.proto`.
- The CEL `trigger` root already exists: `runtime/trigger_info.go`
  `TriggerInfo{Kind, TriggerID, EventID, OccurredAt, Payload}` →
  `trigger.payload.*` in every node.
- The Automations UI is `web/src/components/Automations/`.
- `triggers.notify_on_complete` plus the inbox `RunFinished` kind already
  record run completion for the inbox; a workflow-event trigger source
  builds on that signal.

**Public ingress:** the reliant api-server serves `PUBLIC_URL` (env.k). The
OAuth callback `<PUBLIC_URL>/integrations/oauth/<provider>/callback` is
already served. Webhook receivers go under `<PUBLIC_URL>/integrations/...`
and `<PUBLIC_URL>/hooks/...` on the api-server.

## 3. Target architecture (the contracts between streams)

### 3.1 Manifest auth (stream A, the spec all providers code against)

`ConnectionSpec` declares auth as data, so adding a provider is a YAML file:

- `type: oauth2` with `authorize_url`, `token_url`, `scopes`, `pkce`, extra
  authorize params, and an identity `probe` (URL plus label/id JSON paths).
  The client id and secret are deployment config keyed by integration id
  (`RELIANT_OAUTH_<ID>_CLIENT_ID/_SECRET`), never in YAML or the DB.
- `type: api_key` with `in: header|query`, `name`, and `prefix`. This
  replaces the closed header allow-list; host pinning still applies.
- `type: basic`.
- `type: delegated`, where the token comes from an external authority (the
  GitHub-via-control-plane case). It names a broker id that Go registers.
- `connection_params`: per-connection, non-secret settings (Shopify shop,
  Jira site, Zendesk subdomain) that `base_url` may template.
- A Go escape hatch: an action with `executor: go:<name>` dispatches to a
  registered Go function with the same input/output contract. It is for things
  HTTP declarations cannot express (Gmail MIME, request signing).

### 3.2 Triggers (stream B)

- `triggers.kind` gains `webhook`, `integration` and `workflow_event`, with
  matching `trigger_events.kind` values.
- **Receivers** live on the api-server. They do three things only: verify
  (HMAC/bearer/provider signature), write a `trigger_events` row with a
  deterministic dedupe key, and start the fire workflow. They return 2xx
  quickly; launching stays on the worker.
- **`filter` is CEL** over `trigger.payload`, evaluated after the event row is
  written; a miss is recorded as outcome `skipped` (auditable). An event-type
  mismatch is filtered before writing the row (no noise).
- **Mechanisms:**
  - generic webhook: `POST /hooks/{trigger_id}/{token}`
  - app-level provider webhooks: `POST /integrations/{provider}/events` (GitHub,
    Slack). Route by installation, team or account to matching triggers.
  - poll: a Temporal schedule per trigger running a `PollTrigger` activity,
    with the cursor in `trigger_registrations`. The first poll is a baseline
    and does not fire on history.
- Payloads are untrusted data: size-capped, secret headers stripped, and never
  interpolated into a system prompt.

### 3.3 Workflow events (stream C)

A run reaching `finished`, `failed` or `blocked` emits an event that any of the
owner's `workflow_event` triggers can match. Filters are on the source
workflow name or id and the outcome, plus a CEL `filter`. The payload carries
run id, chat id, workflow name, outcome, and summary/error text. Guard against
loops: a trigger never fires on a run it launched (or one of that run's
descendants), and the launch chain depth is capped.

## 4. Workstreams and ownership (disjoint by directory)

| Stream | Owns | Must not touch |
|---|---|---|
| A: manifest auth spec + loader | `proto/reliant/v1/integration.proto` (ConnectionSpec, ActionSpec executor), `internal/integrations/manifest/`, `internal/connections/` (provider registry from manifests, authenticator) | triggers, web |
| B: trigger receivers + CEL filter | `proto/reliant/v1/trigger.proto` (new source arms), `internal/triggers/`, new `internal/integrations/webhook/`, trigger migrations | integration.proto, connections |
| C: workflow-event triggers | run-terminal event emission in `internal/workflow/runtime` / `internal/launch`, matcher | B's receiver code (consumes B's kinds) |
| D: GitHub via CP tokens | control-plane: new internal RPC in `internal/gitcredential` + handler + proto; reliant: client in `internal/controlplane`/`accesstokenclient` sibling, `delegated` broker registration | the manifest loader (consumes A) |
| E: browser QA | no source changes; a findings report | everything |

Providers (GitHub, Slack, Gmail, Twilio manifests) start once A and B land.

Wave 1 also includes two authoring streams (see §3a), which start once A and B
land:

| Stream | Owns |
|---|---|
| F: YAML + agent tools | `internal/workflow/yaml/`, workflow validation of `triggers:` (CEL filter against the payload schema, refs resolve), `create_workflow`/`edit_workflow` feedback, new `search_integrations` / `get_integration_schema` / `activate_trigger` tools, the trigger-row `workflow_trigger` activation path |
| G: builder UI + search | `IntegrationService.SearchCatalog` / `GetCatalogEntry` (server + proto), `web/src/components/workflow/**` (node picker, action config, trigger rail editor), `web/src/components/Automations/**` for activation |
| H: daemon-less runs | Optional `daemon_id` on triggers; placement-aware tool menu (a daemon-less run is offered only server/any tools, and nodes needing a daemon are refused at preflight/validation); a "no machine" mode for chats if the user approves it; preflight surfaced to authoring tools and UI |

## 3a. Authoring: YAML, agent tools, builder, search (user, 2026-10-05)

The user's three requirements, and the design that meets them:

1. **Everything works in workflow YAML.** Every new concept (action nodes on
   any integration, cron/webhook/integration/workflow-event triggers, CEL
   filters) round-trips through `.reliant/workflows/*.yaml` and the DB-stored
   workflow YAML with no loss.
2. **`create_workflow` / `edit_workflow` handle all of it.** The agent writes
   and edits YAML, so (1) gives the agent the capability. The agent also needs
   validation feedback for triggers and actions, plus a way to discover what
   exists (see 4).
3. **The UI builder handles all of it,** including integrations, with a search
   mechanism that scales to hundreds of integrations.

**Triggers move into the workflow definition, split into WHEN and AS WHOM.**
This supersedes `research/WORKFLOW_UI.md` §3.2's "not saved in the YAML".

```yaml
name: triage-new-issues
triggers:
  - name: new-issue                    # unique within the workflow
    integration:                       # or: schedule / webhook / workflow_event
      event: github/issue.opened@1
      params: { repo: reliant-labs/reliant }
    filter: "!trigger.payload.issue.labels.exists(l, l.name == 'wontfix')"   # CEL
    inputs:                            # CEL mapping trigger.payload -> workflow inputs
      issue_number: "{{ trigger.payload.issue.number }}"
  - name: nightly
    schedule: { cron: ["0 9 * * 1-5"], timezone: America/New_York }
nodes: ...
```

- **The definition carries the WHEN:** the source spec, the CEL `filter`, the
  `inputs` mapping. These are the same proto messages the trigger rows use
  (stream B makes them standalone and embeddable).
- **The trigger ROW is an activation (the AS WHOM / WHERE):** owner, project,
  daemon, connection id, enabled, plus `workflow_trigger: <name>`, which
  points at a declared trigger. Ad hoc rows with an inline source keep working,
  so the existing schedule automations are unchanged.
- Why the split holds: a builtin or shared workflow is read-only but users
  activate it differently (their daemon, their GitHub connection), which is
  WORKFLOW_UI §3.2's original reason. Declaring the WHEN in YAML gives the
  user's requirement without losing that.
- **Activation UX:** declaring a trigger does not fire anything. The builder
  rail and the Automations page show declared triggers as "Activate", which
  picks a connection and a daemon. The agent can do it too, via an
  `activate_trigger` tool. The CEL filter is validated at save time against the
  trigger type's payload schema (from the manifest), so the agent gets errors
  from `create_workflow` / `edit_workflow` like any other validation error.

**Discovery and search for hundreds of integrations:**
- One server-side index over the manifest catalog: integrations, actions and
  trigger types, each with display name, description, category, keywords, auth
  type, and whether the caller has a connection. The service is
  `IntegrationService.SearchCatalog(query, kind: action|trigger, category,
  connected_only, page)`, which returns lightweight entries. A separate
  `GetCatalogEntry(ref)` returns the full param/output/payload JSON schemas.
  Nothing ever ships the whole catalog to the client or into a prompt.
- **Agent:** one `search_integrations` tool (query → top N refs with one-line
  descriptions) and one `get_integration_schema` tool (ref → params/outputs or
  trigger payload schema). The agent searches, reads one schema, then writes
  YAML. `get_workflow_suggestions` / the node schema stay for core node types.
- **Builder:** a command-palette-style node picker ("Add step" and "Add
  trigger") backed by `SearchCatalog`: fuzzy search, category facets,
  connected-first ranking, recent and popular items. The action node's config
  panel and the trigger editor render forms from `GetCatalogEntry` schemas
  via the existing `ProtoFieldRenderer` / schema-form path. There are no
  per-integration React components.
- Ranking v1 is plain lexical search (name/keywords/description) plus connected
  and recent boosts, in memory; the catalog is embedded and small enough.
  Semantic search is a later improvement, not v1.

## 3b. As built after wave 0 (2026-10-05; code against THESE, not §3)

Merged: A #459 (manifest auth), B #461 (trigger receivers), C #460 (workflow
events), D CP#601 + #449 (GitHub tokens via control-plane). forge #467
(oauth2 `ScopeSeparator`) is in review; reliant must re-pin forge to use it.

**Manifest auth (A).** `connection.auth` is a list, most preferred first:
`delegated{broker}`, `oauth2{authorize_url, token_url, scopes,
scope_separator, pkce, authorize_params, revoke}`, `api_key{in, name,
prefix}`, `basic{username_param|username_label, password_label}`. Also
`connection_params` (one DNS label, fills only the leftmost host label) and
`probe{path, ok, external_id, label}`. OAuth clients come from env
`RELIANT_OAUTH_<ID>_CLIENT_ID/_SECRET` (`connections.OAuthClientEnv`).
Escape hatch: `ActionSpec.executor: go:<name>`, registered in
`httpaction.Executors()`. Brokers: `connauth.NewBrokers().Register(id, b)`.
`controlplane-github` is registered on hosted workers. Search fields:
`keywords` (manifest and action), `summary` (action),
`manifest.ActionSchemas(action)` → params/output JSON Schema.
- Comma scopes (Slack): `internal/connections/oauth.go` refuses them until
  reliant pins a forge containing #467. The Slack stream re-pins
  (`scripts/pin-forge.sh <sha>`) and lifts the refusal.

**Triggers (B).** Push providers implement `webhook.Provider{ID(),
Verify(ctx,*Request), Parse(ctx,*Request) (*Delivery, error)}` in
`internal/integrations/webhook` and are added in `webhook.RegistryFromEnv`,
each enabled by its own secret (e.g. `RELIANT_GITHUB_WEBHOOK_SECRET`).
- `Request` carries `PublicURL` (rebuilt from PUBLIC_URL, which is what Twilio
  signatures verify against), `Header`, raw `Body` (≤1 MiB) and `Form`.
- `Delivery{Respond *Response /*handshake, no event*/, Events []Event}`.
- `Event{Type "issues.opened", AccountKey, DeliveryID, OccurredAt,
  Attributes map[string]string, Data map[string]any}`.
- **`AccountKey` must EQUAL the connection's `external_account_id`**; that is
  how an event reaches only that connection owner's triggers.
- `webhook.VerifyHMAC(cfg, secret, body, sig)` is exported.
- Pollers implement `triggers.Poller.Poll(ctx, PollRequest{TriggerID,
  OwnerUserID, ConnectionID, Cursor, Config}) (*PollResult{Cursor, Items})`.
  An empty cursor is the baseline: return the position and no items.
- Proto: `WorkflowTrigger{name, description, filter, inputs, oneof
  source{schedule=20, webhook=21, integration=22, workflow_event=23}}` on
  `Workflow.triggers = 15`. `IntegrationSource{integration, events, match,
  poll_interval}`. `Trigger`/`TriggerDefinition` gained `filter`,
  `connection_id`, `workflow_trigger`, webhook fields. New RPC
  `RotateWebhookToken`.
- Filter = raw CEL over the `trigger` root (the same `TriggerInfo.CELValue()`
  nodes see). False → `skipped` row; an eval error → `failed` with a `has()`
  hint.

**Added by the Twilio stream (code against these too):**
- `HttpRequestSpec.body_format: form` — the body (static or `body_expr`)
  encodes as `application/x-www-form-urlencoded`; lists repeat their key,
  nulls are dropped, nested objects are refused. `PaginationSpec` style
  `next_url` with `next_url: <CEL>` follows a provider-supplied next-page URL
  verbatim (relative resolves against the current page; same host rules as a
  Link header). Followed Link-header URLs are now also requested verbatim.
- A probe may be a `url:` (catalog-fixed, templated) when the identity
  resource is not under `base_url`.
- `IdentityProbe.routes_events: true` declares that the probe's external id
  is what inbound events route on (connection-account routing, as Twilio's
  AccountSid). For such an integration `connections.Service.CreateAPIKey`
  runs the probe before saving, records the provider's `external_id` /
  `label`, and refuses a credential the provider rejects (InvalidArgument).
  Everything else is unchanged (no network call at create) — including
  GitHub, whose events are access-gated (#474) rather than account-routed.
- `webhook.ConnectionSigned` (`SignedAccount`, `VerifyWith(req, secret)`) +
  `EventsOptions.ConnectionSecrets` / `Inbound.WithConnectionSecrets`: for a
  provider whose deliveries are signed with a per-connection secret. The
  receiver verifies against each candidate connection's secret and routes
  only through the verified ones. `Delivery.Ack` is a reply sent after
  events are recorded (TwiML). `webhook.UserConfigured` /
  `Registry.UserConfiguredURL` mark providers whose webhook the user sets per
  resource; their integration triggers render `webhook_url` =
  `<PUBLIC_URL>/integrations/<id>/events`.

**Known gaps (owned by named wave-1 streams):**
- Pollers have no credential path: `Resolver.ForCall` needs a run id. The
  Gmail stream adds a by-connection credential resolution for pollers.
- GitHub routing key: user connections record a user id, while GitHub
  webhooks carry `installation.id`. The GitHub-triggers stream decides.
- Activating a workflow-declared trigger returns Unimplemented, and
  `WorkflowTrigger.inputs` is unused. Owned by stream F.

## 3c. Daemon-less runs (user, 2026-10-05)

Users must be able to create automations and workflows with NO paired daemon,
and possibly have ordinary chats with no daemon connected. The design
requirement is **proper tool-call prevention**: a run that has no daemon must
never be OFFERED a daemon-placed tool or node, so it never burns turns on
"no daemon connected" and trips the offline breaker.

Verified facts (`research/DAEMONLESS.md`, `research/TOOL_PLACEMENT.md`):
- The engine already runs daemon-less: the hermetic e2e harness uses
  `DaemonRouter: nil`.
- Every tool has a `Placement` (daemon/server/any). `fetch` and `websearch` are
  already `PlacementAny`.
- `runtime.RequiresDaemon(wf, cfg)` is a static analysis that already exists.
- Today a daemon-less run is still offered daemon tools, fails three times,
  and then the circuit breaker pauses it.
- `TriggerDefinition.daemon_id` is REQUIRED (`grpc/services/trigger.go:601`).

Stream H owns this (see §4).

## 4a. Using n8n as a reference (user direction)

n8n is a REFERENCE for understanding a provider's API, auth shape and webhook
behaviour. It is never code to copy. A checkout is at `~/src/n8n`:

- `packages/nodes-base/credentials/<Provider>*.credentials.ts`: what a
  provider's auth actually needs (OAuth URLs, scopes, extra params, where an
  API key goes, per-connection fields such as a subdomain).
- `packages/nodes-base/nodes/<Provider>/`: actions, and `*Trigger.node.ts` for
  how webhooks are registered and verified.

Do not port, translate or paste n8n code or text. Its licence (the
Sustainable Use License) is not open source and forbids that kind of reuse,
and our manifests are declarative anyway. Read it to learn the API, then
write ours from the provider's own docs (`research/INTEGRATIONS_V1_PROVIDERS.md`
collects them).

## 5. Gates

```
DATABASE_URL=postgres://postgres:postgres@localhost:55434/<your_db>?sslmode=disable REQUIRE_TEST_DB=1 \
  go test ./internal/<pkgs you touched>/...
make generate-go   # then confirm `git status` shows no drift in gen/
```

Test DB containers: `reliant-triggers-pg-db` (55434) and `reliant-triggers-pg`
(55433). Create your OWN database on one of them (`createdb` via `psql`).
Never use the shared dev Postgres on 5434; it holds real data.
