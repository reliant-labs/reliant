# Static-site edge: request logs in the org's telemetry, and Markdown negotiation

**Status:** approved. Raised by the owner on 2026-11-25. Nothing here is built.
The architectural choices are collected in
[section 8](#8-owner-decisions-decided-2026-11-26); on 2026-11-26 the owner
accepted every recommendation there ("take your recommendations on all"), so
all nine are the [Settled](#settled-owner-2026-11-26) list and no slice waits
on a decision.
**Date:** 2026-11-26
**Related:** control-plane#3745 (request logs; the ticket), the owner's
2026-11-25 ask for `Accept: text/markdown` negotiation (B),
[changes.md](changes.md) (static-site delivery, the precedent for a feature
the operator writes into the site's serving document),
[status-page.md](status-page.md) (the precedent for an opt-in declaration that
renders byte-identically when absent), [start-anywhere.md](start-anywhere.md)
(decision 2: monitoring requires sign-in), control-plane
`docs/static-edge-metering.md` (the edge-log pipeline this reuses).

Code references are to `control-plane/` (cp) and `forge/` at their `main` of
2026-11-26. Line numbers drift; the symbol named beside each is the anchor.

## Summary

Reliant hosted static sites are served by cp's `internal/staticorigin` behind
one Google Cloud external Application Load Balancer backend service,
`static-sites`, with Cloud CDN (cp `deploy/gcp/provision.sh:3366-3369`). Two
things a site owner wants cannot be had today:

- **A. Who reads my site.** The edge logs every request at sample rate 1.0, but
  only to Cloud Logging in Reliant's GCP project. The org's telemetry scope sees
  only requests that reach the origin (cp `internal/proxy/ingress_red.go:56-58`
  says so), and on reliantlabs.io that was 89 of thousands in a week. A site
  owner cannot see traffic, top pages, 404s, cache-hit ratio or which crawlers
  and AI agents read them. Answering "does ClaudeBot read our `.md` twins?" took
  `gcloud logging read` with Reliant's own credentials.
- **B. Markdown for agents at the page's own URL.** Sites publish `<path>.md`
  twins (reliantlabs.io does, linked with `<link rel="alternate"
type="text/markdown">`). An agent that sends `Accept: text/markdown` to
  `/features` still gets HTML: the origin never varies on a request header, and
  the only `Vary` it sends is `Accept-Encoding` (cp
  `internal/staticorigin/origin.go:860-862`).

The proposal:

- **A.** A second Cloud Logging sink carries every edge request (not only the
  cache hits the metering sink carries) through Pub/Sub to a new cp worker. The
  worker maps each request's host to its org, env and site through the host
  index, classifies the user agent against a maintained agent list that lives
  in forge, and writes rows into the org's own ClickHouse database through the
  existing gateway with the env's telemetry token, the way the uptime prober
  does. They land in a new tenant table, `edge_requests`, beside a daily rollup.
  Owners read it with `forge obs traffic`, a "Traffic" and a "Bots & agents"
  group on the code-derived frontend dashboard, `forge obs query`, and the
  console. Billed as observability ingest, and switched off per env with
  `forge.ObservabilitySignals {edge = False}`.
- **B.** An opt-in `markdown_negotiation = True` on a hosted `forge.Frontend`.
  The operator records which pages have a twin in the site's serving document
  at publish time. When an opted-in site gets a request whose `Accept` header
  prefers `text/markdown` over `text/html`, for a page with a twin, the origin
  serves the twin. Both representations of that page carry `Vary: Accept`; the
  HTML one also carries `Link: <…md>; rel="alternate"; type="text/markdown"`.
  The decision is made at the origin, not by a CDN rule, because Cloud CDN will
  not put `Accept` in a cache key but does honour `Vary: Accept`. A site that
  has not opted in gets byte-identical responses, and a browser never sees a
  difference.

Both features ride the same seams: the StaticSite spec, the operator's
`serving.json`, and the edge's request log.

## Settled (owner, 2026-11-26)

The owner accepted each recommendation of
[section 8](#8-owner-decisions-decided-2026-11-26) ("take your recommendations
on all"):

1. **Source of edge request data:** the CDN request log, through a second sink,
   Pub/Sub and a control-plane worker, to the gateway.
2. **On by default.** An env opts out with `ObservabilitySignals.edge = False`.
   It starts for existing sites when the control-plane slice ships, and the
   release notes announce it.
3. **The agent list** is forge `pkg/agentua`, embedded YAML shipped by forge
   releases. A community list may seed it once its licence is checked.
4. **Billing:** observability ingest, like any signal (10 GiB included, then
   10¢/GiB).
5. **Retention:** raw rows on a new `edge_days` (default: the env's info-log
   days), and a daily rollup on the product-events retention (90 days free,
   400 paid).
6. **Client address:** a truncated network (/24, /48) and a country, never the
   full IP. The country column stays empty until control-plane has a GeoIP
   source.
7. **Markdown negotiation is declared** with `markdown_negotiation = True` on a
   hosted `forge.Frontend`; twins are detected from the build.
8. **The origin decides at serve time**, with `Vary: Accept` and no CDN change.
9. **The `.md` serving fix ships with B for every site:** `.md` is a static
   file extension, and a missing twin is a 404.

These are not reopened below.

## 1. Problem and experience

### What the evidence shows

Seven days of reliantlabs.io edge logs (2026-11-18 to 2026-11-25, two filtered
pulls of 1,436 and 2,284 entries), read with `gcloud` in Reliant's project:

| Agent         | HTML pages | `.md` twins | `llms.txt` |
| ------------- | ---------- | ----------- | ---------- |
| ClaudeBot     | 443        | 15          | 2          |
| Claude-User   | 17         | 1           | 2          |
| PerplexityBot | 216        | 15          | 0          |
| GPTBot        | 126        | 1           | 1          |
| ChatGPT-User  | 173        | 0           | 0          |

Five facts in the same data shape the design:

1. **Agents ask for HTML.** Of the requests ClaudeBot made for pages, 3% were
   `.md` twins. An agent that would rather read Markdown has no way to ask the
   page's own URL for it. This is B's case.
2. **The user agent is a claim, not an identity.** 682 of the 2,284
   AI-agent-named requests probed for secrets (`/.env`, `/@fs/.env`,
   `/proc/self/environ`, `/server.key`), carrying ClaudeBot, GPTBot,
   PerplexityBot and ChatGPT-User strings. They were almost certainly spoofed. A
   "Bots & agents" panel that trusts the string would report that ClaudeBot
   probes for `.env` files. Classification needs a **verified** bit (section 2).
3. **404s are mostly scanners.** The top 404 paths were `robots.txt` on hosts
   that ship none (app, demo, forge and status subdomains), then `.env`
   variants. A useful 404 view separates scanner traffic from real broken links.
4. **The edge sees what the origin does not.** 175 of 2,284 were cache hits,
   which never reach the origin. Under `CACHE_MODE_USE_ORIGIN_HEADERS`, a
   `no-cache` response without `public` is never stored, so today only hashed
   assets, 301s and negatively cached errors are edge hits. The log entries'
   `cacheDecision` shows `RESPONSE_CACHE_CONTROL_NO_CACHE`. The origin's own
   counts (`ingress_red.go`) are therefore low for asset-heavy sites, and would
   be far lower if HTML were ever made edge-cacheable.
5. **The log has no request headers.** A load-balancer log entry carries the
   URL, status, sizes, latency, user agent, referer and remote IP. It has no
   `Accept` and no response headers, and no optional log field adds them (the
   optional fields are TLS, ORCA, service-extension and cache-policy-source
   fields). So the edge log cannot say which representation B served; the
   origin has to (section 5).

### The experience

A site owner, signed in (monitoring requires sign-in; start-anywhere decision
2):

```text
$ forge obs traffic prod --since 7d
SITE  REQUESTS  VISITORS*  CACHE HIT  4xx    5xx  BYTES
web   41,220    —          38%        1,410  0    1.9 GiB

TOP PAGES            REQUESTS  HUMANS  AGENTS  CRAWLERS
/                    6,112     4,980   402     730
/features            2,014     1,230   418     366
/llms.txt            97        0       61      36
...

$ forge obs traffic prod --by agent --class ai
AGENT          OPERATOR    CLASS      VERIFIED  REQUESTS  .md  llms.txt  TOP PAGE
ClaudeBot      Anthropic   ai_crawler yes       443       15   2         /features
PerplexityBot  Perplexity  ai_search  yes       216       15   0         /
ChatGPT-User   OpenAI      ai_user    yes       173       0    0         /pricing
GPTBot         OpenAI      ai_crawler no        87        0    0         /.env
...
```

(`VISITORS*`: edge logs carry no visitor identity; unique humans stay the
browser SDK's number, shown beside it in the console, never invented here.)

The console's static-site views get the same numbers. Today the env Overview
(`reliant/frontends/web/src/components/Forge/EnvPage/EnvTabPanels.tsx:112`,
`OverviewTab`) shows state, domains and promotions, and the browser numbers
live in Analytics (`.../Forge/Analytics/AnalyticsPage.tsx:49`) and Speed
Insights (`.../Forge/Analytics/SpeedInsightsPage.tsx:92-117`), all from the
browser SDK. An agent never runs the SDK and a cache hit never reaches the
origin, so none of those views can show either.

For B, a site author adds one line to an env's frontend, deploys, and:

```text
$ curl -sI -H 'Accept: text/markdown' https://reliantlabs.io/features
content-type: text/markdown; charset=utf-8
content-location: /features.md
vary: Accept, Accept-Encoding
link: </features>; rel="canonical"

$ curl -sI https://reliantlabs.io/features          # a browser's Accept
content-type: text/html; charset=utf-8
vary: Accept, Accept-Encoding
link: </features.md>; rel="alternate"; type="text/markdown"
```

## 2. A: edge request logs

### Where the data comes from

The edge already publishes a slice of its log. Phase 11 of `provision.sh`
creates a Cloud Logging sink whose filter is `resource.type="http_load_balancer"
AND url_map_name="static-sites-cdn" AND httpRequest.cacheHit=true`
(`edge_log_filter`, `provision.sh:3519-3524`) into the Pub/Sub topic
`static-sites-edge-hits`, read by the pull subscription
`static-sites-edge-hits-metering` (`provision.sh:184-190`). The consumer,
`internal/staticedge`, bills the bytes the edge serves from cache, exactly once,
with a receipt per `(insertId, timestamp)` (`internal/staticedge/meter.go:1-31`),
and resolves host to org and env through the host index.

So the hard parts already exist: the log export, a Pub/Sub consumer in cp, and
host-to-owner resolution. What is missing is every request (misses included),
the fields billing ignores, and a writer into the tenant.

### Pipeline (proposed)

```text
Cloud CDN / ALB  ──log──▶  Cloud Logging
                             │ sink static-sites-edge-requests
                             │   filter: url_map_name="static-sites-cdn"
                             │           AND NOT httpRequest.requestUrl:"_fprobe="
                             │           AND NOT httpRequest.requestUrl:"/_otel/"
                             ▼
                  Pub/Sub topic static-sites-edge-requests
                             │ pull subscription …-telemetry
                             ▼
       cp worker static_edge_telemetry (new; beside static_edge_metering)
         1. parse entry         (staticedge.ParseEntry, widened: status, latency,
                                 method, protocol, UA, referer, cacheId,
                                 statusDetails, remoteIp)
         2. host → org/env/site (host index: hosts/<host>/site.json →
                                 sites/<org>/<env>/<site>, staticorigin
                                 index.go:196-218; the meter's owner cache)
         3. drop: unowned host, env opted out (serving.json edgeLogs=false)
         4. classify UA         (forge pkg/agentua, section 2 below)
         5. verify agent        (operator-published IP ranges, cached)
         6. truncate IP, drop query string, reduce referer to origin+path
         7. batch per (org, env) → OTLP logs, reliant.source=edge,
            to the gateway's otlp/env with the env's telemetry token
                             ▼
       gateway (deploy/kcl/lib/observability/gateway.k): stamps org/env from
       the token, quota, routing connector sends source=edge to the landing
       table ingest.edge_requests, whose per-tenant view writes
       t_<org>.edge_requests (and the 1d rollup)
```

Why each choice:

- **A second sink, not a wider first one.** The metering sink's filter is a
  billing contract, and `ParseEntry` rejects non-hits on purpose. Widening it
  would make the meter read ten times more and couple billing to telemetry.
  A second sink costs one Pub/Sub topic and its message volume, and keeps the
  two failure domains apart: a telemetry backlog never delays a cap
  enforcement.
- **Through the gateway, with the env's token.** `internal/uptimeprobe/otlp.go:40-46`
  is the model: "the token is the only thing that decides where a sample
  lands". The tenant boundary is the per-org database (`t_<suffix>`,
  `internal/obstenant/names.go:50-52`; `forge/pkg/obsquery/obsquery.go:5-16`:
  "the database, not this package, is the org boundary"), and the gateway
  already enforces it, meters ingest, applies quota and drops unattributed
  rows (the pipeline in `gateway.k`'s header). A worker writing ClickHouse directly would
  re-implement all four.
- **A source stamp the client cannot forge.** The gateway deletes client-sent
  `reliant.*` attributes (`gateway.k`, `otlp/env`). The `edge` source is stamped from
  a signed token claim, the way the release prober's `probe` source is
  (`internal/obsmeter/source.go:112-120`).
- **Its own table, not `otel_logs`.** Crawler traffic in `otel_logs` would
  flood `forge obs logs` and the issue grouping, and live under the app's log
  retention. A dedicated landing table and tenant table keep it out of every
  existing query and give it its own TTL. Precedents for a derived tenant table
  are migration `0009_openbao_audit.sql` (its own 400-day TTL) and
  `0028_product_events_edge_columns.sql` (edge facts as columns).
- **At least once.** The worker acks a Pub/Sub batch after the gateway accepts
  it. A crash between the two redelivers a batch; rows carry `InsertId`, and the
  panels count with `uniqExact(InsertId)` where exactness matters. Billing-grade
  exactly-once (the meter's receipt table) is not worth a Postgres write per
  request for telemetry.
- **Probes and beacons excluded at the sink.** Release-health probes carry
  `?_fprobe=` and are not the owner's traffic (`internal/staticorigin/probe.go:12-53`).
  The browser SDK's `/_otel/v1/*` posts are already telemetry.

### The tenant table (proposed; tenant migration `0035`)

| Column                              | From                                                                                                             |
| ----------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| `Timestamp`, `InsertId`             | the log entry                                                                                                    |
| `EnvId`, `Site`, `ServiceName`      | host index; `ServiceName` is `naming.FrontendServiceName`, the browser SDK's own name                            |
| `Host`, `Path`                      | `requestUrl`, query string dropped (it can carry tokens); `HasQuery` bool kept                                   |
| `Method`, `Status`, `Protocol`      | `httpRequest`                                                                                                    |
| `CacheStatus`                       | `hit`, `validated`, `miss`, `uncacheable`, from `cacheHit`, `cacheValidatedWithOriginServer` and `statusDetails` |
| `CachePop`                          | `jsonPayload.cacheId` (e.g. `IAD`)                                                                               |
| `Bytes`, `LatencyMs`                | `responseSize`, `latency`                                                                                        |
| `RefererHost`, `RefererPath`        | `referer`, query dropped                                                                                         |
| `UserAgent`                         | raw, so a later classifier can reclassify old rows                                                               |
| `UaClass`, `Agent`, `AgentOperator` | `pkg/agentua`                                                                                                    |
| `AgentVerified`                     | IP in the operator's published ranges (section below)                                                            |
| `ClassifierVersion`                 | the agent list's version                                                                                         |
| `ContentKind`                       | `page`, `markdown`, `llms`, `robots`, `sitemap`, `asset`, `other`, from the path                                 |
| `ClientNet`                         | IPv4 /24 or IPv6 /48 (decision 6); the full IP is never stored                                                   |
| `Country`                           | from the IP at ingest, when cp has a GeoIP source (decision 6)                                                   |
| `SampleWeight`                      | 1 today; a quota-degraded batch writes the weight so `sum(SampleWeight)` stays right                             |

A daily rollup, `edge_requests_1d`, keyed by `(EnvId, Site, day, Path,
Status, UaClass, Agent, AgentVerified, CacheStatus)` with request and byte sums,
is what the long ranges and the console read.

### Classifying user agents

Two layers:

1. **Generic.** cp already classifies browser-telemetry user agents with
   `uap-go` and has a `bot` device type (cp `internal/proxy/otel_useragent.go:172-182`:
   uap-core's `Spider` device, or a family naming a bot). It separates browsers
   from bots, and that is all.
2. **Named agents.** Nothing in forge, cp or reliant names an AI agent today. A
   new forge package, `pkg/agentua`, embeds a maintained list
   (`agents.yaml`): each entry is a token to match, a name, an operator, a
   class, and the URL of the operator's published IP ranges where one exists.

The classes are what an owner acts on, since each answers a different
`robots.txt` question:

| `UaClass`        | Means                                                 | Examples (from the evidence)                   |
| ---------------- | ----------------------------------------------------- | ---------------------------------------------- |
| `browser`        | a person's browser                                    |                                                |
| `search_crawler` | builds a search index                                 | Googlebot, bingbot, Applebot                   |
| `ai_crawler`     | collects training data                                | GPTBot, ClaudeBot, CCBot, Bytespider           |
| `ai_search`      | indexes for an answer engine                          | OAI-SearchBot, Claude-SearchBot, PerplexityBot |
| `ai_user`        | fetches a page because a person asked an assistant to | ChatGPT-User, Claude-User, Perplexity-User     |
| `tool`           | a library or CLI                                      | curl, python-requests, Go-http-client          |
| `monitor`        | uptime and link checkers                              |                                                |
| `other_bot`      | a self-declared bot not on the list                   |                                                |
| `unknown`        | nothing matched                                       |                                                |

`AgentVerified` is true only when the request's IP is in the ranges the
operator publishes (OpenAI, Google, Bing, Perplexity and Apple publish JSON
range lists). The worker refreshes them daily and caches them. An agent whose
operator publishes no ranges is `AgentVerified = false` with the name kept, so
the panel says "claims to be" rather than nothing. The evidence's 682 secret
probes would all have been unverified.

**Why forge, not cp.** The list is a vocabulary that dashboards, `forge obs`
and self-managed users share. In forge it ships with forge releases, a
self-managed site can classify its own CDN logs with it, and cp imports it
through `go.mod` like every other `pkg/`. The cost is a release cycle for a new
agent; the `ClassifierVersion` column and the raw `UserAgent` let a query
reclassify old rows with the newer list. Decision 3.

### Billing, retention and opt-out

- **Billing.** Rows count as observability ingest, like any other signal: cp
  meters bytes written per tenant table from ClickHouse's `system.part_log`
  (`internal/obsmeter/contract.go:9-30`), with 10 GiB a month included and
  10¢/GiB after (`internal/plansconfig/pricing.yaml:463-465`). The alternative is
  to add `edge` to `OperatorSources`, the unbilled platform sources (`daemon`,
  `platform`, `gateway`, `ingress`, `probe`; `internal/obsmeter/source.go:120`).
  Size: a row is roughly 400 bytes uncompressed with a ~110-byte user agent, so
  a site with a million requests a month writes about 0.4 GiB. The ticket asks
  for ingest billing. Decision 4.
- **Quota.** The gateway's `reliantquota` degrades rather than blocks. A crawler
  storm on a site near its cap is sampled, and `SampleWeight` keeps the counts
  right.
- **Retention.** Raw rows follow a new `ObservabilityRetention.edge_days`
  (default: the env's info-log days); the daily rollup follows
  `product_events_retention_days` (90 days free, 400 paid;
  `pricing.yaml:468`). Decision 5.
- **Opt-out.** A new env-wide signal beside `browser`:
  `forge.ObservabilitySignals {edge = False}` (forge `kcl/schema.k:7427`,
  `ObservabilitySignals`; `browser` at `:7450`). The browser switch works by
  rendering `OTEL_ENDPOINT=off` into `config.js` (`kcl/render.k:3571-3582`);
  edge logs never pass through `config.js`, so the render writes `edgeLogs:
false` into each hosted static frontend's StaticSite spec, the operator copies
  it into `serving.json`, and the worker drops the env's rows before they leave
  cp. `observability = forge.Observability {enabled = False}` turns it off too.
  `edge` defaults to on, matching every signal but replay. Because an absent
  field means on, existing sites start logging when the cp slice ships,
  whatever forge they pin, and opting out needs the forge release that has the
  field. Decision 2.

### Views

- **`forge obs traffic <env>`** (new verb). Defaults to totals and top pages;
  `--by page|status|agent|referrer|country`, `--class <UaClass>`,
  `--site <frontend>`, `--since`, `--json`. It follows the `obs sessions`
  chain end to end (forge `internal/cli/obs_sessions.go:73-160` →
  `internal/obsbackend/contract.go:52` → `cloud_sessions.go:101-122` → cp
  `RumService/ListSessions`, `internal/handlers/rum/rpc_list_sessions.go:43-104`
  → `pkg/obsquery/rum_sessions.go:298`): a `pkg/obsquery` compiler
  (`TrafficQuery`), an `obsbackend` method (the local backend answers "edge logs
  exist only on Reliant hosting", since `env up dev` has no CDN), a cp RPC under
  `obs:read`, and the verb. One verb with `--by agent`, not a separate
  `obs bots`: the filters are the same.
- **`forge obs query`.** `edge_requests` and `edge_requests_1d` join the
  documented tables (raw SQL already works on any tenant table;
  `internal/cli/obs_query.go:56-80`), with `{env_ids}` bound as everywhere.
- **Dashboard.** Two groups join the code-derived `frontend` dashboard (forge
  `internal/obsdash/spec.go:738`) when the env has a hosted static frontend:
  **Traffic** (requests by class over time, top pages, 404s split into
  scanner-shaped and real, cache-hit ratio, bytes) and **Bots & agents**
  (requests by agent and class, verified versus claimed, which agents fetched
  `llms.txt`, `.md` twins and `robots.txt`, top pages per agent, and, with B,
  Markdown served by negotiation). Panels are SQL, like
  `pkg/obsquery/uptime.go`, so the console template
  (`internal/dashtemplates/templates/frontend.json`) and `obs dashboards`
  render the same thing.
- **Console.** A "Traffic" card on the env Overview for an env with a hosted
  static frontend (requests, cache-hit ratio, humans versus agents, a link to the
  dashboard) and a "Bots & agents" tab beside Analytics and Speed Insights,
  reading through `usePanelQuery` like Speed Insights does. Humans counted by the
  edge (`UaClass = browser`) and visitors counted by the SDK are labelled as
  different numbers, never merged.

## 3. B: Markdown content negotiation

### Declaration (proposed): a field on `forge.Frontend`

```kcl
_web = forge.Frontend {
    name = "web"
    type = "vite"
    public_dir = "dist"
    markdown_negotiation = True     # new; default False
}
```

- **On `Frontend`, not `OnHosted`.** `forge.OnHosted` (`kcl/workload.k:332`) is
  the runtime workloads bind too, and it has no site knobs on purpose
  (`internal/cli/deploy_dispatch.go:795-797`). `forge.Frontend`
  (`kcl/schema.k:4866`) already carries the site's serving facts (`base_path`,
  `asset_dir`, `runtime_config`).
- **Hosted only.** A frontend bound to `OnBucket`, `OnFirebase` or a dev runner
  with `markdown_negotiation = True` is refused at render with a message naming
  the runtime: those runtimes cannot vary on a header. A `check:` in the schema
  and a `negative_*.k` test hold it.
- **Opt-in, explicit.** No default and no scaffold adds it, and a test proves
  that an env without the field renders byte-identically (the status-page rule).
- **Why a spec field and not a build file.** Headers and redirects are files in
  the build (`_headers`, `_redirects`) because they are facts about the code,
  promoted with the digest (cp `internal/staticorigin/site_headers.go:15-38`).
  Which pages have twins is a fact about the code, and the operator reads it
  from the build itself (below). Whether to negotiate is a serving policy an
  owner may want in staging before prod, with a visible line in KCL and a
  capability gate. Decision 7 weighs a build file and "automatic when twins
  exist" against it.

It flows like `asset_dir`: forge's `hostedStaticSpec` (`internal/cli/deploy_dispatch.go:816-833`)
sets `markdownNegotiation: true` on `v1alpha1.StaticSiteSpec`
(`pkg/deploy/v1alpha1/staticsite_types.go:90`), which travels as the untyped
`spec` Struct of `DeployService.CreateDeployment` (cp
`proto/services/deploy/v1/deploy.proto:1760`), so no proto changes. cp decodes
it strictly (`internal/deployspec/spec.go:160-172`), so forge first checks a new
capability, `serves-markdown-negotiation`, beside
`CapabilityServesNamedSiteAPIs` (`pkg/release/bundle_features.go:313`), and an
older control plane refuses cleanly before anything is recorded. The
StaticSite operator writes it into the site's `serving.json`
(`internal/operators/staticsite/serving.go:58-59` → `staticorigin.Serving`,
`internal/staticorigin/serving.go:41-62`), which the origin already reads per
request with a 15-second cache (`serving.go:159-212`).

### Knowing a twin exists without a bucket read

The origin learns that a file exists only by reading it (`origin.go:362-381`,
up to three reads per page today), and nothing lists a release's files at
request time. One more read per page request to probe for a twin would add
latency to every HTML request of an opted-in site.

So the operator, which unpacks every file of the release layer when it
publishes, writes the twin set into `serving.json` beside the flag:

```json
{
  "markdown": {
    "negotiate": true,
    "twins": ["/index.md", "/features.md", "/blog/launch.md"]
  }
}
```

The set is bounded (one entry per prerendered page; reliantlabs.io has 8), and
the operator caps it (10,000 paths); a larger site negotiates for the first
10,000 and the operator reports the cap on the StaticSite status.

### The twin of a page

The origin resolves `/x` to a file first (`candidates`, `origin.go:619-638`:
`x`, then `x.html`, then `x/index.html`). Its twin:

| Resolved file  | Twin, first that is in the set |
| -------------- | ------------------------------ |
| `P.html`       | `P.md`                         |
| `D/index.html` | `D/index.md`, then `D.md`      |
| anything else  | none                           |

So `/` serves `/index.md`, `/features` serves `/features.md`, and the SPA
fallback, a site's `404.html`, redirects and non-HTML files never negotiate.

### Choosing the representation

Serve the twin only when all hold:

1. The site's `serving.json` has `markdown.negotiate`.
2. The request is `GET` or `HEAD`, and the resolved file is a page with a twin.
3. `Accept` names `text/markdown` **explicitly** with a q-value greater than the
   one `text/html` gets (explicitly or through `text/*` / `*/*`). Ties go to
   HTML. `*/*` alone, `text/*` alone and a missing `Accept` get HTML.

A browser sends `text/html,…,*/*;q=0.8`, so it always gets HTML; a test pins
the `Accept` strings of current Chrome, Firefox and Safari, and `curl`'s
default. `text/markdown, text/html;q=0.9` gets Markdown. An agent that sends
only `text/markdown` for a page without a twin gets HTML, never a 406: a page is
always better than an error.

### Headers

On every response for a page with a twin, of an opted-in site, whichever
representation it carries:

- `Vary: Accept` (added beside `Accept-Encoding`). It is set by the origin;
  sites cannot set `Vary` themselves (it is a platform header,
  `site_headers.go:70-82`).

On the HTML representation:

- `Link: </features.md>; rel="alternate"; type="text/markdown"`, the header
  twin of the `<link>` element, for agents that read headers before bodies.

On the Markdown representation:

- `Content-Type: text/markdown; charset=utf-8` (what `/features.md` already
  serves; verified live on 2026-11-26).
- `Content-Location: /features.md`, so a cache or agent knows the
  representation has its own URL.
- `Link: </features>; rel="canonical"`, so a search engine that indexes the
  Markdown credits the page.
- Its own `ETag`: the twin object's, which differs from the HTML object's, with
  the `-gzip` suffix rule applied as for any object (`origin.go:865-874`). A
  conditional request is answered for the representation it would get.
- The same `Cache-Control` as the page.

A site that has not opted in, and every page without a twin, keeps today's
headers byte for byte.

### The CDN cache

Cache-hit requests never reach the origin, so negotiation is only correct if
the edge can never hand one representation to a client that asked for the
other. Cloud CDN's rules (cloud.google.com/cdn/docs/caching):

- A response whose `Vary` names only `Accept`, `Accept-Encoding`, `Origin`,
  `Sec-Fetch-*` and a few others **is cached, per value of those headers**. Any
  other `Vary` value makes it uncacheable.
- `cacheKeyPolicy.includeHttpHeaders` **refuses `Accept`** (and `User-Agent`,
  `Cookie`, `Host` and others), so a normalized `Accept` cannot be put in the
  key.
- Custom request headers take only the load balancer's variables
  (`{client_region}`, `{tls_ja3_fingerprint}`, …), none derived from `Accept`.

Today the question is latent: HTML is served `no-cache` without `public`
(`internal/staticorigin/headers.go:41-58`), which Cloud CDN under
`USE_ORIGIN_HEADERS` does not store, so every page and twin request reaches
the origin (the evidence's `RESPONSE_CACHE_CONTROL_NO_CACHE`). It stops being
latent the day pages are made edge-cacheable (`public, no-cache` revalidation,
or a short `max-age`), and browsers and other shared caches obey `Vary` too.
So `Vary: Accept` ships with B from the first slice, and provisioning changes
nothing: no cache-key change (the key stays the full URI by design,
`provision.sh:3459-3490`; a key change re-keys every site's cache) and no url
map rule. The cost if pages become edge-cacheable later is fragmentation by
raw `Accept` string, on opted-in pages only; browsers send a handful of
distinct strings, so the hit ratio suffers little.

### What it fixes on the side

- `.md` joins `staticFileExtensions` (`internal/staticorigin/route_shape.go:28-41`)
  and its forge mirror (`internal/deploytarget/firebase_serving.go:44`), and the
  uploader's `contentTypeFor` (`internal/operators/staticsite/release.go:397`)
  names `text/markdown; charset=utf-8` explicitly instead of relying on the
  origin's fallback (`origin.go:844-853`). Today `/x.md` is route-shaped, so a
  missing twin is tried as `x.md.html` and `x.md/index.html` and can get the SPA
  shell with a 200. After, a missing `.md` is a 404, like any missing file with
  an extension. This changes a response for every site, from a wrong 200 to a
  correct 404; it is a serving fix, and the slice says so in its title.

### Measuring it

The edge log cannot tell which representation went out (it has no request or
response headers). The origin can: every negotiable request reaches it while
pages are `no-cache`. The origin's ingress metrics (`reliant.ingress.requests`,
`ingress_red.go`) gain a `representation` attribute (`html`, `markdown`) on
negotiable paths, and the "Bots & agents" group shows "Markdown served by
negotiation" from it, joined to edge rows by site and path. If pages ever become
edge-cacheable, the same panel undercounts cache hits, and says so.

## 4. Control plane changes, in one place

| Area                                     | Change                                                                                                                                            |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `deploy/gcp/provision.sh`                | Phase 11: sink, topic and subscription `static-sites-edge-requests`, beside the metering trio; drift-checked like them.                           |
| `internal/staticedge`                    | `ParseEntry` widened (or a sibling parser) to every field in section 2; the meter keeps rejecting non-hits.                                       |
| `internal/workers/`                      | `static_edge_telemetry`: pull, resolve, classify, verify, batch, send, ack.                                                                       |
| `internal/obsmeter`                      | The signed `edge` source; whether it is billed (decision 4).                                                                                      |
| `deploy/kcl/lib/observability/gateway.k` | A route for `reliant.source=edge` to `ingest.edge_requests`.                                                                                      |
| `internal/obstenant/schema`              | Landing table in `001_ingest_landing.sql`; tenant migration `0035_edge_requests.sql` (table, rollup, views), mirrored in `010_tenant_create.sql`. |
| `internal/obstenant`                     | `edge_days` in the retention sync.                                                                                                                |
| `internal/operators/staticsite`          | `serving.json` gains `edgeLogs` and `markdown{negotiate, twins}`; `contentTypeFor` names `.md`.                                                   |
| `internal/staticorigin`                  | Negotiation in `serveContent`, `Vary`/`Link`/`Content-Location`, `.md` in `staticFileExtensions`.                                                 |
| `internal/proxy/ingress_red.go`          | The `representation` attribute.                                                                                                                   |
| A new RPC under `obs:read`               | Backs `forge obs traffic` (beside `RumService`, or in the observability service).                                                                 |

## 5. Forge changes, in one place

| Area                                                                 | Change                                                                                                                                         |
| -------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `kcl/schema.k`                                                       | `Frontend.markdown_negotiation`, `ObservabilitySignals.edge`, `ObservabilityRetention.edge_days`, with `check:`s.                              |
| `kcl/render.k`, `internal/cli/deploy_dispatch.go`                    | Render both into the StaticSite spec; capability gate.                                                                                         |
| `pkg/deploy/v1alpha1/staticsite_types.go`                            | `markdownNegotiation`, `edgeLogs` (`omitempty`, so an older spec decodes unchanged).                                                           |
| `pkg/release/bundle_features.go`                                     | `serves-markdown-negotiation`, `honors-edge-log-opt-out`.                                                                                      |
| `pkg/agentua` (new)                                                  | The agent list, the classifier, range verification helpers.                                                                                    |
| `pkg/obsquery`, `internal/obsbackend`, `internal/cli/obs_traffic.go` | `TrafficQuery`, backends, the verb.                                                                                                            |
| `internal/obsdash`                                                   | Traffic and Bots & agents groups.                                                                                                              |
| `internal/deploytarget/firebase_serving.go`                          | `.md` in the extension list.                                                                                                                   |
| Skills                                                               | `deploy/static-site` (and its `serving` sub-skill: negotiation, twins, headers), `deploy/hosting`, `observability` (edge logs, `obs traffic`). |
| `internal/benefits/benefits.yaml`                                    | A row each, rendered into `docs/benefits.md`; a limit: edge logs and negotiation are Reliant hosting only.                                     |

No lint rule is added. If one is later (for example, warning that
`markdown_negotiation` is set but the build ships no `.md` twin), it starts as
a warning with a row in `internal/cli/lint/lint_rollout_table.go`.

## 6. Tests

- **Origin (B)**, extending `internal/staticorigin/origin_test.go` and
  `serving_test.go` (`newServedOrigin`, `memObjects`): browsers' `Accept`
  strings get HTML with `Vary: Accept` and `Link`; `text/markdown` gets the
  twin with its headers; q-value ties, `*/*`, `text/*`, missing `Accept`;
  `HEAD`; `If-None-Match` per representation; `D/index.md` versus `D.md`;
  `base_path`; a custom host; SPA fallback, `404.html`, redirects and assets
  never negotiate; **an opted-out site and a page without a twin produce
  byte-identical headers to today** (golden); no extra object read happens
  (`memObjects.reads`).
- **Operator**: the twin set from a release layer; the cap; `serving.json`
  round trip; a release without twins writes no `markdown` block.
- **forge**: `closedschema_*` and `negative_*` KCL tests (field on a
  non-hosted runtime; `edge_days` bounds); a render test that an env without
  either field renders byte-identically; the capability gate refuses an older
  control plane with a named message; `v1alpha1` decode compatibility.
- **`pkg/agentua`**: a table of real user agents from the evidence, including
  the spoofed ones; every list entry has a class and operator; a token matches
  only its agent; ranges parse.
- **Worker (A)**: entry parsing from recorded log entries; host resolution for
  platform and custom hosts; unowned hosts and opted-out envs dropped; IP
  truncation and query stripping (a test fails if a raw IP or a query string
  reaches the OTLP payload); redelivery; the gateway route lands rows in the
  right tenant and no other (the cross-tenant test the gateway already runs,
  extended).
- **Panels**: the obsdash SQL compiles against the tenant schema, like the
  existing frontend panels (`internal/obsdash/frontend_panels_test.go`).

## 7. Alternatives considered

| Alternative                                                                   | Why not                                                                                                                                                                                    |
| ----------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **A from origin logs only** (log in `staticorigin`, nothing from the CDN)     | Misses every cache hit, which is most asset traffic and would be most page traffic if pages became edge-cacheable. The ticket exists because the origin's view is incomplete.              |
| **Origin logs for misses plus CDN logs for hits**                             | Two sources to join and deduplicate for no gain: the edge log already has every request, misses included. The origin keeps what only it knows (the representation), as a metric attribute. |
| **Widen the metering sink and share the topic**                               | Couples billing to telemetry; the meter would parse ten times the entries; a telemetry backlog could delay cap enforcement.                                                                |
| **Write ClickHouse directly from the worker**                                 | Re-implements tenant routing, ingest metering, quota and attribution checks the gateway already enforces.                                                                                  |
| **Rows in `otel_logs`**                                                       | Floods `obs logs` and issue grouping with crawler traffic and ties edge retention to app logs.                                                                                             |
| **Metrics only (rollups, no rows)**                                           | Cheaper, but loses per-path and per-agent detail, which is the point of "which agents read which pages".                                                                                   |
| **Export to the customer's own Cloud Logging or bucket**                      | Not every customer has GCP; it skips `forge obs`, the console and agents; it puts a Reliant project's IAM in the customer's way.                                                           |
| **B as a url map route rule** (`headerMatches` on `Accept`, rewrite to `.md`) | The url map is shared by every site; per-site opt-in would mean a host rule per site against the url map's limits, and a twin's existence is not known at the load balancer.               |
| **B with `Accept` in the cache key**                                          | Cloud CDN refuses `Accept` in `includeHttpHeaders`.                                                                                                                                        |
| **B as a redirect** (`303` to `/x.md`)                                        | Costs agents a round trip, and many agent fetchers do not follow redirects across content types. Content negotiation is what `Accept` is for.                                              |
| **B automatic for every site with twins**                                     | Changes responses (adds `Vary: Accept`) for sites that never asked, and the brief requires opt-in. Kept as decision 7's option (b) for later.                                              |

## 8. Owner decisions (decided 2026-11-26)

**All settled (owner, 2026-11-26): each row is decided on its recommended
option** ("take your recommendations on all"), recorded as the
[Settled](#settled-owner-2026-11-26) list. The table keeps the options that
were weighed, as the record of why. Reopening a row is the owner's call.

| #   | Decision                           | Options                                                                                                                                                                                                                                | Decided (owner, 2026-11-26)                                                                                                                                                                                                              | Blocked                                    |
| --- | ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------ |
| 1   | Where edge request data comes from | (a) CDN request log, second sink → Pub/Sub → cp worker → gateway; (b) origin logs for misses plus CDN logs for hits; (c) origin logs only.                                                                                             | (a). The edge log is the only complete source, and the export, consumer and host resolution already exist for metering.                                                                                                                  | Slices 1 and 2.                            |
| 2   | Default and opt-out                | (a) on by default, `ObservabilitySignals.edge = False` per env opts out; (b) off by default, opt in per env; (c) per frontend rather than per env.                                                                                     | (a), as the ticket says. Every signal but replay defaults on, and the data holds no visitor identity. Note it starts for existing sites when the cp slice ships; announce it in the release notes.                                       | Slice 1 (schema), slice 2 (worker filter). |
| 3   | Where the agent list lives         | (a) forge `pkg/agentua`, embedded YAML, shipped by forge releases; (b) cp-internal, updated without a forge release; (c) a third-party list (e.g. a community robots list) vendored.                                                   | (a). One vocabulary for cp, `forge obs`, dashboards and self-managed users; the raw user agent and `ClassifierVersion` let queries reclassify old rows. (c) can seed it, with its licence checked.                                       | Slice 1.                                   |
| 4   | Billing                            | (a) observability ingest, like any signal (included 10 GiB, then 10¢/GiB); (b) unbilled platform source like `ingress` and `probe`; (c) unbilled rollup, billed raw rows.                                                              | (a), as the ticket says. About 0.4 GiB per million requests; the opt-out and the spend cap bound it. (c) is the fallback if crawler-heavy free sites object.                                                                             | Slice 2 (`obsmeter` source).               |
| 5   | Retention                          | (a) raw rows on a new `edge_days` (default the env's info-log days), daily rollup on the product-events retention (90 free / 400 paid); (b) raw rows only, logs retention; (c) rollup only.                                            | (a). Raw rows answer "what happened this week"; the rollup answers "how has agent traffic grown this year" cheaply.                                                                                                                      | Slice 2 (migration), slice 1 (schema).     |
| 6   | Client address and location        | (a) store a truncated network (/24, /48) and a country, never the full IP; (b) store nothing derived from the IP; (c) store the full IP.                                                                                               | (a). The full IP is needed only at ingest, for agent verification; a truncated network is enough to spot a scanner. The country needs a GeoIP source in cp; until there is one the column is empty.                                      | Slice 2.                                   |
| 7   | How B is declared                  | (a) `markdown_negotiation = True` on a hosted `forge.Frontend`, twins detected from the build; (b) automatic for every hosted site whose build ships twins; (c) a build file (e.g. a line in `_headers` or a `_forge/negotiate` file). | (a). Opt-in is required, a KCL line is visible and can differ per env for a staging trial, and the capability gate gives older control planes a clean refusal. (c) fits the "facts about the code" rule but hides a policy in a dotfile. | Slices 3 and 4.                            |
| 8   | Who decides at serve time          | (a) the origin, with `Vary: Accept`, no CDN change; (b) a url map route rule per opted-in host; (c) make pages edge-cacheable and key on a normalized header.                                                                          | (a). Cloud CDN refuses `Accept` in the key and the url map is shared; the origin already sees every page request.                                                                                                                        | Slice 3.                                   |
| 9   | `.md` serving fix for every site   | (a) ship it with B: `.md` is a static file extension, a missing twin is a 404; (b) only for opted-in sites; (c) leave it.                                                                                                              | (a). Today's 200 SPA shell for a missing `/x.md` is wrong for every site and costs agents a useless page.                                                                                                                                | Slice 3.                                   |

## 9. Rollout plan

The honest size is **M** for A (a sink, a worker, a tenant table, a verb and
panels) and **S** for B (an origin branch, an operator field, a KCL field).
They share no slice and can be built in parallel; B's measurement panel waits
for A's dashboard group. Each slice ships alone.

0. **Decisions** 1 to 9 settled by the owner (done, 2026-11-26).
1. **forge: schema and vocabulary.** `ObservabilitySignals.edge`,
   `ObservabilityRetention.edge_days`, their render into the StaticSite spec
   behind `honors-edge-log-opt-out`; `pkg/agentua` with its list and tests.
   An env without the fields renders byte-identically.
2. **cp: edge rows land.** Sink, topic and subscription; the
   `static_edge_telemetry` worker; the `edge` source; the gateway route; tenant
   migration `0035`; retention sync. Dogfood: reliantlabs.io's prod env shows
   rows in `forge obs query prod 'SELECT … FROM edge_requests'`, and the
   7-day agent table above is reproduced from the tenant, without `gcloud`.
3. **cp: negotiation at the origin.** `serving.json`'s `markdown` block from
   the operator, the origin branch and headers, `.md` as a static extension and
   content type, the `representation` attribute. Inert until a site opts in.
4. **forge: `markdown_negotiation`.** The field, the hosted-only check, the
   capability gate, the skill. Dogfood: the landing page's prod `web` frontend
   sets it; `curl -H 'Accept: text/markdown' https://reliantlabs.io/features`
   returns the twin, and a browser sees no change (the landing page is deployed
   by its owner, per its repo rules).
5. **Views.** `forge obs traffic`, the dashboard's Traffic and Bots & agents
   groups, `obs query` docs; then the console's Overview card and Bots & agents
   tab. Dogfood: the owner's question ("does ClaudeBot read our twins and
   `llms.txt`?") is answered from the console.
6. **Docs and limits.** `benefits.yaml` rows, the compare skill (edge logs and
   negotiation are hosting-only; a self-managed CDN keeps its own logs), and the
   release-notes line for decision 2.
