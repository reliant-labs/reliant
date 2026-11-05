---
name: testing
description: "Testing Go services and packages — testkit's real-postgres harness, generated contract mocks, pkg/tdd, the task test lanes and their build tags."
emit: both
---

# Testing a forge project

Forge ships the harness, the mocks and the lanes. How you structure a suite is
yours; this is what is already there to build it out of.

## What forge gives you to test with

The tiers map onto forge machinery, and which one you are in decides what is
already available:

1. **Unit** — the real subject plus the **generated contract mocks** for its
   collaborators (`mock_gen.go`, one per interface in `contract.go` — never
   hand-roll a fake for something declared there).
2. **Integration** — `testkit.NewPostgresDB(t)` gives a real ephemeral postgres
   with your own `db/migrations/*.up.sql` applied, no Docker required. The ORM
   and its SQL are the things most worth exercising for real here, since a
   mocked repository cannot fail the way a query does.
3. **End-to-end** — `task test:e2e`, against a stack from `forge env up dev`.

Forge's generated mocks exist for collaborators, so the subject under test
stays real at every tier.

## Forge-specific harness facts

- **The DB harness is per-test, not per-suite.** `testkit.NewMigratedPostgresDB(t, fs)`
  gives every test its own database with your real schema — including CHECK
  constraints, FKs and triggers that a fixture struct would not reproduce. The
  migrations are applied ONCE per server and each test gets a clone
  (`pgtest.NewFromTemplate`, keyed by a hash of the migration files), so a
  few hundred tests do not replay the chain a few hundred times.
  `FORGE_TEST_POSTGRES_URL` points it at a server instead of the embedded binary.
  It takes `testing.TB`, so a benchmark gets the same database a test does.
  A template unused for a day (`FORGE_TEST_TEMPLATE_TTL`) is dropped by the next
  run, so a long-lived shared server does not keep one per migration edit.
  A test's own database is named after the process that made it, so one a
  crashed run left behind is dropped by the next run that reaches the server,
  and never one whose process is still running.
- **Need the database's URL too?** `pgtest.NewFromTemplate` returns the clone
  as a handle AND its DSN (`db, dsn, cleanup, err`), and hands its `build`
  func the template's DSN beside the handle — for a migrator that takes a URL,
  a second pool, or a server the test boots as a subprocess.
- **Tests run on production's Postgres major.** Declare it once in forge.yaml —
  `database: {postgres_version: "16"}` — and the embedded server boots that
  major (a passing suite on 18 proves nothing about SQL on 16). Unset, it is the
  newest major forge ships. `FORGE_TEST_POSTGRES_VERSION=17` overrides it for one
  run. With `FORGE_TEST_POSTGRES_URL`, a declared version is checked against the
  server's, so a CI service container on another major fails by name.
- **One server per run, not one per package.** Every `go test` binary of a
  fan-out shares one Postgres (embedded, or the `FORGE_TEST_POSTGRES_URL` one).
  Do not start a Postgres container per package with testcontainers: N containers
  is what exhausts a sandbox's memory cap. For a non-Postgres server (ClickHouse,
  a registry) that tests mutate server-wide, start ONE per run and lease it to a
  test at a time, resetting it between leases.
- **Test rows come from regenerated factories, never frozen literals.**
  `internal/handlers/<svc>/factories_gen_test.go` is rebuilt from the applied
  schema on every `forge generate`:
  - `New<Entity>(t, db, overrides…)` inserts one **minimal** valid row — every
    NOT NULL column without a DEFAULT and every FK parent filled, every other
    column left to its DEFAULT or NULL, every CHECK satisfied (lengths, ranges,
    vocabularies, one-way status implications). So a new row sits at its
    initial lifecycle state; override only what your test asserts on.
  - `New<CreateRequest>(t, db, variant)` seeds a create RPC's FK parents and
    returns a request the schema accepts; variants 0 and 1 are distinct rows.
    The scaffold-once `handlers_crud_test.go` builds its rows from these, so a
    migration you edit later (a GENERATED column, a new CHECK) flows into the
    test through `forge generate` instead of breaking it.
  A factory forge cannot derive a valid row for still compiles; calling it fails
  the test with postgres's reason, and `forge generate` prints the same warning.
- **Older lifecycle tests embed literal fixture SQL.** `forge lint
  --fixture-drift` executes each such statement against the current schema
  (rolled back) and reports postgres's own error per statement. The file is
  yours; the durable fix is to replace the seed block and literal create
  requests with the factory calls (see `testing/patterns`).
- **Mock at the typed client interface, not at HTTP.** Forge generates the
  former for every interface in a `contract.go`; mocking HTTP instead forces
  each test to know your serialization shape.
- **Live external dependencies** belong behind an explicit opt-in env var and a
  skip-with-reason, so `task test` stays hermetic in CI.
- **A test that renders or imports a real env** whose KCL declares
  `forge.ControlPlane` reaches that control plane's ledger unless it exports
  `FORGE_LEDGER=machine` (with a temp `FORGE_LEDGER_HOME`). Setting only
  `FORGE_LEDGER_HOME` does not keep it local. Assert it first:
  `forge ledger where <env> --json` must report `"backend": "machine"`.

