# Private pipeline integration fixtures

These are **infrastructure only**, not a passing review pipeline. Missing authority
adapters are not replaced with in-memory successes. Publication enrollment tests
remain separate evidence; no real git-push E2E claim is made.

Compile and test the hermetic HTTP boundary; SQL/Temporal skips under `-short`:

```sh
GOWORK=off go test -short -tags integration ./internal/privatepipelineharness
```

Run the concrete in-process SDK workflow/activity against real private SQL
(no Temporal CLI needed; not a server/history/CAN or review-flow proof):

```sh
RUN_FUTURECHAT_B=1 GOWORK=off go test -tags integration \
  -run '^TestFutureChatBInProcessSQL$' -timeout 2m ./internal/privatepipelineharness
```

Run actual SQL/Temporal server infrastructure only (local Temporal CLI required,
no SDK download fallback):

```sh
RUN_FUTURECHAT_B=1 TEMPORAL_CLI_PATH=/absolute/path/to/temporal \
  GOWORK=off go test -tags integration -run '^TestFutureChatBInfrastructure$' \
  -timeout 3m ./internal/privatepipelineharness
```

The database uses `db.NewTestDatabase`: isolated migrated pgtest clone, dropped at
cleanup. It ignores `DATABASE_URL`. Optional `FORGE_TEST_POSTGRES_URL` selects a
private Postgres maintenance server; never point this at dev port 5434/real data.
The Temporal namespace/queue and server address are private; cleanup stops only
the server/worker this fixture started. No fixed sleeps or shared worker queues.

The HTTP transport permits only original `https://api.github.com` destinations
and rewrites them to its owned loopback `httptest` server. This changes neither
production netguard nor the daemon's literal github.com git-push policy. No
credentials are recorded. The full workflow test must use actual catalog runners,
SQL adapters, CP HTTP handlers and the approved repaired wait implementation.

Run the actual cross-process CP boundary (real CP migrated SQL, encrypted stored
credential and credential service, Connect handler/gate, bridge and SQL claim;
external GitHub only is fake):

```sh
RUN_FUTURECHAT_B=1 CP_CHECKOUT=<absolute-isolated-control-plane-checkout> \
  GOWORK=off go test -tags integration -run '^TestFutureChatBCPRealSQLBoundary$' \
  -timeout 4m ./internal/privatepipelineharness
```

This checks unauthorized rejection/no provider call, exact-head create, duplicate
refusal, exact reconcile and a single provider check POST. It is NOT proof of
upstream review authority, JWT verification, author wait or native enqueue.
The parent compiles the CP helper with its own module and GOWORK=off, runs that
binary directly (not through a cached go-test runner), passes an ephemeral secret
without stdout logging, and closes its owned stdin for cleanup. The helper's
listener is loopback-only. `CP_TEST_POSTGRES_URL` selects CP's private test server;
5434 is explicitly rejected. No Go workspace or production module pins are edited.

Remaining prerequisites are tracked in
`docs/research/futurechat-b-integration-harness-plan.md`: reviewed REL487 publisher
SQL authority and trusted completion/publication/outcome adapters. The reviewed
wait and CP SQL journal are assembled privately. Go workspace replacements cannot
bypass `internal` visibility; CP constructs its bridge/handler inside its own
test helper and exposes loopback HTTP only.
