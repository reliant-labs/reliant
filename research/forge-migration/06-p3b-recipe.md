# P3b recipe: move one Connect service onto forge's handler model

How a P3b lane moves one service out of `internal/grpc/services` into
`internal/handlers/<svc>/` ([PLAN.md](PLAN.md) §4, P3). Written from the
pilot, which moved **SystemService** and **QuestionService** end to end.
A lane can execute from this document alone; the inputs it relies on are
[05-p3a-handler-map.md](05-p3a-handler-map.md) (which files belong to which
service) and the two pilot commits.

| Pilot branch | Commit | What it is |
|---|---|---|
| `refactor/p3b-system` | `5a2901e4` (on main `d814199f`) | SystemService, plus the shared pieces every later lane relies on (§1) |
| `refactor/p3b-question` | `91d28d60` (on the System commit) | QuestionService: the reference lane. A narrow `Repository`, a required collaborator, DB-backed tests, build-tagged consumers |

The pilot was rebased onto main after P2 (#829–#841) and the forge
d67d2a72 pin (#828), then verified again from scratch (§4).

Read `git show refactor/p3b-question` before starting. It is the whole
recipe applied once, in about 300 changed lines.

## 0. Settled — do not re-derive

- **Only the implementation moves.** Proto package `reliant.v1`, procedure
  names, `gen/reliant/v1` import paths and behaviour stay identical.
  control-plane imports `gen/reliant/v1`.
- **`internal/grpc/server.go` keeps mounting every service**, through the same
  `reliantv1connect.New<Svc>ServiceHandler(x, opts...)` call, with the same
  `opts` or `machineWakeOpts`, and so the same interceptor chain (timeout,
  auth, domain whitelist, machine-wake, slow-rpc watchdog, error reporter,
  stream drain). Only the value passed in changes. Do **not** switch to
  `x.Register(mux, ...)` and do not touch the interceptor chain: replacing
  server.go is P3c.
- **One forge binary**: `/home/workspace/go/bin/forge`, always run as
  `FORGE_NO_HOOKS=1 /home/workspace/go/bin/forge …`.
  - It must be the version `go.mod` and `forge.yaml` pin. At the time of
    writing that is `v0.1.45-0.20261013082102-d67d2a72225a` (#828).
  - Never the `forge` or `reliant forge` on PATH: they are other versions.
    A forge whose version differs from `forge.yaml`'s `forge_version`
    refuses to `generate`.
  - When main re-pins, rebase first.
  - Below, `$FORGE` means `FORGE_NO_HOOKS=1 /home/workspace/go/bin/forge`.
- **What the handler package looks like** (forge `api` skill):
  - `type Service struct { reliantv1connect.Unimplemented<Svc>ServiceHandler; deps Deps }`
  - `func New(deps Deps) (*Service, error)`
  - the rpc methods are methods on `*Service`.
  - There is no `contract.go`. A handler package is exempt from the contract
    lint.
- **Deps carry `Logger *slog.Logger` and `Config *config.Config`** (from
  `github.com/reliant-labs/reliant/pkg/config`), exactly as forge scaffolds
  them. server.go passes the shared `handlerLogger` / `handlerConfig`.
  - Since forge #702 the generated harness takes a config only when Deps
    does, so a lane *could* drop `Config`.
  - Keep it anyway. It is the seam P4 fills, and the scaffold writes it.
    Dropping it buys nothing while `pkg/config` must exist for the scaffold
    (§1).
- **Public rpcs are declared in their proto** with
  `option (forge.v1.method) = { auth_required: false };` (#829).
  - `forge generate` projects them into
    `pkg/middleware.UnauthenticatedProcedures`, which `NewServer` hands the
    auth interceptor.
  - `internal/grpc/public_procedures_test.go` pins the set: 10 reliant rpcs
    (8 System, `TokenService.{ExchangeToken,GetGitToken}`) plus forge's two
    gRPC-health paths.
  - A lane never adds, removes or moves that option. Moving System kept it
    byte-for-byte.
  - `proto/forge/v1/forge.proto` is vendored on main for that option. Both
    Go buf templates exclude it, because its Go home is
    `forge/pkg/forgepb`. `$FORGE lint --vendored-protos` checks it.
- **Forge sees no tables today**: `$FORGE project shapes --kind table` finds
  none, so no rpc is CRUD (F5, §7). Every rpc is an ordinary custom rpc,
  whatever its name.

## 1. What is already in place (from the System commit)

**Prerequisite:** `refactor/p3b-system` is merged. Every lane branches from
a main that contains it. The Question commit is independent of other lanes.

| Piece | Why a lane needs it |
|---|---|
| `pkg/config/config.go` (empty `Config`) | forge's **service scaffold** (`service.go.tmpl`) still imports `<module>/pkg/config` unconditionally. Verified at d67d2a72: with `pkg/config` deleted, adopting ToolCallService fails `go mod tidy` and rolls back. Forge #702 fixed only the harness half (F13). P4 replaces the file with the generated one. |
| `pkg/middleware/claims.go`: `ContextWithClaims` → `auth.WithUser` | The generated harness authenticates through this symbol. `AuthedContext(t)` and `NewTest<Svc>Server` therefore install a caller that `auth.GetUserIDFromContext` sees, the same way the API interceptor does. Default principal: user `test-user`. |
| `auth.WithUser(ctx, userID, role, email)` | The one setter the interceptor, the HTTP middleware and the harness share. |
| `internal/architecture`: `serverSideTrees = {"internal/handlers"}` | Every package under `internal/handlers/` keeps grpc/services' filesystem ban. A lane adds nothing to that test. |
| `handlerLogger`, `handlerConfig` in `NewServer` | Pass these to every `New`. |
| forge derives the project as service-kind | `generate` now also writes `deploy/observability/**` and `.forge/hashes.json`. Commit what it writes. |

## 2. Before you start: the lane gates

1. **P2 gate.** Removing `// forge:unmanaged` exposes the proto to
   `forge lint --conventions`, whose `forgeconv-list-filter-optional` rule
   fails any List request scalar without presence.

   **State on main `d814199f`:** P2's filter PRs (#830–#841) made 17
   genuine filters `optional`. They deliberately left the *required parents*
   (fields a handler rejects when empty) for a follow-up that declares them
   `[(buf.validate.field).required = true]`.

   I removed every marker and linted. That still gives **28 findings in 15
   protos**, all required parents.

   - **Unblocked by P2:** FileSystem, PackageCommands, Run, Token.
   - **Never gated:** the 14 services in wave 1.
   - **Still gated** until the required-parent follow-up for its proto has
     merged (a P2 decision, never a lane's):

   | Proto | Required parents still failing the lint |
   |---|---|
   | approval | `ListApprovalsByChatRequest.chat_id` |
   | catalog | `ListModelsByProviderRequest.provider` |
   | chat | `ListBranches/ListChatPlans/ListMessages/ListStepExecutionsRequest.chat_id`, `ListChatsRequest.project_id`, `ListQueuedAgentMessagesRequest.{chat_id,thread_id}` |
   | connection | `ListConnectionEventsRequest.id` |
   | forge | `ListForgeCheckoutsRequest.project_id`, `ListForgeSecretsRequest.{env,project_id}` |
   | mcp | `ListRecommendedRequest.project_id`, `ListServersRequest.project_id` |
   | plan | `ListPlansByProjectRequest.project_id`, `ListPlanTasksRequest.plan_id` |
   | project | `ListProjectDaemonsForDaemonRequest.daemon_id`, `ListRepositoriesForDaemonRequest.daemon_id` |
   | repo | `ListReposRequest.project_id` |
   | scenario | `ListScenariosRequest.workflow_slug` |
   | settings | `ListInstalledSkillsRequest.project_id`, `ListRecommendedSkillsRequest.project_id` |
   | task | `ListReadyTasksRequest.plan_id` |
   | trigger | `ListTriggerEventsRequest.trigger_id` |
   | workflow | `ListWorkflowsRequest.project_id` |
   | worktree | `ListWorktreeRepoStatusesRequest.worktree_id`, `ListWorktreesRequest.project_id` |

   To re-check before branching, remove your marker and run
   `$FORGE lint --conventions`. It reads only protos and takes seconds.
   To see every service at once, apply the 3.1 `sed` to all of
   `proto/reliant/v1/*.proto`, lint, then `git checkout -- proto/`.

2. **Ordering.** Respect the "second mover" rules in the lane table (§8),
   e.g. Run before Chat and Run before Inbox.
3. **Branch** `refactor/p3b-<svc>` from current `origin/main` in your own
   worktree. Install the TS plugin once per worktree, or `generate` refuses
   in pre-flight: `(cd tools/protoc-gen-es && npm ci)`.

## 3. Steps

`<svc>` is the package directory forge creates in 3.2: the service name in
snake case, e.g. `question` or `model_endpoint`. Read it off
`internal/handlers/` rather than guessing it for names like `FileSystem` or
`MCP`. `<Svc>` is the proto service without `Service`, e.g. `ModelEndpoint`.

**3.1 Adopt the service.** Delete the three-line marker:

```bash
sed -i "/^\/\/ forge:unmanaged — reliant's own handlers serve this service, not forge's;$/,/^$/d" \
  proto/reliant/v1/<file>.proto
git diff --stat        # exactly 3 deletions in one proto
```

**3.2 Scaffold.** Run `$FORGE generate`. It takes 2–5 minutes, so run it in
the background. It writes:

- `internal/handlers/<svc>/`:
  - `service.go` (yours)
  - one `rpc_<name>.go` stub per rpc
  - one `handlers_scaffold_<name>_test.go` per rpc (yours)
  - `helpers_gen_test.go` (generated)
- `internal/handlers/mocks/<svc>_mock_gen.go`
- `deploy/observability/dashboards/reliant-service-<svc>_gen.json`
- updates to `gen/forge_descriptor.json`, `.forge/{hashes,scaffolded}.json`
  and `deploy/observability/{dashboard-spec,alert-defaults}_gen.json`,
  `…/saved-searches_gen.ndjson`

**3.3 Move the code.** Use `git mv` so review sees renames:

```bash
rm internal/handlers/<svc>/rpc_*.go          # stubs; your moved methods replace them
git mv internal/grpc/services/<file>.go internal/handlers/<svc>/<file>.go   # every file the map lists
```

- Keep file names. forge's one-rpc-per-file layout is optional; it finds
  hand-written methods in any file and does not re-scaffold their stubs
  (verified).
- In each moved file:
  - `package services` → `package <svc>`
  - delete the old struct and `New<Svc>Service`
  - `func (s *<Svc>Service)` → `func (s *Service)`
  - `s.<field>` → `s.deps.<Field>`
- **Rpcs the old service never implemented** answered through the embedded
  `Unimplemented…Handler`. forge re-scaffolds a stub for any rpc with no
  method, and that stub answers `svcerr.ScaffoldStub`: same code, different
  message, plus an `x-forge-error-reason` header. Keep the old wire
  behaviour with an explicit method that delegates to the embedded handler:

  ```go
  // ListPlansByProject was never implemented; it answers exactly as before.
  func (s *Service) ListPlansByProject(ctx context.Context, req *connect.Request[pb.ListPlansByProjectRequest]) (*connect.Response[pb.ListPlansByProjectResponse], error) {
      return s.UnimplementedPlanServiceHandler.ListPlansByProject(ctx, req)
  }
  ```

  On main this applies to:
  - `PlanService.ListPlansByProject`
  - `SettingsService.{InstallSkill, ListInstalledSkills, GetInstalledSkillDefinition, SetSkillEnabled, ListRecommendedSkills, DeleteGlobalSkill}`
  - `TaskService.{AddTaskDependency, RemoveTaskDependency, ListReadyTasks}`
  - `ToolsDaemonService.ConnectGateway`
- `proto_enum_mappers.go` is shared by file only. Cut your service's mappers
  out into your package and leave the rest. Mappers nothing calls are dead:
  delete them.

**3.4 Write `service.go`'s Deps.** Keep forge's scaffold (`Name`,
`Register`, `RegisterHTTP`, the `var _ = pb.File_…` line) and replace the
Deps block. The scaffolded doc comment says composition happens in
`internal/app/compose.go`, which reliant does not have: say that
internal/grpc/server.go constructs it until P3c, as the pilots do. Rules:

- **Consumer-declared interfaces.** Declare in `service.go` the methods the
  package actually calls, as a `Repository` interface. Let the compiler list
  them: start empty and add what `go build` reports.
  - A service that calls more than ~15 `db.Repository` methods keeps
    `Repo db.Repository` for now. Splitting `db.Repository` is P6's job.
  - Other collaborators get one- or two-method interfaces (`Signaler`,
    `Pinger`). Nil-safety is the field's contract.
- **Name the repository field `Repo`, never `DB`.** forge reserves
  `DB orm.Context`: its harness fills a `DB` field, and its CRUD generator
  injects one.
- **Required vs optional.**
  - Required: checked in `validateDeps()`. The harness auto-stubs required
    interface fields; error-only stub methods fail closed with
    `testkit.StubNotConfigured`.
  - Optional (nil is a supported mode today, e.g. Ready's checks or a
    feature that is off): mark it `// forge:optional-dep` on the line above
    the field, and guard every dereference with `if s.deps.X != nil`.
    `forge lint --optional-deps-guard` checks this.
- **`With*` options become Deps fields** (optional where nil was allowed).
  Keep a two-phase `With*` setter only when the collaborator is built
  *after* the service (a cycle). Say why in a comment.
- **Typed nil.** A nil `*T` stored in an interface field is not nil.
  `validateDeps` passes it, and the `!= nil` guards pass too.
  - Where server.go passes a pointer that can be nil, e.g.
    `worktreeSweeper` into Inbox, convert it explicitly:
    `var sw Sweeper; if worktreeSweeper != nil { sw = worktreeSweeper }`.
  - `NewTriggerServiceFor` is the existing example.

**3.5 Wire server.go.** Replace the old construction:

```go
xService, err := <svc>.New(<svc>.Deps{
    Logger: handlerLogger,
    Config: handlerConfig,
    Repo:   database,
    // …
})
if err != nil {
    return nil, fmt.Errorf("<svc> service: %w", err)
}
```

- Keep the `reliantv1connect.New<Svc>ServiceHandler(xService, opts...)` line
  exactly as it is, including `machineWakeOpts` and any condition around it.
- Add the import `github.com/reliant-labs/reliant/internal/handlers/<svc>`.
- A package name can collide with a local variable: `system`, `question`,
  `task`, `chat`, `run`, `forge`. Rename the variable, never alias the
  package.

**3.6 Move the exported API and its importers.**

- Every `services.<Name>` your service exports moves with it (map §"Exported
  API", repeated per lane in §8).
- Find the importers with a plain text search, which also catches
  build-tagged files that `go build` never compiles:

```bash
rg -n 'services\.(New)?<Svc>|services\.<ExportedName>' --type go
```

Build-tagged consumers seen so far:

- `e2e/stories/harness_test.go` (`e2e`)
- `internal/workflow/runtime/replaytest/harness_gen_test.go`
  (`replayfixtures`, hand-written despite the name)
- `internal/grpc/{localmodel,model_endpoint}_e2e_test.go` (`localmodel_e2e`)

Those harnesses import `internal/config` as `config`, so import ours as
`typedconfig "github.com/reliant-labs/reliant/pkg/config"`.

**3.7 Move the tests.**

- `git mv` the service's test files into the package and set
  `package <svc>`. Keep them in-package (`package <svc>`): they reach
  unexported helpers.
- Replace `New<Svc>Service(...)` with `New(Deps{…})`, or with the harness:
  `NewTest<Svc>(t, With<Svc>Deps(Deps{Repo: repo}))` fills
  Logger/Config/stubs. `AuthedContext(t)` replaces
  `context.WithValue(ctx, auth.UserIDContextKey, "test-user")`. Rewrite only
  what the move requires; the pilot's switch to the harness is optional.
- Fixtures shared across services (map §"Test-side coupling"):

  | Fixture | What the lane does |
  |---|---|
  | `toolexectest`, `dbtest.SeedToolCall`, `signaltest`, `workflowlisttest` | Already packages. Import them. |
  | `strPtr`, `ptrStr` | Use `ptr.Of` (one line each). |
  | `authedCtx`, `userCtx` | Inline `context.WithValue(ctx, auth.UserIDContextKey, id)`, or use `AuthedContext(t, testkit.WithUserID(id))`. |
  | A fixture in another service's `_test.go` (`runListFixture`, `placementFixture`, the ToolsDaemon conn doubles) | A `_test.go` declaration is invisible outside its package. A test needing another service's fixture moves with the **second** of the two lanes, and builds the first service through its exported `New` from its new handler package. |
  | `fakeControlPlaneClient` / `mintCall` (Settings, Trigger) | The second of the two lanes copies it. Do not export it: forge would then refuse `controlplane`'s `//forge:exclude-contract`. |

- **Mixed-service test files:**
  - A file that constructs two services moves with whichever lane goes
    second.
  - The first lane leaves it in `internal/grpc/services` and only changes
    its own constructor call to the new package. A `services` test may
    import `internal/handlers/<svc>`; nothing imports back.
  - A file that is really two files (`windows_path_test.go`,
    `daemon_proxy_machine_state_test.go`, `user_error_class_test.go`,
    `config_row_tail_test.go`, `workflow_draft_gate_test.go`) is split by
    service: each lane takes its own tests out.
- **Scaffolded `handlers_scaffold_<rpc>_test.go`:**
  - If a moved test already covers the rpc, delete the file. It does not
    come back.
  - Otherwise, turn its self-destructing row into a real assertion. At
    minimum: one request-validation row and one not-found row through
    `NewTest<Svc>`, which run under `-short`.
  - Never leave a `WantScaffoldStub` row. It fails the moment the method
    exists.

**3.8 Nothing may still need the old type.**

```bash
go build ./... && rg -n '<Svc>Service\b' internal/grpc/services   # expect nothing but other services' text
```

**3.9 Regenerate to a fixed point, then commit.**

```bash
$FORGE generate                 # regenerates helpers_gen_test.go against YOUR Deps (auto-stubs)
git add -A && git commit        # one commit, conventional message
$FORGE generate && git status --porcelain   # must print nothing
```

Then:

- **If the last command printed anything**, run
  `git add -A && git commit --amend --no-edit` and repeat. The pilot needed this once: deleting and later restoring a
  scaffold test leaves `.forge/scaffolded.json`'s `absent_reported` stale,
  and generate rewrites it while still printing "0 changed" (F17).
- **Trust `git status`, not the summary line.**

## 4. Verification (every lane, before handing back)

```bash
gofmt -l internal pkg cmd e2e                       # empty
go build ./...
go vet ./internal/handlers/<svc>/ ./internal/grpc/ # + every package you touched
go vet -tags e2e ./e2e/stories/                    # if you touched a tagged consumer
go vet -tags replayfixtures ./internal/workflow/runtime/replaytest/
env -u RELIANT_DAEMON_TYPE go test -short ./internal/grpc/... ./internal/handlers/... \
    ./internal/serverapi/... ./internal/architecture/
$FORGE lint --contract --no-fix                    # 0 findings
$FORGE lint --conventions                          # passes (run lanes separately)
$FORGE lint --vendored-protos                      # clean
```

The `internal/grpc` short tests include
`TestUnauthenticatedProceduresArePinned`. It must pass unchanged: a P3b lane
never changes the public set.

**DB-backed tests** (`db.SetupTestDB`) skip under `-short`. Run them against
a throwaway Postgres of your own. Never use 5433 or 5434.

```bash
docker run -d --name p3b-<svc>-pg -e POSTGRES_PASSWORD=postgres -p 127.0.0.1:<port>:5432 postgres:17-alpine
docker exec p3b-<svc>-pg psql -U postgres -c 'create database reliant_test'
DATABASE_URL='postgres://postgres:postgres@localhost:<port>/reliant_test?sslmode=disable' \
  REQUIRE_TEST_DB=1 env -u RELIANT_DAEMON_TYPE go test -count=1 ./internal/handlers/<svc>/
docker rm -fv p3b-<svc>-pg
```

If an e2e story drives your service, run it against the same database. It
starts its own Temporal dev server: `go test -tags e2e -run 'Story02|Story11' ./e2e/stories/`.
The pilot ran stories 02 and 11 for QuestionService.

**control-plane** builds against your branch through a temporary workspace
outside both repos:

```bash
mkdir -p /tmp/p3b-gowork && cat > /tmp/p3b-gowork/go.work <<EOF
go 1.27.0
use (
  $PWD/../control-plane
  $PWD/../control-plane/gen
  $PWD
)
EOF
(cd ../control-plane && GOWORK=/tmp/p3b-gowork/go.work go build ./...)
rm -rf /tmp/p3b-gowork
```

**Boot smoke** (recommended for services with non-trivial wiring). The pilot
booted `reliant server api` against throwaway postgres:17-alpine,
`nats:2-alpine -js` and `temporalio/admin-tools:1.26.2` (entrypoint
`temporal server start-dev --ip 0.0.0.0 --headless --namespace reliant`):

```bash
DATABASE_URL=… NATS_URL=nats://localhost:<p> TEMPORAL_HOST=localhost TEMPORAL_PORT=<p> \
DISABLE_TLS=true GRPC_PORT=<p> HEALTH_PORT=<p> PPROF_PORT=<p> BIND_ADDRESS=127.0.0.1 \
DATA_DIR=/tmp/<dir> JWT_PUBLIC_KEY_FILE=<es256 pub.pem> go run ./cmd/reliant server api
curl -s -X POST -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  -d '{…}' http://127.0.0.1:<grpc>/reliant.v1.<Svc>Service/<Rpc>
```

- The auth interceptor accepts an ES256 JWT with `sub`, `email`, `role` and
  `exp`.
- Probe the auth boundary, not just the happy path:
  - Every rpc in the pinned public set, called with no credentials, must
    reach its handler. Token's two rpcs answer with their own
    `unauthenticated` message about an `rlat_` bearer, not the
    interceptor's.
  - A non-public rpc with no credentials must get the interceptor's
    `missing authorization token`.
  - Your rpcs with a token must succeed.
  - Another user's token must get `NotFound` on your rows.
- The server log's `rpc failed procedure=…` lines show the call went
  through the error-reporting interceptor.
- Remove every container you started (`docker rm -fv <name>`) and stop only
  the process you started.

## 5. Merging: rebasing onto a moved main

Every lane touches the same generated aggregates. Rebase onto main
(`git rebase origin/main`; for a stacked lane,
`git rebase --onto <new-base> <old-base> <branch>`) and resolve
mechanically.

**Never hand-merge generated output.**

1. **Generated files: take a side.** Either side works; main's is
   simplest. During a rebase main's side is `--ours`:
   `git checkout --ours <file> && git add <file>`. Then continue the rebase.
2. **Proto bindings.** If one of them conflicted (`gen/reliant/v1/**`,
   `web/src/gen/**`), take a side the same way, then run
   `make proto-generate-reliant`.
3. **Run `$FORGE generate`.** It recomputes every aggregate from the merged
   inputs. Then `git add -A && git commit --amend --no-edit`.
4. **Run `$FORGE generate` again.** `git status --porcelain` must print
   nothing (§3.9).

| File | Resolution |
|---|---|
| `gen/forge_descriptor.json`, `.forge/hashes.json`, `.forge/scaffolded.json`, `deploy/observability/{dashboard-spec,alert-defaults}_gen.json`, `deploy/observability/dashboards/{saved-searches_gen.ndjson,reliant-overview_gen.json}`, `internal/handlers/<svc>/helpers_gen_test.go`, `internal/handlers/mocks/*_gen.go`, `pkg/middleware/procedures_gen.go` | Steps 1, 3, 4 |
| `gen/reliant/v1/**`, `web/src/gen/**` | Steps 1–4 |
| `internal/grpc/server.go` | Hand-merged code. Adjacent constructor lines and imports: keep both sides |
| `internal/grpc/workspace_proxy_test.go` (FileSystem, Background, Terminal, PackageCommands) | Interface assertions on adjacent lines: keep both sides |
| `go.mod` / `go.sum` | Take main's side; step 3 tidies |

The lane's own files (`internal/handlers/<svc>/**` except
`helpers_gen_test.go`, its dashboard) never conflict.

**What the pilot's rebase onto P2 and the d67d2a72 pin hit:**

- **`server.go` imports:** the pilot's `pkg/config` against P2's
  `pkg/middleware`. Kept both.
- **`gen/forge_descriptor.json`**, on both commits: took main's side and
  regenerated.
- **Auto-merged cleanly:** `interceptors/auth.go`, `system.proto` (P2's
  `auth_required` options next to the removed marker), `go.mod` and
  `forge.yaml`.
- **Regeneration then rewrote** the descriptor and both
  `helpers_gen_test.go`, which lost their config import comment block
  under #702.

**Forge fix in flight:** conflict-free generated files between parallel
lanes (descriptor, dashboards, `.forge/*.json`, `.gitattributes`; F16).
When it lands, step 1 should stop being necessary for those files.

## 6. Pitfalls the pilot hit, and their fixes

1. **`generate` fails in pre-flight on protoc-gen-es.** Run `npm ci` in
   `tools/protoc-gen-es` (once per worktree).
2. **`generate` rolls back with "go mod tidy failed … fix go.mod or
   go.sum".** The real cause is generated code importing a package that
   does not exist. Pilot instance: `pkg/config` and
   `pkg/middleware.ContextWithClaims`, now present; do not delete either.
   Read the `go: … imports …: no matching versions` line above the root
   cause (F13, F14).
3. **"internal/handlers/<svc> exists but contains no buildable .go file".**
   Do not create the directory before the first generate. Let the scaffold
   create it.
4. **"0 changed" is not a fixed point.** Check `git status --porcelain`
   (F17).
5. **A `services` identifier in a build-tagged file.** `go build ./...` stays
   green while e2e/replay breaks. Use `rg` and the tagged `go vet` in §4.
6. **`DB` field name.** forge injects or expects `DB orm.Context`; use
   `Repo`.
7. **`config` name clash** with `internal/config` in harness files. Import
   `typedconfig`.
8. **Dual implementations.** FileSystem, Background, Terminal and
   PackageCommands each have a `*Proxy` type and a local type, chosen in
   server.go by `router != nil`.
   - forge allows one `*Service`. Make it the proxy: in the API server the
     router is always the NATS router.
   - First check whether the local branch is reachable in production. The
     only production `NewServer` caller is `serverapi`, which always sets a
     router. If it is not reachable, delete the local implementation in a
     separate preceding commit (reliant removes dead paths).
   - If it is reachable, keep it as an unexported type in the same package
     and have each `*Service` method forward to it explicitly.
   - Never embed a handler interface in `Service`. forge's stub would shadow
     the promoted method, or the selector becomes ambiguous.
9. **Hard-coded paths in `internal/architecture`.**
   - The MCP lane moves the `knownViolations` entry
     `internal/grpc/services/mcp.go` to its new path.
   - The FileSystem lane updates `TestFilesystemService_UsesLocalFS`'s path.
10. **`forge project audit` warns `unscoped_auth`** for every moved
    authenticated rpc. forge recognises only
    `middleware.GetUser`/`ClaimsFromContext` as caller resolution, while
    reliant uses `auth.GetUserIDFromContext`. Advisory; ignore it. A forge
    fix is in flight (F18).
11. **Boot smoke: `localhost` may resolve to `::1`.** The containers publish
    on `127.0.0.1` only, so give `DATABASE_URL`, `NATS_URL` and
    `TEMPORAL_HOST` the address `127.0.0.1`. The pilot's rerun failed with
    `dial tcp [::1]:…: cannot assign requested address` until it did.

## 7. F5 (CRUD detection) once P5a lands

Measured in a scratch copy of `refactor/p3b-question`, with the schema as
`db/migrations/00001_baseline.up.sql` (pg_dump preamble stripped) and
TaskService adopted. Its five Create/Get/List/Update/DeleteTask rpcs collide:
table `tasks` and message `Task` both exist.

| State | What forge does |
|---|---|
| **Today** (no `db/migrations`) | forge sees no tables, so no rpc is CRUD. Verified: all 8 TaskService rpcs, the 5 CRUD-named ones included, get ordinary `rpc_*.go` stubs and scaffold tests |
| **P5a with `//forge:no-orm`** (`internal/db/forge_no_orm.go`) | generate passes. forge scaffolds stubs and scaffold tests **only for the 3 custom rpcs**. The 5 CRUD-named rpcs get **no stub, no shim, no test**, and neither `audit` (`crud_stubs`, `orphan_stubs`) nor `shapes` mentions them; they would silently serve the embedded `Unimplemented`. It also writes `db/embed_gen.go`, `db/source_gen.go` and a `factories_gen_test.go` per adopted service, and the dashboards grow DB panels |
| **ORM on** (P5b removes the marker), stubs only | generate **fails** in CRUD generation: `TaskService.CreateTask: Task.status (enum) <-> tasks.status INT4: no conversion` |
| **ORM on**, all 5 methods hand-written (the post-P3b shape) | generate **fails** at validation: forge projects the entity into reliant's hand-written `internal/db` (`internal/db/task_orm_gen.go`), so `Task redeclared` with `internal/db/models.go:119`. It also announces "CRUD wiring (5 methods)" despite all 5 being hand-written. The run failed before handlers compiled, so whether the shims were really emitted is unobserved |

**Forge fix in flight:** a per-rpc CRUD opt-out, closing the silent no-orm
gap, and ORM placement (F5, F5a, F5b). Re-measure when it lands.

What this means for P3b lanes:

- **Before P5a**, CRUD-named rpcs are scaffolded like any other.
- **After P5a (no-orm)**, a CRUD-named method the lane forgets to move
  produces no stub and no failing test. Lanes that run after P5a must diff
  declared rpcs against implemented methods themselves:
  `$FORGE project shapes --kind rpc,handler --grep <Svc>`, or
  `go doc ./internal/handlers/<svc> Service`.
- **P5b is blocked twice:**
  - by F5: no per-rpc or per-entity opt-out from CRUD detection;
  - by the ORM target package: forge's entity projection collides with
    reliant's `internal/db` types.

  Both are forge work (§9). The affected lanes are the 52 collisions listed
  per service in §8.

## 8. Lane table (the 33 remaining services)

Size is the decl lines from the map: S < 400, M 400–1200, L > 1200.

- **"P2"**: blocked until P2's required-parent follow-up for that proto
  has merged (§2).
- **"F5"**: CRUD-named rpcs with table and message (matters after P5a).
- **"Ext"**: importers outside `internal/grpc/services` besides server.go.

| Service | Size | P2 | F5 | Files (map) | Ext / exported API | Ordering, grouping, notes |
|---|---|---|---|---|---|---|
| Account | S | – | – | `account.go` | – | Mounted only with a SQL repo (`database.(*db.Repo)`); Deps take `*sql.DB`. Tests: `account_test.go`, `account_controlplane_test.go` |
| Approval | M | P2 | – | `approval.go` | e2e harness | |
| Attachment | S | – | 2 | `attachment.go`, `attachment_content.go` | `AttachmentContentHandler`, `AttachmentContentPathPrefix` (server.go mux) | Moves its HTTP handler too |
| Background | M | – | – | `background.go`, `background_proxy.go`, `background_provider{,_db}.go`, its `proto_enum_mappers` | `BackgroundProcessProvider` (server `Config` field type), `NewDBBackgroundProcessProvider` (serverapi) | Dual impl (§6.8). Splits `daemon_proxy_machine_state_test.go` (with Daemon, Terminal) and `background_proxy_timeout_test.go`; `workspace_proxy_test.go` assertion |
| Catalog | M | P2 | – | `catalog.go`, `catalog_search.go` | `IntegrationMethodsFrom` (server.go); `localmodel_e2e_test.go` | Two-phase `WithTagPrefs`, `WithCatalogSearch` → Deps. Second mover of `model_endpoint_test.go` if after ModelEndpoint |
| Chat | L (5.6K) | P2 | 6 | the 12 `chat_*.go` + `chat.go` | serverworker, e2e harness + `main_test.go`, replaytest | **After Run.** `agent_message_test_helpers_test.go`, `list_messages_batch_test.go` are Chat's. Second mover of `tool_call_status_read_test.go`, `workflow_list_chat_contract_test.go`, `streaming_snapshot_enrichment_test.go`, `run_signal_pending_test.go` (whichever goes second). Keeps `Repo db.Repository` (>15 methods). Consider splitting into its own sub-lanes only by file, never by rpc |
| Connection | S | P2 | 4 | `connection.go` | – | |
| Connector | M | – | – | `connector.go`, `connector_consent.go` | – | Mounted only with a SQL repo |
| Daemon | S | – | – | `daemon_proxy.go` | – | Splits `daemon_proxy_machine_state_test.go` |
| DaemonRegistry | M | – | – | `daemon_registry.go` | `localmodel_e2e_test.go` | `daemon_registry_disk_test.go`, `daemon_registry_localmodel_test.go`, `tools_daemon_lease_test.go` reach ToolsDaemon internals: they **move with ToolsDaemon**. If DaemonRegistry goes first they stay in `services` and import the new package |
| FileSystem | L | – | – | `filesystem.go`, `fs_proxy.go`, `fs_proxy_checkout.go`, its mappers | – | Dual impl (§6.8); proxy mounted with `machineWakeOpts`. Architecture test path (§6.9). With **Worktree**: the second of the two takes `machine_wake_test.go`, `fs_proxy_routing_test.go`. Splits `windows_path_test.go`, `user_error_class_test.go`; `workspace_proxy_test.go` assertion |
| Forge | M | P2 | – | `forge.go` | `ForgeRPCDeadlines` (server.go timeout interceptor, before the services are built) | Mounted only with router and not `forgeAPIDisabled()`. Package name `forge`: rename local variables |
| Inbox | M | – | – | `inbox.go` | – | **After Run** (its tests need `runListFixture`: rebuild it from `run.New`). `WithSweeper(worktreeSweeper)` passes a possibly-nil `*worktreesweep.Sweeper`: typed-nil (§3.4). Second mover of `trigger_gmail_e2e_test.go` if after Trigger |
| MCP | L | P2 | – | `mcp.go`, `mcp_status_cache.go`, `mcp_daemon_proxy.go` | – | Moves the architecture `knownViolations` entry (§6.9) |
| Message | S | – | 1 | `message.go` | – | `tool_call_status_read_test.go` (Message, Chat): second mover |
| ModelEndpoint | S | – | 4 | `model_endpoint.go` | `model_endpoint_e2e_test.go` (`localmodel_e2e`) | `model_endpoint_test.go` (ModelEndpoint, Catalog): second mover |
| PackageCommands | M | – | – | `package_commands{,_proxy}.go`, its mappers + `proto_enum_mappers_test.go` | `internal/grpc/workspace_proxy.go` (`pickPackageCommandsService`) | The proxy **embeds** the local service and serves through it: fold both into one `Service` with explicit forwarding |
| Plan | S | P2 | 4 | `plan.go`, its mappers | – | `ListPlansByProject` is unimplemented: delegate explicitly (§3.3) |
| Preset | M | – | – | `preset.go` | – | `workflow_draft_gate_test.go` (Chat, Preset, Scenario): split |
| Project | L | P2 | 6 | `project.go`, `project_from_repo.go`, its mappers | – | `project_from_repo_test.go`, `project_install_resolution_test.go` also build ToolsDaemon: second mover. Splits `windows_path_test.go` |
| Repo | S | P2 | 1 | `repo.go` | – | |
| Run | M | – | – | `run.go` | serverworker; `internal/agentruns` (`services.RunService`) | **Before Chat and Inbox.** Declare a 6-method `Chat` interface (StartChat, SendMessage, PauseChat, ResumeChat, TerminateChat, InterruptThread) in `run`; server.go and serverworker pass `*services.ChatService` until Chat moves |
| Scenario | M | P2 | – | `scenario.go`, `scenario_workflow_loader.go` | – | Splits `config_row_tail_test.go` (with ToolsDaemon), `workflow_draft_gate_test.go` |
| Settings | L | P2 | 5 | `settings.go`, its mappers | – | 6 skill rpcs are unimplemented: delegate explicitly (§3.3). `provider_modalities_test.go` is Settings'. `fakeControlPlaneClient`: the second of Settings/Trigger copies it |
| Streaming | M | – | – | `streaming.go` | – | `streaming_snapshot_enrichment_test.go` builds Chat: second mover |
| Task | S | P2 | 5 | `task.go` | – | 3 dependency rpcs are unimplemented: delegate explicitly (§3.3). The F5 experiment service (§7) |
| Terminal | M | – | – | `terminal.go`, `terminal_proxy.go`, `terminal_ws.go` | `TerminalWSHandler`, `WithTerminalCheckouts` (server.go `/api/v2/terminal/ws`) | Dual impl; `NewTerminalService` (local) has **no production caller**: delete it, after checking which tests still use it. Splits `daemon_proxy_machine_state_test.go`; `workspace_proxy_test.go` assertion |
| Token | M | – | – | `token.go`, `token_exchange.go`, `token_git.go` | `TokenControlPlane` (server.go, `cmd/reliant/commands/forge_credential_test.go`, `internal/grpc/token{,_exchange}_e2e_test.go`) | Two-phase `WithGitTokens` → optional Dep. Its rpcs `ExchangeToken`/`GetGitToken` declare `auth_required: false` in `token.proto` (#829) and authenticate their own bearer: keep both untouched; `TestUnauthenticatedProceduresArePinned` must still pass |
| ToolCall | S | – | – | `tool_call.go` | – | Fixtures already in `toolexectest`/`dbtest`/`signaltest` |
| ToolsDaemon | L (2.9K) | – | – | `tools_daemon{,_reconcile,_localmodel}.go` | **Not mounted in server.go**: `internal/grpc/daemon_server.go`, `internal/servergateway` (`ToolsDaemonService`, `HeldDaemonStream`, 22 uses) | `ConnectGateway` is unimplemented: delegate explicitly (§3.3). Takes the three DaemonRegistry-mixed tests, `forge_name_learn_test.go`, its half of `config_row_tail_test.go`, `user_error_class_test.go`, `windows_path_test.go`. No ordering constraint of its own |
| Trigger | L | P2 | 6 | `trigger.go`, `trigger_inbound.go`, `trigger_automation.go`, `trigger_senders.go` | `internal/triggertools` (production), serverapi (`SenderDirectory`, `ResolvedSender`, `IntegrationAccess`), serverworker (`InboundOptions`), e2e harness, server `Config.TriggerSenders` | `NewTriggerServiceFor` encodes a typed-nil guard: keep it in `New`. Two-phase `WithInbound`/`WithPolledIntegrations`/`WithSenderDirectories` → Deps |
| Workflow | L | P2 | 3 | `workflow.go`, `workflow_validation.go`, `workflow_catalog.go`, `workflow_convert.go` | – | `workflow_list_chat_contract_test.go`: second of Workflow/Chat |
| Worktree | L | P2 | 5 | `worktree.go`, `worktree_git_ops.go`, `worktree_placement.go`, its mappers | – | Mounted with `machineWakeOpts`; two-phase `WithSettler`. FileSystem pairing (above) |

**Suggested waves.** Lanes run in parallel; only their merges serialize,
through §5's regenerate.

| Wave | Lanes | Gate |
|---|---|---|
| 1 | Account, Attachment, Background, Connector, Daemon, DaemonRegistry, FileSystem, Message, ModelEndpoint, PackageCommands, Preset, Run, Streaming, Terminal, Token, ToolCall, ToolsDaemon | None (the System commit merged) |
| 2 | Inbox (after Run) | Ordering |
| 3 | Approval, Catalog, Chat (also after Run), Connection, Forge, MCP, Plan, Project, Repo, Scenario, Settings, Task, Trigger, Workflow, Worktree | Each waits for P2's required-parent follow-up on its proto (§2) |

The mixed-test rules in §3.7 hold whatever order the lanes actually land
in.

Budget about 15 minutes of `generate` per lane: at least three runs, each
2–5 minutes.

After the last lane (a separate cleanup commit):

- delete `internal/grpc/services`, its `serverSidePackages` entry and
  `TestFilesystemService_UsesLocalFS`'s old path;
- delete what remains of `proto_enum_mappers.go` (43 dead lines in the map);
- update `forge.yaml`'s header comment.

`HeldDaemonStream`, `AttachmentContentHandler` and the like keep their names
in their new packages; only the package qualifier changes.

## 9. Forge friction found by the pilot

Fix these in forge, not around them. F1–F12 are PLAN.md's. "In flight"
means a forge lane is on it and it has not landed. Do not work around those.

| # | Gap | Status (main `d814199f`, forge d67d2a72) | Where it bit |
|---|---|---|---|
| F13 | The handler scaffold and its generated harness hard-require `<module>/pkg/config.Config` even when the project has no `proto/config`. The rollback blames `go.mod`/`go.sum` ("go mod tidy failed") instead of naming the missing package | **Half fixed.** The harness follows `Deps.Config` (forge #702). `service.go.tmpl` still imports `pkg/config` unconditionally; that half is filed and **open** | `pkg/config` must stay until the template is fixed |
| F14 | The generated harness hard-requires `<module>/pkg/middleware.ContextWithClaims`, which nothing scaffolds when an in-place cli-kind project adopts its first service | **Open** | Bridged by `pkg/middleware/claims.go` → `auth.WithUser`, which is the right seam regardless |
| F15 | The project kind flips cli → service when `internal/handlers/` first appears, silently. That enables observability dashboards, hot_reload and the KCL-reading audits, and `forge.yaml` cannot override features | **In flight** (kind flip made explicit) | `deploy/observability/**` in the System commit |
| F16 | Aggregated generated files serialize parallel lanes. The `forge-generated` merge driver covers only fingerprint lines, in-place adoption writes no `.gitattributes`, and `FORGE_NO_HOOKS=1` disables the driver's registration | **In flight** (conflict-free descriptor, dashboards, `.forge/*.json`, `.gitattributes`) | Every rebase (§5) |
| F17 | generate's "N changed" summary omits `.forge/scaffolded.json` (`absent_reported`) | **In flight** | Fixed-point checks use `git status` |
| F18 | The `unscoped_auth` audit cannot learn the project's own caller resolver (`auth.GetUserIDFromContext`) | **In flight** | Every moved authenticated rpc warns |
| F5a | With `//forge:no-orm` and tables, a CRUD-named rpc gets no shim, no stub, no test and no audit finding | **In flight** | Lanes after P5a (§7) |
| F5b | With the ORM on, entities project into reliant's hand-written `internal/db` (`Task redeclared`), and there is no per-rpc/per-entity CRUD opt-out | **In flight** (per-rpc opt-out, ORM placement) | P5b blocker |
| F19 | `forge project shapes --kind table` names every table `public` for `CREATE TABLE public.x` | **In flight** | Recon after P5a |
| F20 | Minor output nits: the mock path in the generate log, the "pristine older forge render" wording, `forge project libraries tdd.Case` | **Open** | Noise |

Worked as designed and is worth keeping:

- `// forge:unmanaged` adoption one service at a time (F4).
- Hand-written methods in any file suppress the per-rpc stubs.
- Deleted scaffold tests stay deleted.
- The harness auto-stubs required interface Deps and fails closed on
  error-only methods.
- `forge:unmanaged` exempts a proto from the conventions lane until it is
  adopted.