<!-- @forge-only:start -->
## Library entry point: `pkg/tdd`

The canonical entry point for table-driven tests in a forge project is the `github.com/reliant-labs/forge/pkg/tdd` library. Forge's scaffolders (`forge project new`, `forge scaffold service`, `forge package new`, `forge generate`) emit unit / contract test files (plus CRUD integration tests when entities exist) that already import it. Scaffolded per-RPC rows are self-destructing — `WantErr: connect.CodeUnimplemented` fails the moment the handler is implemented, demanding a real assertion in its place; `pkg/tdd` has no permissive any-outcome mode. Treat the helpers below as the default vocabulary for new test files; reach for hand-rolled `for _, tc := range cases` only when the shape doesn't fit.

| Helper | Use |
|--------|-----|
| `tdd.Case[Req, Resp]` + `tdd.TableRPC` | unary Connect RPC tests (unit/integration/E2E) |
| `tdd.ContractCase` + `tdd.TableContract` | `internal/<pkg>/contract.go` Service tests |
| `tdd.E2EClient(t, srv, factory)` | `httptest.Server` → typed Connect client |
| `tdd.NewMock(opts...)` | terse Forge MockService construction |
| `tdd.AssertConnectError`, `tdd.WithTimeout`, `tdd.SetupMockDB` | standalone helpers |

See `testing/patterns` for copy-paste-ready templates.

## Test commands

The suite is defined in the project's `Taskfile.yml`, not in the forge CLI —
so these are the same commands the generated CI workflow runs. (There is no
`forge test`; it was removed precisely because a second definition could
disagree with this one.)

```bash
task test:short -- ./internal/<pkg>/...    # inner loop: -short, cached, no race
task test                                  # unit + every frontend's tests
task test:integration                      # the `integration`-tagged lane
task test:e2e                              # the e2e lane
task test:all                              # unit + frontend + integration
task coverage                              # coverage.out + coverage.html
```

Everything after `--` replaces the default `./...`, so it takes both package
patterns and `go test` flags. A scoped run skips the frontend lane — a Go
package pattern says nothing about a frontend:

```bash
task test -- ./internal/handlers/users/... # only that package tree
task test -- -v ./...                      # verbose; use when debugging
task test -- -run TestCreate ./internal/handlers/users/...
task test GOTESTRACE=                      # without the race detector
```

The race detector is ON by default in `task test`. `Taskfile.yml` is yours —
edit it when the project needs different flags, and every caller (you, CI,
agents) picks the change up at once.

### Two tiers: iterate on `test:short`, finish on `test`

While iterating, run `task test:short` scoped to the packages you touched,
after each edit; run `task test` once before you finish. The fast tier is
Go-only, `-short`, and deliberately has no `-count=1` and no `-race`: Go's test
cache keys on the test binary, its flags, and the files and env vars the test
reads, so a cached pass is a real pass for a test that builds its own state
(`t.TempDir()`, a fresh `testkit` database), while `-race` rebuilds every
package under its own cache key and runs 2-10x slower. Do not add either flag
to an inner-loop run, and never set a private `GOCACHE`.

The cache keys only on what the TEST PROCESS reads. A test that execs a child
process which reads repo files (`forge`, `kcl`, `go build`, `npm`, `git`, a
migration CLI) must either read those inputs itself or skip under `-short`, or
the cached tier can report a stale pass. The scaffolded CRUD tests are safe:
they read `db/migrations` with `os.ReadFile` (or `go:embed`) in-process.

### Mark the slow tests; everything else is fast

A test is fast — it runs in the blocking `-short` tier — unless it says
otherwise. A test that takes more than ~2s, or that does work a child
process or a container does, declares it first:

```go
func TestRendersProd(t *testing.T) {
	testkit.Slow(t, "renders prod through `forge env render`",
		testkit.Inputs("deploy/kcl/**", "forge.yaml"))
	...
}
```

`testkit.Slow` (`forge/pkg/testkit`) skips under `-short` and in the
integration lane (`-tags=integration` also compiles the untagged tests, which
`task test` already ran), and runs in `task test`. Its skip message is `why`
plus where the test runs instead. A helper that does the slow work for several
tests can call it with the test's `t`. `testkit.SlowSkipped()` answers the
same question for a `TestMain` that builds something only slow tests use.
Never call it from an integration-tagged test: it would skip in the only lane
that compiles it.

`testkit.Inputs` names the files the test depends on, as globs relative to the
module root (`*` within a segment, `**` across segments). They must be string
literals: a tool selecting tests by what a change touched reads them from
source, without running the test, so it can run a render test in the blocking
tier exactly when `deploy/` changed. Slow validates them on every run,
`-short` included.

A plain `if testing.Short() { t.Skip("<why>") }` is still a valid marker, and
so is bypassing only the slow side effect under `-short`. Gate it, don't gut
it — never weaken an assertion to fit the 60s per-package timeout.

