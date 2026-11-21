<!-- Generated from internal/benefits/benefits.yaml (reasons) by `task docs:benefits`; edit the YAML, not this file.

How this page stays true:

- Every reason cites the benefits that prove it, and Validate
  (internal/benefits) refuses an unknown or in-progress one, a reason whose
  backing spans one category, and a headline benefit no reason cites.
- TestBenefitsEvidenceExists (internal/cli) resolves every cited benefit's
  evidence against the forge binary and this repository.
- TestRenderedFilesAreCurrent (internal/benefits) fails while this page
  differs from a fresh render.
-->

# Why Forge

Most stacks are stitched together: a framework from one place, CI, hosting,
logs, alerts and secrets from others. None of them knows what the others
know, so every integration is glue you write, and every agent working in
the repo works blind. Forge reads your services, RPCs, schema,
environments and releases from files you own, and builds each of its
tools on that one model. The reasons below are what that buys you.

Each reason names the benefits that back it. Those live in
[benefits.md](benefits.md), where a test resolves every piece of evidence
against the forge binary and this repository, so a reason cannot claim
what forge does not ship. A reason marked "Reliant hosting" needs a
project deployed there; every other one is open-source Forge on any
infrastructure, and a cited benefit that needs more says so beside it.
`forge why` prints the same list in a terminal, and what Forge does not do
is [at the end](#limits).

## Contents

1. [Forge knows your whole app, so its tools share one model of it](#knows-your-app)
2. [Every RPC traced, measured and logged with no instrumentation code](#rpc-observability)
3. [Each release judged by its own telemetry and tied to the errors it causes](#release-health)
4. [Build once and promote the same bytes from staging to prod](#build-once-promote)
5. [Database migrations checked against the running release before they ship](#safe-migrations)
6. [Lint that knows your architecture, not just your syntax](#architecture-lint)
7. [Secure by default, with auth that fails closed and secrets out of git](#secure-defaults)
8. [Built for AI agents, with a project that explains itself and gates that check the work](#built-for-agents)
9. [Tests against a real Postgres that still run in the fast tier](#real-fast-tests)
10. [Production from the first commit, with the whole stack on your laptop](#prod-from-day-0)
11. [Adopt it piece by piece in any repo, and keep the tools you already run](#start-anywhere)
12. [Reliant hosting wires Postgres, secrets, telemetry and domains from your declarations](#hosting-knows-your-app)
13. [What Forge does not do](#limits)

---

<a id="knows-your-app"></a>

## Forge knows your whole app, so its tools share one model of it

A stack stitched from separate tools has no shared model of the app: the tracer does not know your RPCs, the deploy tool does not know your migrations, and the linter does not know your wiring, so the glue between them is code you write and keep in step. Forge reads the services, RPCs, tables, components and environments from files in the repo, and its codegen, lint, service catalog and deploys are all derived from that one declaration.

**How:** Because forge reads the proto, the applied migrations, the composition graph and the env declarations as its inputs, it can generate the handlers, ORM and hooks, check the wiring as a graph, and print where every symbol lives from the same facts, so none of them can disagree.

**See it:** `forge project shapes`

**Backed by:**

- [Proto in, full stack out](benefits.md#proto-to-everything)
- [SQL migrations are the schema](benefits.md#sql-first-schema)
- [Browsable API inventory](benefits.md#service-catalog)
- [Wiring checked as a graph](benefits.md#composition-graph-lint)
- [Deploy anywhere, declare once](benefits.md#workload-once-bind-per-env)
- [Find any symbol instantly](benefits.md#shapes-map)
- [Capabilities introspection](benefits.md#capabilities-introspection)

---

<a id="rpc-observability"></a>

## Every RPC traced, measured and logged with no instrumentation code

Hand instrumentation is the work that gets skipped, and the RPC that pages you is the one without a span. In a forge project every RPC is already one span, one metric sample and one log record with trace and request ids, every component call and ORM query is a child span, and the same signals run on your laptop and in production. Dashboards are generated from the deployed commands and the composition graph, and forge reports which services actually sent telemetry instead of assuming they did.

**How:** Because forge generates the RPC edge and holds a contract for every component, it can wrap each one in telemetry, derive dashboards from the composition graph, and name every place a request's trace is cut.

**See it:** `forge obs endpoints dev`

**Backed by:**

- [Every request traced and measured](benefits.md#edge-telemetry)
- [Package contracts enforced](benefits.md#contract-lint)
- [Broken-trace detection and fixes](benefits.md#lint-rules-tracing)
- [Dashboards derived from code](benefits.md#generated-dashboards)
- [Observability stack included](benefits.md#local-clickstack)
- [Query prod from the shell](benefits.md#obs-cli)
- [Proves telemetry actually arrives](benefits.md#telemetry-proven)
- [Trace trees with uninstrumented time](benefits.md#readable-traces)
- [Continuous CPU and heap profiling](benefits.md#continuous-profiling)
- [Profile-guided builds from production](benefits.md#profile-guided-builds)
- [Real-user sessions and replay](benefits.md#rum-sessions)

---

<a id="release-health"></a>

## Each release judged by its own telemetry and tied to the errors it causes

Most deploy tools stop at "the pods are healthy"; whether the release made errors or latency worse is a dashboard someone has to remember to open. Forge records every promotion, compares the window after it with the equal window before it, per service, against a policy declared in the env, and ties error issues to the release that introduced them. Every env gets default alerts for error rate, latency, regressed releases and more. On Reliant hosting the control plane judges every promotion on its own, and a declared health gate refuses a prod promote until staging holds a PASS.

**How:** Because forge both cuts the release and owns its telemetry, it can mark releases on dashboards, judge each promotion with the env's declared policy, and resolve an issue in the next release so that a later event from an older one counts as a regression.

**See it:** `forge obs check prod --wait`

**Backed by:**

- [Prove each deploy is good](benefits.md#obs-check)
- [Release health policy as code](benefits.md#release-health-policy)
- [Release markers on dashboards](benefits.md#release-markers)
- [Error issues tied to releases](benefits.md#issues-triage)
- [Release timeline per environment](benefits.md#release-ledger)
- [Production alerts out of the box](benefits.md#default-alerts)
- [Bad releases flagged automatically](benefits.md#release-regressed-alert) _(hosted)_
- [Prod gated on staging's verdict](benefits.md#release-health-gate) _(hosted)_

---

<a id="build-once-promote"></a>

## Build once and promote the same bytes from staging to prod

When each environment rebuilds, staging tested a different artifact than the one prod runs, and per-env config drifts in files nobody diffs. Forge records a release as an immutable set of artifact digests and promotes that record, declares every env as typed KCL over one shared base, and proves parity before its first deploy of an existing app. A deploy is a plan you approve and a compare-and-set against what it read, and it waits for health. Recovery is a new release, and a rollback floor refuses any deploy older than a line you draw.

**How:** Because forge builds the release, records it on a ledger and renders every env from the same declaration, it can promote by digest, show how each env would change, refuse a promote that raced a hotfix, and refuse a deploy below an env's rollback floor.

**See it:** `forge env diff prod`

**Backed by:**

- [Build once, promote by digest](benefits.md#build-once-promote-by-digest)
- [Runtime frontend config per env](benefits.md#frontend-runtime-config)
- [Environments as typed code](benefits.md#envs-in-kcl)
- [One env base, per-env deltas](benefits.md#shared-env-base)
- [Config errors caught before push](benefits.md#config-check-before-push)
- [Pre-deploy parity verification](benefits.md#env-verify-parity)
- [Diff your code against deployed](benefits.md#env-diff)
- [Deploy plans with binding approval](benefits.md#deploy-plan-approval)
- [Compare-and-set promotions](benefits.md#promote-cas)
- [Deploys wait, then smoke-test](benefits.md#deploy-waits-for-health)
- [Roll-forward recovery](benefits.md#roll-forward)
- [Rollback floors per environment](benefits.md#rollback-floors)

---

<a id="safe-migrations"></a>

## Database migrations checked against the running release before they ship

The migration that takes prod down is rarely wrong SQL; it is a lock queued behind a slow reader, a column the previous release still reads, or a file applied out of order. Every forge migration declares whether the previous release survives it, and lint refuses one that is unsafe or undeclared. Each statement can be measured for locks and rewrites on a disposable copy of a real Postgres, migrations apply under an advisory lock and a lock timeout before any new pod starts, and a release whose migrations the env's schema already passed is refused.

**How:** Because the migrations are the schema forge projects its ORM from, and forge cuts the release that carries them, it can lint each one against what a running release survives, apply them before the rollout, and raise a rollback floor when one declares that it breaks rollback.

**See it:** `forge lint --migration-safety`

**Backed by:**

- [SQL migrations are the schema](benefits.md#sql-first-schema)
- [Migration safety checks](benefits.md#migration-safety)
- [Measured migration locks and rewrites](benefits.md#migration-analysis)
- [Migration lock and statement timeouts](benefits.md#never-waiting-migrations)
- [Migrations gate every rollout](benefits.md#migrations-gate-rollout)
- [Out-of-order migrations refused](benefits.md#migration-release-guard)
- [Rollback floors per environment](benefits.md#rollback-floors)
- [Backups and point-in-time restore](benefits.md#managed-postgres-backups) _(hosted)_
- [Restores are drilled, not assumed](benefits.md#managed-postgres-restore-drill) _(hosted)_

---

<a id="architecture-lint"></a>

## Lint that knows your architecture, not just your syntax

A generic linter cannot tell that a read-only field is never written, that a list filter silently matches everything, that a component is wired but unreachable, or that an authenticated RPC never looks at who is calling. Forge's analyzers know those seams, and each rule names the bug class it prevents. They run beside golangci-lint, buf, eslint and the typecheck with one verdict, a lane that could not run fails instead of passing, and a rule new in a release warns before it gates.

**How:** Because forge generated the seams (entities, filters, the composition graph, the auth chain, the static export), it can check how your code uses them, and run the whole CI gate locally with one command.

**See it:** `forge lint`

**Backed by:**

- [Bug-class linters, one verdict](benefits.md#one-lint-gate)
- [The whole CI gate, locally](benefits.md#local-ci-gate)
- [Unwritten read-only field detection](benefits.md#unwritten-fields)
- [List filters that actually filter](benefits.md#list-filter-optional)
- [Wiring checked as a graph](benefits.md#composition-graph-lint)
- [Find rpcs that ignore the caller](benefits.md#unscoped-auth-audit)
- [Data-integrity rules](benefits.md#lint-rules-data-integrity)
- [Static-export breakage caught early](benefits.md#static-export-lint)
- [Breaking API changes gated](benefits.md#proto-breaking-gate)
- [Stale codegen can't merge](benefits.md#generated-drift)
- [Skipped lint lanes fail the gate](benefits.md#lint-skip-is-failure)
- [New rules warn before gating](benefits.md#lint-rollout-grace)

---

<a id="secure-defaults"></a>

## Secure by default, with auth that fails closed and secrets out of git

Security that must be switched on stays off. Every forge RPC needs a valid credential unless its proto opts out, so a forgotten annotation never publishes an endpoint; errors reach callers classified while the SQL and hosts stay in the server log; secrets are references bound per env, and a `.env` file fails lint. Images are signed with an SBOM, dependencies and images are scanned with expiring exemptions, and production reads go through an audited read-only command. What a caller may do stays your handler's code, and forge lists the RPCs that never ask who is calling.

**How:** Because forge owns the interceptor chain, the env render and the build, it can make the safe path the default one and lint for the rest: unscoped RPCs, secret keys nothing produces, and credential-shaped columns the read-only role could see.

**Backed by:**

- [Auth that fails closed](benefits.md#auth-fail-closed)
- [Errors never leak internals](benefits.md#svcerr-redaction)
- [Secrets never in git](benefits.md#secrets-model)
- [No .env files](benefits.md#no-dotenv)
- [Secrets wiring verified per env](benefits.md#secrets-model-lint)
- [Credential columns need a decision](benefits.md#db-grants-lint)
- [Find rpcs that ignore the caller](benefits.md#unscoped-auth-audit)
- [Dependency and image scanning](benefits.md#vuln-scan)
- [Signed images, SBOM and provenance](benefits.md#image-supply-chain)
- [Rate limits, idempotency, webhook checks](benefits.md#middleware-hardening)
- [Outbound SSRF guard](benefits.md#ssrf-guard)
- [Audited read-only prod queries](benefits.md#guarded-db-query)

---

<a id="built-for-agents"></a>

## Built for AI agents, with a project that explains itself and gates that check the work

An agent in an unfamiliar repo spends its tokens finding where things are and writing boilerplate, then guesses at conventions nobody wrote down. Forge ships version-matched skills inside the binary, prints its whole surface and the project's map in one call, answers in JSON with shared exit codes and suggested next commands, and generates the code an agent would otherwise type. The lint and test gates then catch what the agent got wrong before a person reviews it.

**How:** Because forge knows the layout, its own commands and every symbol's file and line, it can tell an agent where code goes and what to run next, over the CLI or MCP, from the same binary that enforces the rules.

**See it:** `forge project capabilities`

**Backed by:**

- [Skills that match your forge](benefits.md#skills-in-the-binary)
- [Built-in MCP server](benefits.md#mcp-server)
- [Hosted MCP connector](benefits.md#hosted-mcp) _(Reliant)_
- [Capabilities introspection](benefits.md#capabilities-introspection)
- [Find any symbol instantly](benefits.md#shapes-map)
- [Project state snapshot](benefits.md#project-audit)
- [JSON everywhere, with next steps](benefits.md#machine-readable-json)
- [Exit codes pipelines can branch on](benefits.md#exit-code-contract)
- [Predictable project layout](benefits.md#predictable-layout)
- [One marker, a whole entity](benefits.md#entity-from-one-marker)
- [A generated mock per interface](benefits.md#generated-mocks)
- [Bug-class linters, one verdict](benefits.md#one-lint-gate)

---

<a id="real-fast-tests"></a>

## Tests against a real Postgres that still run in the fast tier

Mock-heavy tests pass while production breaks, and tests against real dependencies get so slow that nobody runs them. Forge generates a recording mock for every package contract, runs database tests on a real embedded Postgres with each test on its own copy of a migrated template and no Docker, and writes a lifecycle test for every entity it scaffolds. A slow test must say so, so the fast tier stays fast, and a test whose inputs Go's cache cannot see may not replay a stale pass.

**How:** Because forge knows each package's contract and the applied schema, it can generate the mocks and the migrated test database, and lint the tests that would make the fast tier slow or its cache unsound.

**Backed by:**

- [A generated mock per interface](benefits.md#generated-mocks)
- [Real Postgres in every test](benefits.md#pgtest-real-postgres)
- [Unit, integration and e2e tiers](benefits.md#test-tiers)
- [CRUD tests come free](benefits.md#entity-lifecycle-tests)
- [Flow tests over real data](benefits.md#flow-tests-seedgraph)
- [Table-driven tests, batteries included](benefits.md#tdd-library)
- [Slow-test gating for the fast tier](benefits.md#slow-test-enforcement)
- [Go test cache soundness checks](benefits.md#cache-soundness)
- [Skipped-test detection in CI](benefits.md#skipped-pass-detector)
- [Test-hygiene rules](benefits.md#lint-rules-tests)
- [Test fixtures checked against schema](benefits.md#fixture-drift-lint)

---

<a id="prod-from-day-0"></a>

## Production from the first commit, with the whole stack on your laptop

A dev setup that differs from production hides the bugs that matter, and a shared dev database turns parallel work into collisions. `forge env up dev` builds and hot-reloads every service, worker and frontend with Postgres, a real sign-in, the observability stack and mail ready, and the scaffolded dev needs neither Docker nor a cluster. Every checkout gets its own ports and data, so ten agents run ten stacks, and every log, the browser console included, lands in one directory.

**How:** Because dev is an environment declared like prod, from the same workloads, forge can run it as host processes with the probes, sign-in and telemetry production gets, configured differently.

**See it:** `forge env up dev`

**Backed by:**

- [Production from the first commit](benefits.md#prod-from-day-0)
- [The whole stack, one command](benefits.md#env-up-dev)
- [A dev stack per worktree](benefits.md#per-worktree-devstacks)
- [Browser console logs on disk](benefits.md#browser-logs-on-disk)
- [Hot reload per workload](benefits.md#hot-reload)
- [Real sign-in locally, no setup](benefits.md#local-dev-idp)
- [Observability stack included](benefits.md#local-clickstack)
- [Probes and zero-drop shutdown](benefits.md#probes-graceful-shutdown)
- [Build UI before the backend](benefits.md#mock-mode)

---

<a id="start-anywhere"></a>

## Adopt it piece by piece in any repo, and keep the tools you already run

A framework that demands a rewrite only ever gets adopted on a new project. Forge adopts an existing app in any language without changing its code, and most of it works piece by piece: keep your deploy tool, migration tool, identity provider and telemetry backend, record the deploys your CI already runs, and take any generated file over for good. What forge does not do is written down beside what it does.

**How:** Because every part of forge reads a declaration rather than assuming its own stack, each part can be bound to what you already run: a deploy hook, your migration command, an OIDC issuer, any OTLP endpoint, or a cluster you operate.

**See it:** `forge project benefits`

**Backed by:**

- [Adopt an existing app](benefits.md#adopt-existing-project)
- [Adopt any Vite or Next app](benefits.md#adopt-frontend)
- [Exclude paths from codegen](benefits.md#scaffold-exclude)
- [Keep your migration tool](benefits.md#keep-your-migration-tool)
- [Keep your deploy tool](benefits.md#external-deploy-tooling)
- [Record deploys from existing CI](benefits.md#record-deploys)
- [Bring your own IdP](benefits.md#byo-issuer)
- [Send telemetry anywhere](benefits.md#telemetry-anywhere)
- [Disown any generated file](benefits.md#owned-seams-disown)
- [Override any field, bring any YAML](benefits.md#overrides-byo-yaml)
- [Connect a cluster you run](benefits.md#cluster-connect)
- [Monitoring for any app](benefits.md#monitoring-any-language) _(hosted)_

---

<a id="hosting-knows-your-app"></a>

## Reliant hosting wires Postgres, secrets, telemetry and domains from your declarations

On a stitched stack each hosted piece needs an account, a key and a config file, and none of it knows the app. Reliant hosting reads the declarations forge renders: a declared database becomes a managed Postgres in your own namespace with backups, a bucket gets its credentials and CORS, secrets are write-only per env, telemetry flows with no token to paste, and a hostname gets HTTPS. With the Reliant harness, a paging alert becomes an investigated GitHub issue, every pull request gets code and security review, and agents deploy through the same planned path.

**How:** Because the platform reads the same declarations forge renders, it can provision and wire each resource with no keys to copy, and because Reliant's agents use forge's own tools, they act within its plans.

**See it:** `forge env status prod`

**Needs:** Reliant hosting

**Backed by:**

- [Staging and prod hosted by default](benefits.md#hosted-by-default) _(hosted)_
- [Managed Postgres](benefits.md#managed-postgres) _(hosted)_
- [Backups and point-in-time restore](benefits.md#managed-postgres-backups) _(hosted)_
- [Managed object storage](benefits.md#managed-buckets) _(hosted)_
- [Write-only hosted secrets](benefits.md#hosted-secrets) _(hosted)_
- [Telemetry wired with no keys](benefits.md#hosted-otel-injection) _(hosted)_
- [Custom domains with automatic HTTPS](benefits.md#custom-domains-tls) _(hosted)_
- [Static hosting with a CDN](benefits.md#static-hosting-cdn) _(hosted)_
- [A live URL on deploy](benefits.md#live-hostname) _(hosted)_
- [An on-call first responder](benefits.md#oncall-agent) _(Reliant)_
- [AI code and security review](benefits.md#ai-pr-security-review) _(Reliant)_
- [Agents deploy with guardrails](benefits.md#agent-forge-deploy-tool) _(Reliant)_

---

<a id="limits"></a>

## What Forge does not do

The trade-offs, checked like the benefits. `forge skill load compare` puts
them beside Vercel, Supabase and Render.

- **Hosted frontends are static exports.** Reliant hosting serves a frontend only as a static export, so no server-side rendering, server actions, middleware, ISR, request-time `cookies()`/`headers()` or on-the-fly image optimization; a Vite SPA or a statically exportable Next.js app fits as it is. Instead: run a Next.js app that needs a server as a container workload (`output: standalone`, skill `frontend/serving`), or keep it on a host built for that and point it at the forge API.
- **No hosted cron jobs or operators.** Hosting runs services, workers and jobs, not cron-kind workloads or Kubernetes operators. Instead: a worker that runs its own schedule runs hosted; bind a cron job or an operator to a cluster you run and keep the rest hosted.
- **Generated backends are Go.** The typed API, ORM, handlers and tests forge generates are Go over Connect RPC, with TypeScript clients; a backend in another language deploys from its Dockerfile with environments, releases, hosting and OpenTelemetry monitoring, but none of the generated code.
- **One hosted region.** Hosted services and managed databases run in one region, GCP us-central1, with no region choice; static sites are served through the CDN edge. Instead: bind a workload to a cluster you run in another region (`forge.OnCluster`).
- **Hosted backends do not autoscale.** A hosted service or worker runs the replica count its env declares; it does not scale with traffic between a min and a max, and a managed database's storage and compute do not grow on their own. Instead: raise the replica count or the shape in the env's `main.k` and deploy, or bind the workload to a cluster you run and give it your own HorizontalPodAutoscaler.
- **Hosted profiling is opt-in.** Continuous CPU and heap profiling is on by default only against the local ClickStack; a hosted env collects profiles only once its `main.k` sets `signals.profiles = True`. Instead: set `observability = forge.Observability { signals = forge.ObservabilitySignals { profiles = True } }` in the env's `main.k`.
- **No sign-in gate on deployments.** Hosted URLs, previews included, are public; there is no team sign-in or password in front of a whole deployment. Managed identity signs in your app's own users, and each RPC requires a signed-in caller unless its proto opts out, but a static site or page is served to anyone. Instead: put the pages behind your app's own sign-in (the frontend route guard and `auth_required`), or keep a private preview on a cluster you run behind your own access proxy.
- **Unclaimed deploys are temporary.** A deploy with no account goes offline after 72 hours unless its claim link is opened, and an unclaimed service has no outbound internet.
