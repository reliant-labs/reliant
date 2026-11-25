# Status page: a public page built on the uptime probes

**Status:** the owner approved building a status page on 2026-11-25. It is the
first item of the platform-gap wave, picked up from the parked ticket
control-plane#3227. The design below is **proposed**. The open owner decisions
are collected in [section 11](#11-open-owner-decisions), and each one says what
it blocks.
**Date:** 2026-11-25
**Related:** control-plane#3227 (this ticket), control-plane#3230 (incidents,
parked), [changes.md](changes.md) (the precedent for "the release declares, the
control plane holds live state"), [start-anywhere.md](start-anywhere.md)
(decision 2: monitoring requires sign-in; decision 8: forge.yaml versus KCL),
`docs/benefits.md` (`Uptime and certificate probes`), control-plane
`docs/observability/uptime-prober.md`.

## Summary

A **status page** tells an app's own users whether the app works. It shows one
row per component, the component's current state, 90 days of daily availability
bars, and the history of incidents and maintenance. Its numbers come from the
outside-in uptime probes the platform already runs, so a team gets a page
without writing one and without wiring a second monitoring product.

The design has three parts:

- The **release declares** what the page shows: which uptime checks become which
  public components, under which names. This is `forge.StatusPage` in the env's
  KCL, next to the `forge.Uptime` it refers to.
- The **control plane holds live state**: whether the page is public, its
  incidents and maintenance windows, and its subscribers. The state is audited.
  People and agents change it through the same API.
- The **platform publishes** the page as a static artifact: HTML, a JSON
  document and an Atom feed, rendered by one forge renderer and served from
  hosted static hosting under a platform hostname or a custom domain. A visitor
  reads only what was published. No request from the public ever reaches tenant
  telemetry.

A page is private by default, and the private view is a console page for signed
in members. Making it public is a deliberate act by a person. Measured state
(up, down, no data) appears on its own. Words (incident updates) are drafted by
a person or an agent and published only by a person.

## Settled

1. **Build it** (owner, 2026-11-25), from control-plane#3227. The ticket asks
   for: one row per probed endpoint, current state, 90-day bars, incident
   history, hosting on Reliant static hosting, private by default, optionally
   public, a custom domain, and incident updates by a person or an agent.
2. **Monitoring requires sign-in** (owner, 2026-11-11, start-anywhere decision
   2). There is no claimable or anonymous path to observability. This proposal
   therefore treats a public page as a **published artifact**, not as anonymous
   read access. [Decision 1](#11-open-owner-decisions) asks the owner to confirm
   that reading, because the page is the first public surface fed by telemetry.
3. **Multi-region hosting is not now** (owner, 2026-11-25; control-plane#3240
   stays parked). The prober runs in one region, the platform cluster
   (`kcl/schema.k`, `UptimeCheck`: "Regions are not declared: v1 probes from the
   platform's own cluster"). The page states this limit and does not hide it.

## 1. Problem and experience

**What exists.** Every hosted env is probed with no declaration. forge derives
one check per `forge.OnHosted` frontend, per hosted workload that exposes a
port, and per HTTPRoute that has a hostname. `forge.UptimeCheck` adds more
checks (`kcl/schema.k:7251`, `schema Uptime`). Checks are recorded in the release
shape as `uptime_checks` (`pkg/release/uptime.go:57`, `ShapeUptimeCheck`;
`BundleFeatureUptimeChecks`). The control-plane worker
`internal/workers/uptime_prober` runs `internal/uptimeprobe`. It probes each
check from the platform cluster through `pkg/netguard` (public addresses only)
and writes `uptime.check.up`, `uptime.check.duration` and
`tls.cert.expiry_seconds` into the env's own telemetry. The alerts
`UptimeCheckFailing`, `UptimeCheckDown`, `CertificateExpiring` and
`CertificateExpiringSoon` read those metrics (`internal/obsdash/alerts_uptime.go`).
`pkg/obsquery/uptime.go` defines availability, incidents (3 failed probes in a
row), status and per-bucket timelines as vetted SQL. `pkg/obsbuiltin` draws them
as the Uptime dashboard and the Overview's Uptime card.

**What does not exist.** There is no page an app's own users can read, no
written incident or maintenance notice, no subscriber list, and nothing outside
the console. `rg -i 'statuspage|status page'` over the three repos finds nothing
(verified in #3227). Teams buy Statuspage, Instatus or Better Stack and set up
the same monitoring a second time.

**A prerequisite is off.** The prober's Deployment renders in prod
(`UPTIME_PROBER_RENDER = True`, control-plane `deploy/kcl/lib/obs_prod.k:143`),
but `uptime_prober.enabled` is still unset (false) everywhere. Prod writes no
samples today (`docs/observability/uptime-prober.md`, "Status"; step 2 of
"Turning it on" is the owner's). A status page with no samples shows "no data"
for every component, so slice 0 of the rollout is turning the prober on.

**What a person does.** In **Observability › Uptime & Status page** they see
the private page: every check of the env, its state and its bars, rendered by
the same renderer the public page uses. The project's `deploy/kcl/prod/main.k`
names the public components. "Publish page" makes the page public on a platform
hostname, and **Configure › Domains** binds `status.example.com` to it. During
an outage they open an incident from the down component, write or approve
updates, and resolve it. They schedule maintenance ahead of time.

**What an agent does.** When `UptimeCheckFailing` fires for a component on a
public page, the Autopilot on-call workflow (reliant
`internal/workflow/builtin/autopilot-oncall.yaml`, reached through an
`autopilot` alert receiver, control-plane `internal/alerting/autopilot.go`)
reads the timeline: `obs changes`, new issues and the last promotion. It
**drafts** an incident with a first update ("Investigating: Checkout is
unavailable since 14:02 UTC; a release went out at 13:58"). A person publishes
the draft or edits it first. The agent keeps drafting follow-ups and drafts the
resolution once the checks pass again.

**What a visitor sees.** A fast, accessible HTML page with no required
JavaScript. It shows an overall banner ("All systems operational" or "Partial
outage"), one row per component with 90 daily bars, open incidents with their
updates, upcoming maintenance, and the incident history. The page links to
`/feed.atom`, `/status.json` and (in a later slice) an email subscribe form.

## 2. What the page shows

### Components and checks

A **component** is a public name for one or more uptime checks of **one** env.
Its state is the worst state among its checks.

| Source                                         | Check name (today)        | Shown on the private view | Shown on the public page                     |
| ---------------------------------------------- | ------------------------- | ------------------------- | -------------------------------------------- |
| `forge.OnHosted` frontend (derived)            | `<frontend>`              | Yes, one row per check    | Only if a declared component names the check |
| Hosted workload with an exposed port (derived) | `<workload>`              | Yes                       | Same                                         |
| HTTPRoute with a hostname (derived)            | `route-<route>`           | Yes                       | Same                                         |
| `forge.UptimeCheck` (declared)                 | its `name`                | Yes                       | Same                                         |
| A check the env opted out of (`Uptime.skip`)   | not in the shape, no data | No                        | No; naming it is a render error              |

The public page is **opt-in per component**. A derived check on `route-admin`
must never become public because someone published the page. The private view
lists every check, so the console stays useful with nothing declared.

### State vocabulary

The state comes from `pkg/obsquery` definitions, so the page, the Uptime
dashboard and the alerts agree on what "down" means.

| Component state      | Measured when                                                                                                                                                                             |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Operational          | Every check's newest probe passed, and no check has an open run of failures.                                                                                                              |
| Degraded             | At least one recent probe failed, but no run reached `UptimeIncidentFailures` (3).                                                                                                        |
| Outage               | An uptime incident is open on a check (3 or more failed probes in a row), the definition `UptimeChecksSQL` uses.                                                                          |
| Maintenance          | A published maintenance window covering the component is active. It takes precedence over the measured state.                                                                             |
| No data              | The newest probe is older than `UptimeStaleAfter` (15 minutes), or the check has no samples. Shown as grey and never as "operational": silence from the prober is not evidence of health. |
| (override) any state | A person set it on an incident, for example to correct a single-region false positive. It is labelled on the page and recorded ([decision 5](#11-open-owner-decisions)).                  |

### History bars

There is one bar per UTC day for 90 days. A bar shows the day's availability:
the share of that day's probes that passed, which is the `obsquery` definition.
A day with no probes is grey. Probes inside a published maintenance window are
excluded from availability and the bar marks them. The incidents published on
that day show on hover.

Metrics are kept for **30 days** on hosting (`MetricsDays: 30`, control-plane
`internal/obstenant/render.go:51`), so 90 days of bars cannot be read from
`otel_metrics_gauge`. They need a durable daily rollup
([decision 7](#11-open-owner-decisions)). The proposal is a tenant-schema
rollup table, `uptime_check_1d` (env, check, day, probes, passed, first failure,
last failure). A materialized view on `otel_metrics_gauge` fills it at ingest,
and it is retained like the existing query rollups (`RollupDays: 395`, the
schema v21 pattern at `010_tenant_create.sql`, "The query rollups"). `obsquery`
gains `UptimeDailySQL`, which reads the rollup, with the raw table as the
fallback for the current day.

### What is public, and what never is

The public document is an **allowlist projection**, defined in forge
`pkg/statuspage` and tested on its own:

- **Public:** the page title, logo URL, links, component display names and
  descriptions, component states, daily availability per component, published
  incident and maintenance text with timestamps and affected components, and a
  `generated_at` time.
- **Never public:** check names, URLs and paths, hostnames, HTTP statuses, error
  types, latency, certificate expiry, env ids and names, org and project ids,
  alert names, issue titles, the identity of the person who published, whether
  an update was drafted by an agent, and any unpublished draft.

## 3. Architecture

```
 deploy/kcl/prod/main.k                                control plane
   forge.Uptime  (checks)        --release shape-->      uptime_prober --> env telemetry (otel_metrics_gauge)
   forge.StatusPage (components)                                              |  MV at ingest
                                                                              v
                                                                         uptime_check_1d (395d)
   StatusService (people, agents) --> status_pages, status_incidents,         |
                                      status_updates, status_maintenance      |
                                                    \                         /
                                                     status_publisher (worker, one lease per page)
                                                       builds forge.status/v1 document
                                                       renders with forge pkg/statuspage
                                                                 |
                       platform bucket: status/<org>/<env>/live/{index.html,status.json,feed.atom}
                       host index: <platform host> and custom domains --> that prefix
                                                                 |
                                          staticorigin + CDN  -->  visitor
```

### Declaration (proposed): `forge.StatusPage` in the env's KCL

```kcl
uptime = forge.Uptime {
    checks = [forge.UptimeCheck {name = "api-ready", target = "api", path = "/readyz"}]
}
status_page = forge.StatusPage {
    title = "Acme status"
    links = [forge.StatusLink {label = "Support", url = "https://acme.com/help"}]
    components = [
        forge.StatusComponent {name = "Website", checks = ["web"]}
        forge.StatusComponent {name = "API", checks = ["api-ready", "route-billing"], description = "REST and webhooks"}
    ]
}
```

This is a sketch, not settled syntax. A render validates that every named check
exists in the env's final check list: derived checks minus `skip`, plus declared
checks. This follows how `Uptime.skip` refuses an unknown name. The page lands
in the shape as `status_page`, behind a new `BundleFeatureStatusPage` with strip
logic in `pkg/release/bundle_features.go`, because the shape is strict-decoded
and older control planes must not choke on it. That is the same path
`BundleFeatureUptimeChecks` took. The control plane reads the page from the
shape through `alerting.Store.CurrentShapes`, as the prober reads checks.

KCL is proposed over forge.yaml because components refer to checks, and checks
are per-env topology that lives only in KCL today (start-anywhere decision 8:
"YAML holds switches and simple values; KCL holds topology"). Changes went to
`changes.yaml` because a change is identical in every env. A status page is the
opposite case: it describes one env. This is
[decision 3](#11-open-owner-decisions).

**Publishing is not declared.** Whether the page is public is live control-plane
state, changed by a person, like a domain binding (`forge domain` keeps domains
out of KCL for a similar reason: an outward-facing, human-gated act that must
not flip with a rollback). Rolling back to an older release must not publish or
unpublish a page. That is [decision 2](#11-open-owner-decisions).

### State (proposed): control-plane tables

All tables are new and backward-compatible, keyed on `deploy_environments.id`
as Changes is:

| Table                   | Holds                                                                                                                                                                                                                                                                                                    |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `status_pages`          | One per env with a declared page: visibility (`private`, `public`), platform hostname, `published_at`, `published_by`, last published document revision and ETag.                                                                                                                                        |
| `status_incidents`      | Title, impact (`minor`, `major`, `critical`), lifecycle (`investigating`, `identified`, `monitoring`, `resolved`), affected components with an optional per-component state override, start and resolve times, and an optional link to an alert fingerprint (later, the control-plane#3230 incident id). |
| `status_updates`        | Append-only, with an immutability trigger like `deploy_promotions`. Each row has an incident or maintenance id, body (Markdown, rendered to a safe subset), lifecycle step, `state` (`draft`, `published`, `discarded`), author and author kind (`person`, `agent`), and the publisher.                  |
| `status_maintenance`    | A scheduled window, affected components, an option to silence those checks' alerts during it (creates an `alert_silences` row, `internal/alerting/silences.go`), and its lifecycle.                                                                                                                      |
| `status_subscribers`    | In a later slice ([section 5](#5-subscribers)): an email hash and an encrypted address, a confirmation state, an unsubscribe token, and component filters.                                                                                                                                               |
| `status_page_revisions` | Every publish: the document revision, its digest and who or what caused it. The audit ledger; `forge/pkg/activity` mirrors it into the user-visible stream.                                                                                                                                              |

### Publishing: one document, one renderer

The **document** is `forge.status/v1`, a JSON schema owned by forge in
`pkg/statuspage`. It holds the page, its components and states, the 90 daily
bars, open and recent incidents with their published updates, maintenance, and
`generated_at`. It is the public contract: a team that wants its own design
builds it from `/status.json`, so the 20% are never disempowered.

The **renderer** is pure and dependency-free in `pkg/statuspage`, using Go
`html/template`. It turns a document into `index.html`, `feed.atom` and the
canonical `status.json`. It emits no third-party script, the page works with
JavaScript off, and it meets WCAG AA contrast. State is shown as an icon plus a
word, never as colour alone. The control plane and forge both call it, so
self-managed users and `forge` previews get the same bytes
([section 7](#7-forge-only-and-self-managed-users)).

The **publisher** is a new control-plane worker, `status_publisher`, with one
`forge/pkg/lease` lease per shard of pages, the scheduling model the prober
uses. For each page with a declared shape it:

1. reads current state and the bars as the org's read-only ClickHouse user,
   through vetted `obsquery` templates, as `GetUserActivity` does
   (`internal/handlers/observability/rpc_get_user_activity.go`). It uses the
   `automation` query budget (`pricing.yaml`, `observability.query_limits`);
2. reads published incidents, updates and maintenance from Postgres;
3. builds the document, renders it, and writes the three files only when the
   document's digest changed, plus a `generated_at` heartbeat at most every 5
   minutes so a visitor can tell a live page from a frozen one;
4. runs every 60 seconds, and at once on a publish, a resolve or a maintenance
   boundary (a Postgres `NOTIFY`, the way the reconciler is woken in
   `internal/app/reconcile_wire.go`).

Private pages are never written to the bucket. The console renders the private
view from the same document, which it gets through `GetStatusPage` under
`obs:read`.

### Serving, hostnames and domains

Files go to a platform-owned prefix, `status/<org>/<env>/live/`, beside the
sites' `sites/<org>/<env>/<site>/` prefixes (`internal/operators/staticsite/layout.go`).
A host-index entry maps the page's hostnames to that prefix
(`internal/staticorigin`, `hosts/<hostname>/site.json`). The page is therefore
served by the same static origin and CDN as every hosted site, with no new
serving path. `index.html`, `status.json` and `feed.atom` are `no-cache` with
an ETag, the treatment `ChangesPath` already gets
(`internal/staticorigin/headers.go`). The page also sets `stale-if-error`, so
the CDN keeps serving the last page through an origin error.

- **Platform hostname.** It is allocated by the same `hostname.Request`
  allocator static sites use, on the static-site base domain.
- **Custom domain.** `forge domain add status.acme.com --env prod`, then
  `forge domain bind status.acme.com --env prod --target @status`. A binding's
  target is a workload or static-site name inside the env (`DomainBinding.target`,
  control-plane `proto/controlplane/v1/deploy.proto:850`). Names are RFC 1123
  labels, so the reserved target `@status` can never collide with a tenant's
  workload. The domain handler and the static origin's resolver learn that one
  reserved target. Verification and certificates work unchanged.

**Shared fate.** The page is served by the platform that hosts the app it
reports on. A backend outage does not reach the static origin, so the common
case (the app's API is down) is reported correctly. An outage of Reliant's
static origin or CDN takes down the hosted site and its status page together,
and `stale-if-error` softens that only for cached pages. Independent hosting is
[decision 10](#11-open-owner-decisions). The prober itself shares fate with the
cluster it runs in. Its external heartbeat (`/healthz/uptime-prober`, probed by
Cloud Monitoring) is unchanged, and a stale prober shows "No data", never
"Operational".

## 4. Incidents and maintenance

**A status incident is a communication, not a detection.** `obsquery` already
calls a run of 3 failed probes an "incident". The page shows that run as the
component's **Outage** state on its own. A **status incident** is the written,
human-approved notice about it. The two are linked when one was opened from the
other, and they are never conflated: an outage can have no notice, and a notice
can exist with no measured outage ("Emails are delayed").

| Lifecycle step | Who moves it                    | Public effect                                                                    |
| -------------- | ------------------------------- | -------------------------------------------------------------------------------- |
| draft          | person or agent                 | None.                                                                            |
| investigating  | person publishes                | The incident appears with its first update; affected components show its impact. |
| identified     | person publishes an update      | A new update.                                                                    |
| monitoring     | person publishes an update      | A new update.                                                                    |
| resolved       | person publishes the resolution | Closed and kept in history.                                                      |

**Updates are append-only.** A published update can be corrected only by a new
update; the original stays in the ledger. A published update can be hidden from
the page by a person (to remove a mistake or a leaked secret). The hiding is
recorded with a reason.

**Agent drafting.** An agent writes drafts through `DraftStatusUpdate` with
`status:write`. A draft raises an Inbox item and an approval card for people
holding `status:publish` in that environment. Publishing requires a
**console-session principal**, as Changes decision 3 proposes: a person's
`user_token` held by an agent is not a person here. Whether an agent may ever
publish on its own is [decision 4](#11-open-owner-decisions). The agent's input
is the timeline the incidents ticket (control-plane#3230) describes: the alert's
`Why` findings, `obs changes`, new and regressed issues, and the last promotion.
None of that is copied into the public text unless a person publishes it.

**Maintenance.** A person schedules a window (start, end, components, text).
It is published ahead of time as "Scheduled", shows as **Maintenance** while
active, and closes on its own at the end unless extended. Optionally it creates
an alert silence for the affected checks for the same window, so planned
downtime does not page anyone.

**Relationship to control-plane#3230.** That ticket proposes an internal
incident object: owner, severity, timeline, runbooks and postmortem. This
proposal does not build it. Status incidents carry a nullable link, and when
#3230 lands its incident gets a "Post to status page" action that creates a
draft here. Building the incident object first is
[decision 8](#11-open-owner-decisions).

## 5. Subscribers

Subscribers turn the page from "pull" into "push". They are also the first time
Reliant would store personal data that belongs to a **customer's customers**.
They are sliced by data risk:

| Channel                                     | Personal data                      | Proposed slice                                   |
| ------------------------------------------- | ---------------------------------- | ------------------------------------------------ |
| Atom feed (`/feed.atom`) and `/status.json` | None                               | First public slice.                              |
| Email, double opt-in                        | Email addresses of the app's users | Its own slice, after a privacy and terms review. |
| Webhook, Slack, SMS                         | Endpoints or phone numbers         | Out of v1.                                       |

Email proposal: a subscribe form on the page posts to one **unauthenticated**
rpc, `SubscribeStatus` (`auth_required: false`, the only public write in this
design). It is rate-limited per IP and per page, and it only ever sends a
confirmation email, so it cannot be used to mail a third party more than once.
Confirmation and one-click unsubscribe (RFC 8058 `List-Unsubscribe-Post`) are
tokens in the link. Mail is sent through the existing Resend client
(`internal/email`) from a Reliant sending domain, with the page title as the
display name. It is sent on publish of an incident's first update, of its
resolution, and of maintenance (24 hours before, and at start). Middle updates
are sent only if the subscriber opted in. Addresses are stored encrypted, deleted
with the page, and never exported or shown in full in the console. Who controls
this data, and on what terms, is part of [decision 6](#11-open-owner-decisions).

## 6. Public serving and the sign-in rule

The owner settled that **monitoring requires sign-in**. A public status page
would be the first public surface fed by tenant telemetry, so this section
says why it does not reopen that rule:

- **No query path from the public.** Visitors read files the platform wrote.
  Nothing a visitor sends reaches ClickHouse, Postgres or an rpc, except the
  rate-limited subscribe form, which reads nothing.
- **An allowlist, not a filter.** The document type has only the fields listed
  in [section 2](#what-is-public-and-what-never-is). A new telemetry field cannot
  leak by default, because the projection must name every field it copies.
- **Off until a person turns it on.** Pages are private by default. Publishing
  needs `status:publish` and a console session, and it is audited.
- **Claimable deploys get no page.** A claimable env has no signed-in owner and
  no monitoring, so it has nothing to publish.

The alternative, a public read API that runs `obsquery` for anonymous callers,
is anonymous observability under another name, and this proposal rejects it
([section 10](#10-alternatives-considered)).

## 7. Forge-only and self-managed users

| User                                                                                     | Probes                                                                                                                                                                                       | Page                                                                                                                                                     |
| ---------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Reliant hosting                                                                          | Derived and declared checks.                                                                                                                                                                 | Everything here.                                                                                                                                         |
| Self-managed env, signed in (`forge.ControlPlane` declared; `OnCluster` or `OnExternal`) | Declared `url` checks and routes with hostnames (the prober reads every persistent env's shape; derived target checks need hosting). `forge.UptimeCheck {url = ...}` covers everything else. | The same: the platform publishes it. The app runs anywhere and the page is on Reliant static hosting.                                                    |
| Start-anywhere monitoring only (`monitoring:` in forge.yaml, no KCL)                     | None today: checks are declared only in KCL.                                                                                                                                                 | None until checks have a forge.yaml form. If that lands, `status_page:` follows it, as one fact in one place (decision 8 forbids the same fact in both). |
| No Reliant account                                                                       | None. forge ships no prober.                                                                                                                                                                 | `pkg/statuspage` and the `forge.status/v1` schema are public, so a team can render a page from its own monitor's data.                                   |
| Dev (`forge env up dev`)                                                                 | None (a local stack has no prober).                                                                                                                                                          | `forge obs statuspage preview dev` renders the declared page from a sample document to a local file, so the layout can be checked before a deploy.       |

A self-hosted prober in forge is out of v1 ([decision 9](#11-open-owner-decisions)).

## 8. API, scopes, UI, CLI and agent tools

**`StatusService`** is a new control-plane service
(`proto/services/status/v1/status.proto`), not more rpcs on `DeployService` or
`ObservabilityService`. The rpcs are proposed:

- `GetStatusPage` (private view and document, `obs:read`)
- `PublishStatusPage` and `UnpublishStatusPage` (`status:publish`, console
  session)
- `CreateStatusIncident`, `DraftStatusUpdate`, `DiscardStatusDraft`,
  `ScheduleMaintenance` and `UpdateMaintenance` (`status:write`)
- `PublishStatusUpdate`, `SetComponentOverride` and `HideStatusUpdate`
  (`status:publish`, console session)
- `ListStatusIncidents` and `ListStatusRevisions` (`obs:read`)
- `SubscribeStatus`, `ConfirmStatusSubscription` and `UnsubscribeStatus`
  (public, in the subscriber slice)

Every mutation takes `expected_revision`, the precedent Changes follows.

**Scopes.** Two new members of forge's closed set (`pkg/accesstoken/scope.go`)
are proposed. `status:write` lets a holder draft and schedule. `status:publish`
lets a holder publish, override and change visibility. Both can be bound to an
environment. The order is a forge PR, then a control-plane migration widening
the `CHECK`, then `OrgScopes` and `EnvScopes`, as `changes:write` and
`changes:launch` went. `obs:write` is not reused: it authors dashboards, and a
dashboard editor is not someone who speaks for the company in public.

**Console** (reliant `frontends/web`). The page is
`components/Forge/Observability/StatusPage/`, registered with
`registerConsoleNavEntry` (`components/Forge/Console/consoleNavRegistry.ts`) as
**Observability › Uptime & Status page**, the ticket's placement. It holds the
private view, the publish toggle (with a preview of exactly what goes public),
incidents with the draft and approval flow, and maintenance. The custom domain
lives in the existing **Configure › Domains** (`components/Forge/Domains`). The
console calls `controlplane.v1.*` clients from the vendored proto, synced from
control-plane's `proto/public-api.txt`, with a demo fixture as observability has.

**CLI** (forge). The commands are `forge obs statuspage show <env>`, `publish`,
`unpublish`, `incident open|update|resolve`, `maintenance schedule` and
`preview`. They sit under `obs`. `statuspage` is one word because
`forge obs status` already exists and answers "what is wrong now". CLI commands,
flags and `--json` shapes are a public surface, so they ship with the slice that
needs them and not before.

**Agent tools** (reliant `internal/llm/tools`, modelled on `change_tools.go`).
`get_status_page` is read-only. `manage_status_incident` has the actions
`draft`, `update_draft` and `discard`. Publishing raises an approval card bound
by hash to (incident, update revision). An unattended Autopilot run may only
draft.

## 9. Pricing and billing

What a page costs Reliant:

- one small rollup row per check per day;
- one query batch per page per minute, on the `automation` budget;
- a few kilobytes written to the bucket only when the document changes;
- static egress for visitors, already metered (`hosting.static_egress`, 10 GiB
  a month included);
- email sends in the subscriber slice.

The proposal ([decision 11](#11-open-owner-decisions)):

- The private view is included for every signed-in org. It is part of
  monitoring.
- One public page per org is included and does **not** count as a static site.
  It counts neither toward the free tier (`computegate`,
  `StaticFreeTierExceeded`) nor toward a billed org's live cap
  (`planlimits.StaticSiteCap`). Otherwise a free user who hosts their one site
  could never publish its status page. (`pricing.yaml` `extra_static_site` is
  shadow-only and charges nothing in any case.)
- Additional public pages and email subscribers past a free cap need billing.
  The cap is set in the subscriber slice from Resend's real cost. Until billing
  is set up, the publish is refused with the billing link, as other hosting
  limits are (exit 7 in the CLI).
- A custom domain on the page follows the existing domain rules.

The benefits registry gains a row: "A public status page from the uptime
probes", with its limits (one probe region, shared fate). `compare` gains the
row for Statuspage, Instatus and Better Stack.

## 10. Alternatives considered

| Alternative                                                                       | Why not                                                                                                                                                                                                                                                                                                                                          |
| --------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Integrate Atlassian Statuspage** (push our probe state through its API)         | It is the market default with mature subscriber tooling. Components are maintained by hand and do not follow the release. The user needs a second account and a second bill, and it adds a credential we would hold. It does not meet "day 0 with no setup". A team that wants it can still drive it from an alert webhook receiver.             |
| **Integrate Instatus**                                                            | It has fast static pages and its own monitors. The second monitor duplicates ours and can disagree with our alerts about whether something is down. It has the same account, bill and credential costs as Statuspage.                                                                                                                            |
| **Integrate Better Stack**                                                        | It is the closest to this design: uptime, status pages, incidents and on-call in one product. Pointing users at it hands over the whole reliability workflow, which is where our agent angle (draft, triage, postmortem from `obs changes`) is strongest. It would be a competitor's feature in our product.                                     |
| **Open-source self-hosted pages** (Upptime, OpenStatus, Cachet)                   | They are good for teams without a platform. Each brings its own monitor or database and a deploy of its own, which is the setup this feature removes. Our renderer and JSON schema stay open, so such a page can be fed from `/status.json`.                                                                                                     |
| **A public read API over `obsquery`** (the page queries live on each visit)       | Always fresh, with no publisher. It is anonymous access to tenant telemetry, which the sign-in rule forbids. It also puts public traffic on ClickHouse and makes an outage of the query path an outage of the status page.                                                                                                                       |
| **A platform-managed `StaticSite` custom resource** per page                      | It would reuse the operator's hostname, CDN and host-index code wholesale. But the operator converges releases (immutable archive prefixes, promotions), and a page has no release; publishing every 60 seconds through it would be releases nobody built. The publisher reuses the operator's layout, host-index and allocator helpers instead. |
| **A Next.js or Vite page shipped as a site template** the user deploys themselves | Fully customizable. It is a project to maintain, it needs a data endpoint, which is the anonymous-API problem again, and it is not "day 0". The `forge.status/v1` JSON keeps this open for the 20%.                                                                                                                                              |
| **Declaring visibility in KCL** (`public = True`)                                 | It is declarative, but a rollback would flip it, and going public is a person's act. See decision 2.                                                                                                                                                                                                                                             |

## 11. Open owner decisions

None of these is decided. Per project rules, the owner decides architectural
choices. Each row gives the realistic options, a recommendation and what it
blocks.

| #   | Decision                                         | Options                                                                                                                                                                                                                                                                                  | Recommendation                                                                                                                                                                                                                                                                                                                    | Blocks                                                        |
| --- | ------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------- |
| 0   | Turn the uptime prober on in prod                | (a) now, as uptime-prober.md steps 2 to 4 describe (`uptime_prober.enabled = true` and the Cloud Monitoring heartbeat, phase 15); (b) later, with the status page shipping private-only.                                                                                                 | (a). It is not new architecture, but it is the owner's step in that runbook, and every slice below shows "No data" without it. The alerts it powers are worth having anyway.                                                                                                                                                      | Every slice's dogfood; the 90-day history starts on this day. |
| 1   | May a status page be public?                     | (a) yes, as a **published artifact**: a platform-written allowlist projection, no public query path ([section 6](#6-public-serving-and-the-sign-in-rule)); (b) no, private console view only; (c) a public live read API.                                                                | (a). It honors "monitoring requires sign-in": no one reads telemetry anonymously, they read what a person chose to publish. (c) is anonymous observability.                                                                                                                                                                       | Every public slice.                                           |
| 2   | How a page becomes public                        | (a) live control-plane state set by a person with `status:publish` in a console session, audited, like a domain binding; (b) `public = True` in KCL, applied on deploy; (c) both, KCL as the default.                                                                                    | (a). Going public is an outward-facing act that must survive a rollback unchanged, which a declared field cannot promise.                                                                                                                                                                                                         | `PublishStatusPage`, console toggle.                          |
| 3   | Where the page's components are declared         | (a) `forge.StatusPage` on the env's Bundle in KCL, beside `forge.Uptime`; (b) a forge.yaml `status_page:` section; (c) console-only configuration in the control plane.                                                                                                                  | (a). Components name per-env checks, which exist only in KCL (start-anywhere decision 8: topology in KCL). (c) breaks "declarative over imperative". A forge.yaml form follows only if checks gain one.                                                                                                                           | Shape field, render validation, the skill.                    |
| 4   | May an agent publish on its own?                 | (a) never: agents draft, a person publishes every word; (b) per-page opt-in: an agent may publish a **fixed template** ("Investigating: API is unavailable") after N minutes of a measured outage, free text still needs a person; (c) agents publish freely, as the ticket's text says. | (a) for v1, then (b) as an opt-in once drafts have a track record. Public words speak for the company. The measured Outage state already appears with no one acting, so (a) does not leave visitors uninformed. The ticket says "the agent posts updates"; the brief for this wave says human-published, so this needs your call. | Agent tools, Autopilot step, approval card.                   |
| 5   | Measured or manual component state               | (a) measured by default, a person may override with a labelled, audited state; (b) manual only, probes only suggest (Statuspage's model); (c) measured only, no override.                                                                                                                | (a). Day-0 value needs measured state. One probe region means false positives happen, so a person must be able to correct them, visibly.                                                                                                                                                                                          | `SetComponentOverride`, state logic.                          |
| 6   | Subscriber channels and data responsibility      | (a) feeds only (Atom and JSON), no personal data; (b) also email with double opt-in, Reliant processing the app's users' addresses under the customer's instruction; (c) also SMS, Slack and webhooks.                                                                                   | (a) in the first public slice, (b) as its own slice after a privacy and terms review (subprocessor list, DPA wording, deletion). (c) later.                                                                                                                                                                                       | Subscriber slice, `SubscribeStatus` (the only public write).  |
| 7   | Where 90 days of history live                    | (a) a tenant-schema ClickHouse rollup `uptime_check_1d`, retained like the query rollups (395 days); (b) a Postgres daily table the publisher writes; (c) show 30 days, the metrics retention.                                                                                           | (a). It is one source, filled at ingest, the established rollup pattern, and `obsquery` reads it for the dashboard too. (b) is a second copy of the same fact.                                                                                                                                                                    | The bars; the tenant schema migration.                        |
| 8   | Order relative to incidents (control-plane#3230) | (a) status incidents stand alone now, with a nullable link the #3230 incident fills later; (b) build the #3230 incident object first, and the page publishes from it.                                                                                                                    | (a). The page is useful with no incident model, and the link keeps one future record. (b) delays the page by an M-sized design.                                                                                                                                                                                                   | Incident tables and rpcs.                                     |
| 9   | Forge-only users with no account                 | (a) public renderer and schema only, no self-hosted prober; (b) ship a self-hosted prober in forge.                                                                                                                                                                                      | (a). Monitoring requires sign-in. A forge prober is a second monitoring runtime to maintain for users we do not monitor.                                                                                                                                                                                                          | `pkg/statuspage` scope; docs.                                 |
| 10  | Where pages are hosted (shared fate)             | (a) Reliant static hosting with `stale-if-error`, the limit published; (b) a separate serving path on independent infrastructure (another provider or bucket and CDN).                                                                                                                   | (a) for v1, stated honestly in benefits and compare. (b) is a multi-provider project, and multi-region is "not now".                                                                                                                                                                                                              | Serving slice; what status.reliantlabs.io may claim.          |
| 11  | Pricing                                          | (a) private view free; one public page per org free and exempt from the static-site count; email past a free cap and further pages need billing; (b) a public page needs billing; (c) a priced add-on.                                                                                   | (a). The ticket's premise is "every production app needs a status page on day 0". Its marginal cost is small and bounded.                                                                                                                                                                                                         | `computegate`, `pricing.yaml`, benefits row.                  |
| 12  | New scopes                                       | (a) `status:write` and `status:publish` in forge's closed set (a semver minor); (b) reuse `obs:write` or `obs:triage`.                                                                                                                                                                   | (a). Speaking publicly is a separate authority from editing dashboards or triaging issues, and it must be grantable per environment.                                                                                                                                                                                              | Authorization for every mutation.                             |

## 12. Rollout plan

The ticket sized this **S**: a page over existing data. The honest size is
**S for a private view plus a public page with feeds**, and **M** with
incidents, maintenance, agent drafting and email. Each slice ships alone.

0. **Prober on** (decision 0). `uptime_prober.enabled` in prod, the heartbeat,
   and one env's `uptime.check.up` row confirmed. History accrues from here.
1. **forge: schema, renderer, declaration.** `pkg/statuspage`: the
   `forge.status/v1` document, validation, the HTML, Atom and JSON renderer,
   golden files, and a test that pins the allowlist (a new field fails until
   classified). `forge.StatusPage` in KCL with render validation, the
   `status_page` shape field, and `BundleFeatureStatusPage` with strip logic.
   The `observability/status-page` skill. `forge obs statuspage preview`.
2. **control-plane: history.** The `uptime_check_1d` rollup (tenant schema
   migration) and `obsquery.UptimeDailySQL`. The dashboard's range beyond 30
   days reads it too.
3. **control-plane and console: private view.** `StatusService.GetStatusPage`
   under `obs:read`, and the console page behind a `ReliantWebConfig` flag. The
   first user-visible value.
4. **Public page.** Scopes (the forge PR first), `status_pages`,
   `PublishStatusPage`, the `status_publisher` worker, bucket prefix and host
   index, `no-cache` and `stale-if-error`, the `@status` domain target, and
   feeds. Behind decisions 1, 2, 10 and 11.
5. **Incidents and maintenance.** Tables, rpcs, the append-only ledger,
   overrides, the maintenance-silence link, console flows and CLI verbs.
6. **Agent drafting.** Agent tools, the Autopilot on-call step, Inbox and
   approval card (decision 4).
7. **Email subscribers.** After the review in decision 6.

**Dogfood.** First, **status.reliantlabs.io** reports `reliantlabs.io`, the
landing page's prod env on hosted static hosting, with its derived check, plus
`forge.UptimeCheck {url = ...}` checks for the public app and API endpoints. It
is published from slice 4, with incidents from slice 5, and Reliant's own
on-call drafts its updates from slice 6. It carries decision 10's caveat: it
shares fate with Reliant static hosting, and the Cloud Monitoring heartbeat
stays the independent signal for the owner. Second, one external user's hosted
app publishes a page with a custom domain, which exercises domain binding,
billing and the free-tier exemption.

**Explicitly out of v1:** probing from several regions; private pages for an
audience (password or SSO for a customer's customers); per-subscriber component
filtering beyond email; SMS, Slack and webhook subscribers; on-call schedules
and escalation (control-plane#3230); response time charts on the public page;
custom HTML themes beyond title, logo, links and an accent colour; third-party
component status (for example "Stripe"); and a self-hosted prober.