Two guards hold the line, because neither sees everything:

| Guard | What it catches |
|---|---|
| `forge lint --slow-tests` | A test that execs `go`, `forge`, `kcl`, `node`, `npm`, `git`, `docker`, `kubectl`, … by a constant name, drives containers (testcontainers, dockertest, the docker client), or dials a real external host — in its own body, a `_test.go` helper, or a helper package that imports `testing` — with no `testing.Short` check on the way. It also catches the stale-cache case a timer cannot: a FAST test whose child process reads repo files. `git` against a repo the test built under `t.TempDir()` (or `os.MkdirTemp`) is not slow work — the analyzer follows the directory through locals, struct fields set in the same function, helpers' parameters and helpers that return a temp dir; git over the checkout still counts. A non-literal `testkit.Inputs` argument is reported too. Opt-in for now |
| `go test -short -json ./... \| forge ci test-budget` | Any top-level test that RAN in a -short run and took longer than `--budget` (default 2s), with file:line — whatever made it slow. Under -short a marked test skips, so anything it reports is unmarked |

`forge/pkg/pgtest` (and `testkit.NewPostgresDB`) is deliberately not "slow":
every test of a run shares one server and gets a cloned database, so the
scaffolded CRUD tests belong in the fast tier. A database test that is still
slow is the budget's to report.

## Don't hand-roll what forge already provides

Before you stand up your own test infra, reach for the capabilities forge ships — agents routinely reinvent these:

- **Full environment for integration/e2e — `forge env up <env>`.** This builds every service, brings up the compose-managed infra (Postgres, observability, …), and deploys each service to its declared target. Use it instead of hand-rolling `kubectl apply` / raw manifests / a bespoke docker-compose to get a stack under test. Once it's up, point real Connect clients at the running services.
- **Multi-cluster is native — let the deploy target drive it.** Each workload's binding names its own `forge.ClusterTarget` (its `cluster` field is the kubectl context). `forge env up` / `forge env deploy` send each workload to its own context, so a flow that spans clusters is just the normal deploy talking to multiple contexts. Don't script per-cluster `kubectl --context` juggling in your test — declare the targets and let forge route. Verify each context is reachable first (see the `debug` skill).
- **Fixtures + scenarios + mocks — `pkg/testkit` and the generated `mock_gen.go`.** `pkg/testkit` provides the harness primitives (migrated test DB, authed contexts, claims options) plus a fixture builder and a scenario builder for composing multi-step setups. Per-contract mocks are generated into `mock_gen.go` — use those for collaborators rather than hand-writing stubs. See the `pkg/tdd` table above for the table-driven entry points that sit on top.
- **Auth is enforced in every mode — inject claims, don't fake a token.** Authentication runs through `pkg/authn.Policy` and `SetupAuth` builds a real validator in dev and prod alike, so there is no environment in which a synthetic bearer is honored. Below the transport, hand the handler a claims-bearing context (`app.AuthedContext(t, testkit.WithUserID(...))`, or `middleware.ContextWithClaims`) — that skips the token dance without pretending validation happened. When the behavior under test IS the auth path — token validation, or the access-control checks your handlers make on the claims — drive a real signed token through the interceptor and assert that a missing or bad one is rejected.

## Go build tags (forge's tag/marker mechanism)

Forge enforces the "isolate heavy tests" discipline via Go build tags. Anything that touches subprocesses, network, filesystem outside `t.TempDir()`, time-based behavior, or external services MUST live in:

- `*_integration_test.go` with `//go:build integration` — DB-bound tests.
- `*_e2e_test.go` with `//go:build e2e` — full-stack flows.

Default `task test` runs the untagged lane only — untagged Go tests plus each
frontend's `npm test` — under `-timeout 120s` per package. The tagged lanes are
physically excluded from it (a build tag that is not set means those files are
never compiled, not skipped at runtime), so they cost nothing until you ask for
them with `task test:integration` / `task test:e2e`.

## The untagged lane has a 120s budget

`TestRunNewKindValidation/empty_becomes_service` once invoked the full `runNew`
pipeline — network, filesystem, subprocesses — from an untagged test file, and
hung for 99+ seconds against that budget. Worth knowing because the failure mode
is a CI stall rather than a red test: an untagged file that reaches for real I/O
is compiled into the default lane, where nothing skips it.

## Sub-skills (forge)

- `testing/unit` — hermetic, fast handler-level tests.
- `testing/integration` — real-DB tests behind `//go:build integration`.
- `testing/flow` — hand-written multi-entity RPC tests: seed the FK spine with `seedplan.SeedGraph`, drive the real service against a migrated DB, assert the derived result + the negative/gap case.
- `testing/e2e` — full-stack flows behind `//go:build e2e`.
- `testing/patterns` — copy-paste-ready table-driven templates for the four most common test shapes.

For Next.js / vite-spa / React frontends, this Go-flavored skill does NOT apply. Load the top-level `frontend-testing` skill instead — it covers Vitest + Testing Library patterns, the `mockTransport()` seam, and the four-state page coverage rule.
<!-- @forge-only:end -->
