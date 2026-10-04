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

## 5. Gates

```
DATABASE_URL=postgres://postgres:postgres@localhost:55434/<your_db>?sslmode=disable REQUIRE_TEST_DB=1 \
  go test ./internal/<pkgs you touched>/...
make generate-go   # then confirm `git status` shows no drift in gen/
```

Test DB containers: `reliant-triggers-pg-db` (55434) and `reliant-triggers-pg`
(55433). Create your OWN database on one of them (`createdb` via `psql`).
Never use the shared dev Postgres on 5434; it holds real data.
