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

Run actual SQL/Temporal server infrastructure and real CAN-history verification
(local pinned Temporal CLI required; no SDK download fallback):

```sh
RUN_FUTURECHAT_B=1 TEMPORAL_CLI_PATH=/absolute/path/to/temporal \
  GOWORK=off go test -tags integration \
  -run '^TestFutureChatB(Infrastructure|RealHistoryContinuity)$' \
  -timeout 3m ./internal/privatepipelineharness
```

Verified local binary used for the recorded proofs:
`/tmp/futurechat-b-tools/temporal-1.5.1/temporal`, official temporalio/cli v1.5.1,
asset `temporal_cli_1.5.1_linux_amd64.tar.gz`, SHA256
`ddc95e08b0b076efd4ea9733a3f488eb7d2be875f8834e616cd2a37358b4852d`.
Official same-release `checksums.txt` SHA256
`1b674a6e62e2e582cfd5cd84fa09f58844a2507d4621e0f4278a587ef9095a90`.
Both were verified before extracting only `temporal`, after rejecting unsafe tar
paths/symlinks. The fixture enforces the extracted executable's SHA256 at runtime:
`eb4f6ddaa41cadf58c7a77aa50c4bd932d6739fcef04ac76c7a679eaff2f5e6a`,
plus `--version`=1.5.1 BEFORE allocating SQL. Only linux/amd64 is supported; an
unknown platform, missing absolute path or changed binary fails explicitly.
This is a temporary dev dependency, not a system installation.

The fixture does not use SDK StartDevServer: it starts its owned CLI directly in
a fresh process group, registers cleanup before starting, bounds startup readiness,
and terminates/waits its own group on startup failure or teardown. A single
lifecycle owner uses Linux Waitid(WNOWAIT) to retain the direct child's PID/PGID
identity, including as a zombie, until ALL group signals finish; only then does
it call cmd.Wait once. Stop/context only request shutdown and await that owner.
A deterministic concurrent-stop regression pauses before/after group signaling,
proves the leader remains unreaped through both phases and is finally reaped once.
Together with never-ready and binary-mismatch tests it passed 0.493s. Repaired
lifecycle actual server+CAN+DynamicWorkflow SQL tests passed together in 19.685s.
DynamicWorkflow uses concrete SQL status/checkpoint/cleanup activities; its
immutable definition loader is an explicit trusted-fixture boundary, not a mock
review-completion result. Full reviewer/author pipeline is still unproven.

Real server infrastructure passed 3.247s; actual CAN history/namespace fencing/
exact-run successor and terminal proof passed 17.621s. These still are NOT an
end-to-end reviewer-author pipeline. Server state lives in a unique temporary
SQLite file; SDK binds loopback on an allocated port. Only owned worker/server
handles are stopped. No pre-existing stack is reused.

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
5434 is explicitly rejected. Overrides deliberately support only maintenance URIs
with a literal loopback IP, explicit safe port and user, path `/postgres`, and
exactly one `sslmode=disable` parameter. Arbitrary PostgreSQL configuration is not
supported: all other query parameters (including encoded `port`, `dbname`,
`database`, `service`, `options`), multi-host/fallback targets and nonempty `PG*`
environment variables are rejected BEFORE database allocation. Both override
variables are checked in BOTH processes. Accepted URIs are rebuilt canonically and
parsed by actual pgx; effective host/port/database/runtime parameters and every
fallback are checked. The clone path therefore remains authoritative.
Pure `TestPrivatePostgresGuard` cases never connect or migrate any supplied URI.
No Go workspace or production module pins are edited.

Publisher-to-inbox boundary (PASS25.199s): run
`TestFutureChatBPublisherConfirmedInboxBoundary` with the same opt-in environment,
CP checkout and verified CLI. It uses real SQL policy/assignment/intent stores,
real catalog/provider head reads, core Prepare, actual CP HTTP credentials/claim/
bridge, confirmed receipt, WaitPolicy/WaitOutcome, exact-author server history and
SQL inbox consume. **Reviewer terminal evidence is explicitly seeded trusted SQL
fixture input**: this is NOT actual reviewer graph completion or full E2E.
Admission projection uses actual Enqueue/Reserve/UpdateOutcome/BindRun APIs; no
mock ReviewSource, IntentStore, CreateJournal, HeadSource or CheckBridge is used.

Remaining prerequisites are tracked in
`docs/research/futurechat-b-integration-harness-plan.md`: reviewed REL487 publisher
SQL authority and trusted completion/publication/outcome adapters. The reviewed
wait and CP SQL journal are assembled privately. Go workspace replacements cannot
bypass `internal` visibility; CP constructs its bridge/handler inside its own
test helper and exposes loopback HTTP only.
