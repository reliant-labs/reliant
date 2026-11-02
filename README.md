# Forge

**Forge is an opinionated, open-source Go + React framework and deploy platform
that ships the production system — dev environments, tests, secrets, multiple
environments, releases, observability and hosting — from the first commit, built
for AI agents to work in.**

Most teams build that system in year two, under deadline, on top of a codebase
that wasn't shaped for it. With Forge the first commit already has it, wired
into files you own and can change.

## Everything included

What a new app would otherwise stitch together from separate tools:

|                                      | What you get                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Build**                            | Go services over [Connect RPC](https://connectrpc.com), plus workers, jobs, Kubernetes operators and CLIs · Next.js (static export), Vite and Expo React Native frontends with generated typed hooks · SQL migrations as the schema, a generated ORM, [migration safety checks](#linting-for-bug-classes-not-just-style) and seeded, foreign-key-coherent dev data · managed sign-in, with a local identity provider in dev · custom linters and generated CI |
| **Test**                             | A real Postgres test harness, a generated mock for every interface, table-driven RPC tests, frontend mock scenarios                                                                                                                                                                                                                                                                                                                                           |
| **Dev**                              | `forge env up dev`: the whole stack on one command, under hot reload; every git worktree gets its own stack on its own port block, so ten agents can run ten stacks on one machine                                                                                                                                                                                                                                                                            |
| **Ship**                             | Any number of environments, each a directory under `deploy/kcl/<env>/` · secrets declared once and bound per environment, never in a `.env` file · container builds · releases built once and promoted by digest · a release timeline (`forge env releases`) · deploys to host processes, Docker Compose, a Kubernetes cluster you run, or Reliant hosting                                                                                                    |
| **Host** (optional, Reliant hosting) | Services, workers and jobs · managed Postgres · static hosting with a CDN · custom domains · a container registry                                                                                                                                                                                                                                                                                                                                             |
| **Run**                              | Logs, traces, metrics, continuous profiles, frontend errors and session replay · dashboards generated from your services · default alerts (error rate, p95 latency, a silent service, crash loops, OOM kills, failed deploys, browser error spikes) · release markers                                                                                                                                                                                         |

Add [Reliant](https://reliantlabs.io) and the list grows an agent harness,
cloud dev machines, AI code and security review, and an on-call agent. Forge
doesn't need any of them.

## Opinionated, never a wall

The opinions come from a principal engineer with more than a decade of building
reliable systems, and several exist because the unsafe version caused a real
incident:

- **An error message once shipped a database password** to an unauthenticated
  caller. Now `svcerr.Wrap` replaces unrecognized errors with
  `internal server error` and keeps the original server-side.
- **Images built on an arm64 laptop once got pushed to amd64 nodes.** Now cloud
  targets require an explicit `platform`, and it fails at author time.
- **Replicas once half-migrated a schema.** Now migrations apply from an
  initContainer under a Postgres advisory lock, and a failed one stalls the
  rollout with the old pods still serving.

Every default is overridable and every generated file has an owned seam beside
it. You get an ORM you can drop out of, a middleware chain in a `serve.go` you
own, and `pkg/` as a library you import rather than a framework you live inside.
Bring your own infrastructure or use ours: each workload binds to a host
process, Compose, a cluster you operate or Reliant hosting, in the same KCL.
Forge is container- and Kubernetes-first, and it deploys code it never compiled,
in any language. Our own control plane builds three services out of a
**separate repository** that Forge never compiles. More in
[Opinions with an exit](#5-opinions-with-an-exit).

## Scales with you

A frontend on a hosted database is a fine place to start and a hard place to
stay. Forge's starting point is the one you'd grow into: multi-service
backends, background workers, jobs, operators and CRDs, CLIs and mobile apps,
over a real Postgres you manage with migrations that are safety-checked before
they run (every migration declares whether the previous release survives it).
Environments multiply without a rewrite: `forge env new` derives staging, prod
or a customer's environment from one you already have, and `forge env diff`
shows how each differs from what is deployed.

It covers the whole range, from the small end up. A landing page, docs site, SPA
or PWA with no backend is a first-class project: one static frontend, the same
environments and build-once releases, a dev loop with no database or cluster,
and hosted static hosting with a CDN (one static site per org is free; each
environment's site counts). Start one with `forge project new <name> --kind site`,
or adopt an existing Vite or Next.js app in place with `forge project new
--in-place --kind site` (`forge skill load deploy/static-site`). When it grows a
waitlist or a checkout, the API is its own forge project beside it.

## Built for your agent

When an agent writes most of the code, the bottleneck stops being how fast you
can write it and becomes **knowing whether it's right**. Forge is one binary on
your `PATH` with no plugin, editor extension or server, so anything that can run
a shell command can drive it: Claude Code, Codex, Cursor, Copilot, Gemini CLI,
Aider, a harness you wrote, or a CI job. A person at a terminal uses the same
commands, so there is nothing an agent can do that you can't check by hand.

- **Skills live in the binary.** `forge skill search <topic>` and
  `forge skill load <name>` print the playbook for an area, about 150 of them,
  matched to the installed version. Nothing to paste into a prompt, nothing to
  drift.
- **It explains itself.** `forge start` prints the path from an empty directory
  to authored protos. `forge project capabilities --json` lists every verb, lint
  analyzer and marker, enumerated from the binary, so it can't go stale.
- **Scaffolding and lints are guardrails against AI slop.** `forge scaffold`
  lands each service, rpc, worker or entity in the right place, already wired.
  `forge lint` rejects what review misses: errors that could leak internals,
  list filters that silently do nothing, edited generated files, unsafe
  migrations. A finding names
  the file, line, rule and fix, and `forge lint --quiet` prints only what
  failed and ends with the verdict, so the agent reads one line to know whether
  it is done.
- **It debugs from real telemetry.** `forge obs status`, `forge obs issues` and
  `forge obs trace` read a running app's logs, traces, errors and alerts, so a
  fix starts from what happened rather than a guess
  ([section 3](#3-feedback-loops-so-an-agent-can-see-what-it-built) has the rest).
- **The project tells the agent all of this.** `forge project new` writes
  `AGENTS.md` and a pointer for the agent it detects
  ([Conventions & skills](#conventions--skills)).

To use it from any agent, put this in the prompt or the agent's instructions
file:

```text
Use Forge (github.com/reliant-labs/forge) for this project.
Run `forge start` first and follow it. Load depth with
`forge skill search <topic>` and `forge skill load <name>`.
A change is done when `forge lint`, `go build ./...` and `task test` pass.
```

## Greenfield, or bring your own app

Forge is best from an empty directory, where the whole structure is yours from
the first commit. An existing app doesn't have to wait for that. Forge builds
and deploys code it never compiled, so a service in any language gets
environments, secrets, release promotion and some monitoring as it is, and a Vite or Next.js app is adopted in
place. Move code onto Forge's structure over time as it earns it:
`forge skill load migration` is the playbook.

## The first sixty seconds

```bash
# Install (macOS / Linux; Windows, Homebrew, npm and go install are under Install)
curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh

forge project new my-app && cd my-app
forge scaffold service billing
forge generate
forge env up dev
```

That last command brings up Postgres, runs your migrations, and starts your
services and frontends, then prints a table of URLs and a log file for each
process. The table includes the HyperDX UI, already receiving the app's traces,
metrics and logs (set `_observability = False` in `deploy/kcl/dev/main.k` to turn
it off). With a frontend (`forge project new my-app --frontend web`) it also
starts a local identity provider, so the app's own sign-in page works on the
first run. No signup, no external accounts, no cloud project, no cluster.

Once you add tables (a `// forge:entity` message and `forge scaffold`), a fresh
database is **seeded with foreign-key-coherent rows that satisfy every
constraint your schema declares**, so the app boots alive instead of booting
empty. Text forge cannot derive reads as `sample_<column>_<n>` until you name
your domain's words in `db/seeds/vocab.yaml` (`orders.currency: [USD, EUR]`,
`orders.customer_email: {type: email}`).

When you want it online, `forge env deploy staging` builds a release once and
deploys it. A new project's staging and prod are bound to Reliant hosting; the
same environments deploy to a Kubernetes cluster you run, or as host processes,
by changing where each workload is bound.

## With Reliant

[Reliant](https://reliantlabs.io) is the agent harness we build around Forge. It
already knows every Forge skill, runs `reliant forge` signed in to hosting, and
ships Autopilot workflows that react to errors and alerts. It is never required.

---

## 1. Production best practices from day 0

The first commit already has the things teams usually bolt on in year two.
A scaffolded service boots with structured logging, tracing, metrics, panic
recovery, request IDs, health and readiness probes, graceful shutdown, and
payload caps — all wired in a `serve.go` **you own** and can reorder.

The defaults are chosen to fail safe:

- **Errors don't leak.** `svcerr.Wrap` replaces unrecognized errors with
  `internal server error` and keeps the original server-side, so error text you
  didn't write is never text you publish.
- **Auth fails closed.** A production server refuses to boot with no auth
  provider configured, at construction time rather than on first request.
  JWT with OIDC discovery is the default; Clerk and Firebase are variants, API
  keys are a first-class credential (`X-API-Key`, checked against a table you
  own, on the same pool your handlers use), and a local dev IdP gives you a
  real browser sign-in with one command.
- **Unsafe migrations are caught before they run**, not after they lock a table
  in production — see [bug-class linting](#linting-for-bug-classes-not-just-style).
- **You can't deploy the wrong env to the wrong cluster.** The kubectl context
  is read solely from the environment's KCL. There is no CLI override and no
  fallback to your current context.
- **Secrets stay out of rendered output.** Declare a secret once as a
  reference and bind it per-environment through a provider. Forge refuses to
  render plaintext Secrets into anything but a recognized local cluster.
  `sensitive` config fields get no slot in git to paste a password into and no
  CLI flag to leak one into shell history.

### What "secure by default" does and doesn't mean here

Two different things ship under that heading, and they're worth separating,
because one is load-bearing and one is a starting point.

**The runtime posture above is real and tested** — it's the core of Forge, it's
covered by the same gates as everything else, and it's what stops the incidents
in the list above from recurring.

**The scanning is generated into your CI, and it's yours to verify.** A new
project gets a `ci.yml` that runs `govulncheck` and `npm audit` through
`forge ci vuln-scan` — a gate built to refuse a pass it didn't actually verify,
so a missing scanner fails the job instead of exiting green over an empty scan.
Container images get Trivy scans that block on HIGH/CRITICAL, and pushed images
get an SPDX SBOM, a keyless cosign signature, and a build-provenance
attestation, all bound to the immutable digest rather than a mutable tag.
Dependabot ships too.

What is **not** there today: secret scanning, SAST, IaC/manifest scanning, and
license compliance. And the generated workflows are among the less-exercised
surfaces in Forge — treat them as a strong default to review and adapt, not as
a compliance story you can point an auditor at. `forge lint` itself does no
security scanning at all; it enforces architecture and migration safety.

### Multiple environments on day 0, not month six

Environments are not a thing you graduate to. A new project declares them as
KCL under `deploy/kcl/<env>/`, and the same source renders dev, staging, and
prod. Our own control plane runs `dev`, `dev-k8s`, `e2e` and `prod` across local
k3d clusters and two GKE clusters, from one definition.

- **Build once, promote by digest.** `forge env deploy staging` builds,
  cuts an immutable release and deploys it; `forge env deploy prod --from
staging` ships the exact bytes that passed staging. Every deploy prints a
  plan first and writes nothing until it is approved (`--yes` in CI, or
  `--plan-only` then `--approve <digest>` for a reviewed two-stage pipeline).
- **Preflight before apply.** On remote clusters, Forge verifies every
  referenced Secret key and every image against the live target and reports
  all failures up front, rather than discovering them as a mid-rollout
  `ImagePullBackOff`.
- **Failures move to author time.** KCL schemas validate at load. The guard
  requiring an explicit `platform` on cloud targets exists because building
  from an arm64 laptop once pushed images that wouldn't run on amd64 nodes.
- **Config is typed and declared once.** Annotate a proto field; get env-var
  loading with precedence, a KCL schema, per-environment values, and — for
  `sensitive` fields — a secret reference with no slot in git to paste a
  password into and no CLI flag to leak one into shell history.

---

## 2. A dev environment that actually comes up

The most expensive document at most companies is the onboarding doc that is
wrong in two places. Forge's answer is that there isn't one: the environment is
declared in the same KCL that renders staging and prod, so "how do I run this"
has a command instead of a wiki page.

- **One command, whole stack.** `forge env up dev` starts the dev
  infrastructure, every host service (under hot reload) and every frontend,
  with no cluster involved unless something in `dev` is bound to one.
- **The database is not empty.** On first boot against a dev env, Forge seeds
  deterministic, foreign-key-coherent data **introspected from your applied
  schema**, in one transaction, only when every seedable table is empty.
  Synthesized values satisfy your CHECK constraints, length caps, and UNIQUE
  columns by construction. No seed files land in your repo.
- **Full observability locally.** `forge env up` runs ClickStack — ClickHouse,
  the HyperDX UI and an OTLP collector, in one container — and points every
  host process at it. Traces, metrics and logs show up in HyperDX with nothing
  to register or copy, no accounts, no sampling surprises. Turn it off with
  `_observability = False`.
- **Ten checkouts don't collide.** Each git worktree gets its own stable
  100-port block through a lock-guarded registry, so parallel stacks (or
  parallel agents) coexist. `forge env ps` lists every Forge stack on the
  machine, across projects.
- **Nothing hangs.** Without a TTY, `forge env up` starts everything, prints
  the summary, and returns — so CI and agents don't deadlock waiting on a
  foreground process.

The pitch in one line: a new engineer, or a new agent, gets the whole system
running against seeded data, with end-to-end tracing, before lunch on day one.

---

## 3. Feedback loops, so an agent can see what it built

Generation is the easy half. This is the half that decides whether a project
survives contact with six months of agent-authored changes.

### Predictability: one known place for everything

An LLM's failure mode isn't writing bad code — it's _guessing_. Guessing which
port the API bound, where the logs went, how to reach the database, whether the
thing it just changed is even running. Every guess is a plausible-looking
answer that might be wrong, and wrong answers compound silently.

Forge's structural advantage is that these questions all have a command, and
the command reads the same rendered KCL the running stack was built from — so
the answer can't drift from reality:

| The question                                      | The command                                                   |
| ------------------------------------------------- | ------------------------------------------------------------- |
| What is running, on what port, and is it healthy? | `forge env status <env>` (`--json`)                           |
| Where are the logs for this service?              | `forge env status` — a log path per service                   |
| What are the ingress URLs?                        | `forge cluster urls` (`--json`)                               |
| What does the schema actually look like?          | `forge db introspect --dsn "$DATABASE_URL"` (`--format json`) |
| How do I call this RPC?                           | `forge api curl <service.method>`                             |
| What is this project, structurally?               | `forge project audit --json`, `map --json`                    |
| Is the project well-formed?                       | `forge doctor`                                                |
| What can Forge even do?                           | `forge project capabilities`, `annotations`, `libraries`      |

Two properties make this usable by a model rather than just available to one.
**Every answer is machine-readable**, so an agent parses instead of scraping
scrollback. And **the surface is enumerated from Forge's own command tree and
descriptors**, so it cannot drift the way a document can — the agent asks the
binary rather than trusting a stale README.

`forge env status` is the sharpest example. It reports the port a service bound
_right now_, which process holds it, whether that binary is **stale versus your
repo HEAD**, and a loud DUPLICATE flag when two vintages of the same service
are serving at once — the classic "hot reload spawned a new worker and didn't
reap the old one" failure that otherwise reads as "my fix didn't work."

### Observability you get without asking for it

Three boundaries are instrumented, so a request is traceable end to end:

1. **The RPC edge** — one span, metric, and log line per RPC.
2. **Every in-process component call** — the layer that is usually dark. Forge
   generates a per-method decorator from your `contract.go` interface, so
   adding a method instruments it automatically. There is nothing to maintain
   by hand and nothing to forget.
3. **Every database query** — a child span per query, via the ORM's bun hook.

Logs are structured JSON with stable keys (`procedure`, `request_id`,
`trace_id`, `duration_ms`, `status`, `code`), and the trace ID is injected into
every log line — so a log entry is one click from the trace that produced it.
ClickStack (ClickHouse, the HyperDX UI and an OTLP collector) runs locally in
Docker Compose, on by default, with traces, metrics and logs already flowing.
No external accounts, no signup, no sampling surprises.

`forge generate` also derives dashboards from your services and frontends (an
overview, one per service with a row per RPC, a frontend dashboard) and a set
of default alerts (error rate, p95 latency, a silent service, browser error
spikes, crash loops, OOM kills, failed deploys), tuned per environment in KCL.
Both ride in every release, so a deployed environment's console shows the set
its release carries. Frontends ship with browser telemetry (errors, sessions,
web vitals), and their source maps are filed privately with the release
rather than served. Details:
[Observability](https://docs.reliantlabs.io/forge/observability).

### Checks that refuse to lie to you

```bash
forge env status dev                  # everything runtime about an env
forge env status dev --signal traces  # or one signal at a time
forge env status dev --json           # machine-readable, for an agent
```

A check that cannot obtain the facts it needs reports **UNDETERMINED**, never a
pass and never a skip. Three outcomes, not two. This matters more than it
sounds: a false green is the single most expensive thing you can hand an agent,
because it will confidently build on top of it.

For the same reason, we'll tell you what these checks _don't_ cover. They
verify the telemetry pipeline is healthy — containers up, signals ingesting,
probes green. They do **not** certify your app logic is correct. A stack can be
fully green while a cross-cluster dial is failing. Proving an app-flow
invariant is what tests are for.

### Test seams that already exist

- **A mock for every interface**, generated from `contract.go`. An agent never
  hand-rolls a fake, and a contract change breaks the build _at the mock_
  rather than somewhere distant three files away.
- **A real Postgres, not a stub.** `pkg/pgtest` and `pkg/testkit` give
  hermetic tests an actual ephemeral database with your migrations applied.
- **Table-driven RPC tests** via `pkg/tdd`, so handler tests have one shape.
- **Scaffolded tests self-destruct.** A generated test row asserts
  `Unimplemented`, so it _fails the moment the handler is implemented_ —
  demanding a real assertion. There is no permissive "any outcome" mode.
- **Frontend mock scenarios.** `forge scaffold scenario` writes a typed
  Connect-RPC handler overlay you reach with `?scenario=<name>` in the URL —
  so an agent can teleport the UI into a specific server-state shape (an empty
  list, a revoked token, a failing payment) without touching the backend.
  Anything a scenario doesn't override falls through to the base transport.

### Two more that don't fit in a table

- **A real debugger.** `forge debug` drives Delve against a running service:
  breakpoints, step in/over/out, locals, args, stack, goroutines, and
  expression evaluation. An agent can inspect a live process rather than
  reasoning about one.
- **Errors are runbooks.** Failures state what was expected, what was found,
  and the literal command that fixes it — so a failure is an instruction to
  follow rather than a puzzle to solve. Commands are non-interactive and never
  hang without a TTY, so an agent can drive them unattended.

---

## 4. Guardrails, so the loop stays closed

Feedback is worthless if the code drifts faster than you can observe it.

- **`contract.go` is the seam.** Every internal package declares its interface,
  its `Deps` (interface-typed, enforced by lint), and its constructor.
- **Conventions are enforced, not suggested.** `forge lint` ships twenty-odd
  analyzers that catch what review usually misses: handlers doing business
  logic, unwrapped domain errors, concrete types in `Deps`, adapters making
  RPCs, frontend stores holding server data, unsafe SQL migrations, edited
  generated files. A convention a linter enforces is one an agent can't quietly
  drift from. Deterministic-safe fixes apply automatically; `--json` and
  `--strict` are there for CI.
- **Regeneration is safe.** Every generated file carries a hash of its own
  body. Hand-edit one and the next `forge generate` stops rather than silently
  reverting your work.
- **Boilerplate is scaffolded, so logic is what's left.** `forge scaffold`
  takes a noun — `service`, `entity`, `rpc`, `package`, `adapter`, `worker`,
  `binary`, `operator`, `crd`, `webhook`, `frontend`, `scenario` and more — and
  lands it in the right place, already wired, already conventional.
- **Markers declare intent in one line.** `// forge:entity`, `soft-delete`,
  `append-only`, `secret`, `immutable`, `version` for optimistic concurrency,
  and others make the schema, the RPCs, and the generated code agree about a
  decision you stated once.

### Linting for bug classes, not just style

Most linters argue about formatting. The interesting ones catch **bugs that
compile, pass review, and fail in production** — and that's where Forge's are
aimed.

The distinction matters because architectural drift is how those bugs get in.
A handler that reaches past the service layer straight into the ORM isn't a
style violation; it's a transaction boundary in the wrong place, an
authorization check that now has somewhere to hide, and a query nobody will
find when it gets slow. Shipping today:

- **Layering is enforced.** Handlers may not map errors or carry business
  logic (`no-handler-error-mapping`, `handler-file-size`); `Deps` must be
  interfaces so a layer can't bind itself to a concrete implementation
  (`deps-are-interfaces`); adapters and outbound I/O may not make RPCs
  (`adapter-no-rpc`, `outbound-io-no-rpc`).
- **Migrations are checked before they run.** Adding a `NOT NULL` column to a
  populated table, a volatile default, a destructive change — each caught at
  author time, each with an explicit opt-out for when you mean it.
- **Runtime nil-derefs get caught statically.** `optional-deps-guard` flags an
  unguarded dereference of a dependency that is legitimately nil at runtime.
- **Server state can't leak into client state.** `frontend-stores-no-server-data`
  catches the Zustand store quietly duplicating what React Query owns — the
  root of most "why is the UI stale" bugs.

**Where this is going:** the roadmap is more bug-class analyzers of the same
shape — N+1 query detection, handlers calling the ORM directly instead of going
through the service layer, foreign keys with no supporting index, and similar
patterns that are invisible in review and expensive at scale. These are
**planned, not shipped**; `forge project capabilities` always prints the
analyzers your binary actually has, which is the list to trust over this one.

The reason to build them here rather than bolt on a generic linter is that
Forge already knows the shape of your project — which package is a handler,
which is a service, which struct is an entity, and what the applied schema
looks like. A rule like "this foreign key has no index" or "this handler
skipped its service" needs exactly that structural knowledge, and a
general-purpose Go linter doesn't have it.

---

## 5. Opinions with an exit

Forge's philosophy is **primitives, not modes**: every default is overridable,
every generated file has an owned seam beside it, and no convenience is
implemented as a wall. Stated as pairs, because an opinion is only safe if you
know how to leave it:

- We give you **an ORM** — and you can drop to raw SQL whenever you want.
- We give you **a middleware chain** — in a `serve.go` you own and can reorder.
- We give you **`pkg/`** — as a library you import, not a framework you're
  trapped inside.
- We give you **Kubernetes deploys** — and every workload binds its own
  runtime, so the same declaration runs as a host process in dev, on a cluster
  you operate, or on hosted infrastructure we run. A runtime that hands a
  workload to your platform's own CLI (Fly.io, Cloud Run, ECS, Vercel) is
  [planned](https://github.com/reliant-labs/forge/issues/400), not shipped.
- **And you don't have to write Go.** KCL declares the workloads; `ShellBuild`
  builds anything Forge doesn't compile.

That last one is the one people underestimate, so here is the receipt: our own
control plane uses `ShellBuild` to build three services out of a **separate Go
repository**, cross-compiling and pushing images Forge never compiles in-tree —
plus one fat image built by shelling out to GCP Cloud Build, because its
toolchain steps can't be cross-compiled on a laptop.

The details:

- **Deploy is target-agnostic.** Workloads are declared once as a KCL
  `fw.Workload`, and each env binds every one to its own runtime —
  `forge.OnHost`, `OnCompose`, `OnCluster` or `OnHosted`. Frontends bind the
  same way, to a dev server or a static runtime (`OnHosted`, `OnBucket`,
  `OnFirebase`). There is no env-wide mode flag.
- **`ShellBuild` is the shell escape hatch** for a workload forge does not
  build itself: a sibling repo, a third-party binary, a language runtime.
  `cmd` is a plain KCL string and forge runs it byte-for-byte via `sh -c`, from
  the declared `cwd`, with the declared `env` merged on. Forge substitutes
  nothing — the tag, arch and environment are read in KCL
  (`forge.image_tag()`, `forge.target_arch()`, `forge.env()`) where the
  command is composed.
- **KCL is still KCL.** Forge models _your_ workloads — Application,
  Environment, ConfigMap, Ingress, RBAC. It deliberately does not model
  third-party in-cluster infra, so when you need NATS, Temporal, or a Postgres
  operator, you emit raw manifests (or run Helm) and they compose into the same
  rendered stream. No fork, no fight with the schema. That's how our control
  plane runs all three today.
- **`pkg/` is a library you import, not a framework you're stuck inside.**
  `serverkit`, `svcerr`, `orm`, `auth`, `apikey`, `oauth2`, `obs`,
  `middleware`, `crud`, `tdd`, `testkit`, `pgtest`, `config`, `money`,
  `controller`, `migratekit`, `schemadef`, `seedplan`, `audit`, `validate`, and
  more — `forge project libraries` lists them all with their doc summaries.
  `@reliantlabs/forge-web-runtime` is the TypeScript twin.

---

## The two truths

The one model to internalize before anything else:

- **Proto is the wire truth** — services, RPCs, messages, config.
- **SQL migrations are the schema truth** — `db/migrations/*.up.sql`.

Neither derives from the other. `forge generate` applies your migrations to a
real ephemeral Postgres and introspects the result, so entity structs, the ORM,
CRUD wiring and frontend pages are projected from a schema that provably
exists. Arbitrary Postgres DDL — schema-qualified tables, `JSONB`, arrays,
generated columns — works without Forge needing to understand it.

An entity exists where both halves exist: CRUD RPCs in the proto **and** the
matching table. Either half alone generates honest nothing.

## Who this isn't for

Forge is opinionated at the floor even though it's open at the edges, and some
of those floor decisions won't suit you. Rather than have you discover that in
week three:

- **You don't want generated code in your repo.** Forge writes real files and
  regenerates them. If you'd rather everything be hand-authored, the ownership
  model will feel like overhead instead of a safety net.
- **You need a REST-first public API.** Connect RPC is the native surface.
  Connect handlers speak plain HTTP+JSON and REST URLs are available via
  Vanguard, but if a hand-shaped OpenAPI contract is your product, you'll be
  working against the grain.
- **Postgres isn't your database.** Schema projection introspects a real
  ephemeral Postgres. MySQL, SQLite, and non-relational stores aren't
  supported.
- **Your backend isn't a container.** A service deploys as an image, even
  when the target isn't Kubernetes. (Static sites are the exception: a static
  frontend ships as files to hosted static hosting, your own bucket or
  Firebase, with no image to run.)
- **You want a framework you'll outgrow and replace.** Forge is designed to
  hold structure for the life of the project. If you want a one-time scaffold
  and then never to see the tool again, use a template repo — that's a
  legitimate choice and Forge is the wrong shape for it.

## Install

Pick one. All of them give you a single `forge` binary; none needs a C toolchain, and the
first four need no Go toolchain either.

```bash
# macOS / Linux (amd64, arm64): one command, no sudo, no prompts.
# Installs to ~/.local/bin (set FORGE_INSTALL_DIR to change it) and checks the
# release's sha256 before installing. Pin a release with FORGE_VERSION=v1.0.0.
curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh
```

```powershell
# Windows (amd64, arm64)
irm https://raw.githubusercontent.com/reliant-labs/forge/main/install.ps1 | iex
```

```bash
# Homebrew (macOS and Linux)
brew install reliant-labs/reliant/forge

# npm / npx: downloads the same verified binary on first run
npx @reliantlabs/forge version
npm install -g @reliantlabs/forge

# Go toolchain
go install github.com/reliant-labs/forge/cmd/forge@latest
```

For a non-interactive sandbox or CI image, the `curl | sh` line is the whole
setup. Pin the version so the image is reproducible:

```bash
curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | FORGE_VERSION=v1.0.0 sh
export PATH="$HOME/.local/bin:$PATH"   # the installer prints this if it is needed
```

Releases attach `forge_<os>_<arch>.tar.gz` for linux, darwin and windows on amd64
and arm64, plus `checksums.txt`, if you would rather download one yourself.

### From a clone

```bash
# Build the binary into ./bin/forge
task build

# Or install onto $PATH (into $GOBIN)
task install
forge version

# Run straight from source without installing
go run ./cmd/forge version
```

On first render forge extracts KCL's native library (`libkcl.so`/`.dylib`, or
`kcl.dll` on Windows) into your user cache dir — `%TEMP%\kcl` on Windows — and
loads it from there. Set `KCL_LIB_HOME` to use another directory, which is the
fix when antivirus quarantines the DLL or `%TEMP%` is locked down.

## Quick start

```bash
# Scaffold a new project (service / CLI / library)
forge project new my-app
cd my-app

# Add a service, then regenerate the stack
forge scaffold service billing
forge generate

# The gate before you call a change done:
forge generate && forge lint && go build ./... && task test

# Run the whole dev stack: Postgres, the API under hot reload, frontends, ClickStack
forge env up dev

# Ship it (staging and prod are hosted on Reliant by default)
forge env deploy staging
```

The **[Forge user guide](https://docs.reliantlabs.io/forge/overview)** covers
the journeys end to end: quick start, project anatomy, adding a feature,
environments and deploys, hosting, secrets, domains, observability and
troubleshooting.
[**docs/getting-started.md**](docs/getting-started.md) walks the first one
from a checkout — service, entity, CRUD, running stack — and explains which
files are yours.
[**docs/concepts.md**](docs/concepts.md) covers why Forge is shaped this way.

Run `forge --help` for the full command surface, or `forge <command> --help`
for any one of them. Four commands print Forge's entire surface:

```bash
forge project capabilities   # every verb, every lint analyzer, every marker
forge project annotations    # how proto fields become columns and validators
forge project libraries      # every forge/pkg library and where it lives
forge project audit --json   # what THIS project currently is
```

## Conventions & skills

Forge ships an extensive skill catalog — playbooks covering architecture,
proto, db, api, services, testing, frontend, auth, deploy, observability,
debugging, migration, and code review. The conventions they describe are
enforced by `forge lint`, so prefer loading a skill before guessing:

```bash
forge skill list            # discover what's available
forge skill load <name>     # read one
forge skill search <term>   # find one by keyword
```

Skills are embedded **in the binary**, so `forge skill load` always matches the
Forge you are running. Forge never writes skill bodies into your project.
Reliant reads them from the binary automatically. Every project gets
`AGENTS.md` (the cross-tool project guide); `forge project new` also detects
the agent running it (`--harness` overrides) and adds a thin pointer for it:
`CLAUDE.md` importing `AGENTS.md` plus `.claude/skills/forge/SKILL.md` for
Claude Code, `.cursorrules` plus `.cursor/rules/forge.mdc` for Cursor,
`.github/copilot-instructions.md` for Copilot. Each pointer only tells the agent
to run `forge skill load forge`, `forge skill search <query>` and
`forge project capabilities`.

Any MCP-capable harness can skip the instruction file entirely: `forge mcp`
serves the introspection verbs, the skills, lint and generate as tools, with
instructions that say what forge is and to load the `forge` skill first.

```bash
claude mcp add forge -- forge mcp      # Codex: codex mcp add forge -- forge mcp
```

Config for Claude Code, Codex and Cursor: [docs/mcp.md](docs/mcp.md).

`reliant.md` at the repo root captures the critical rules and testing tiers in
brief.

## Repository layout

```
forge/
├── cmd/forge/       # CLI entrypoint (package main)
├── internal/        # CLI implementation, generators, templates, linters
├── kcl/             # KCL module: typed schemas + manifest render layer
├── pkg/             # Libraries projects import (serverkit, svcerr, orm, …)
│   └── components/  # UI component library shipped to scaffolded frontends
├── web-runtime/     # @reliantlabs/forge-web-runtime — the web twin of pkg/
├── proto/           # Forge's own proto annotations (forge/v1)
├── docs/            # getting-started, concepts, releasing, pkg + cross-repo sources
├── examples/        # Runnable examples
├── forge.yaml       # Project manifest
└── Taskfile.yml     # Automation entrypoints
```

## Development

```bash
task deps           # install Go (and frontend) dependencies
task test:short     # inner-loop tests: whole repo in seconds
task test           # full unit suite with -race
task lint           # golangci-lint + buf
task fmt            # goimports + go mod tidy
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the full dev loop, pre-commit
hooks, and PR process, and [`reliant.md`](reliant.md) for the testing tiers and
project conventions.

## License

Copyright 2025-2026 Reliant Labs, Inc. Licensed under the
[Apache License, Version 2.0](LICENSE); see also [`NOTICE`](NOTICE). That
covers all of forge: the CLI, the `github.com/reliant-labs/forge/pkg/...`
libraries your services import, the KCL modules, and the
`@reliantlabs/forge-web-runtime` npm package your frontends bundle.

**Earlier versions.** Forge versions published before this change were
released under the Business Source License 1.1. Reliant Labs, Inc., as their
copyright holder, also licenses every one of those versions — every commit of
this repository, tagged or not, and every published version of
`@reliantlabs/forge-web-runtime` — under the Apache License, Version 2.0. You
may use any of them under either license.

**What forge writes into your project is yours.** Generated code, scaffolded
files, and the components forge copies into your frontends become part of
your project. Where any of that output reproduces or derives from forge's own
templates or component library, Reliant Labs, Inc. grants you a perpetual,
worldwide, non-exclusive, no-charge, royalty-free, irrevocable license to use,
reproduce, modify, sublicense and distribute it under terms of your choice,
without any obligation to carry forge's license text, `NOTICE` or other
attribution. This grant covers that output only. Forge itself, including the
libraries and packages your project depends on, stays under the Apache
License 2.0.
