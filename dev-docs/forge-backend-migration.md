# Backend/tooling Forge equivalence ledger

## Baseline and scope

Audited 2026-11-08 after fetching and fast-forwarding clean worktrees to
Reliant `fb63f15dd` and control-plane `41f9c70a0`. This supersedes the **state
claims**, not the design history, in `research/forge-migration/`.

The target is scaffold-equivalent infrastructure and extension seams, not
replacing product workflows with generic CRUD or making every file's name
match a newly scaffolded app. Reliant is a launched v1 product: CLI commands,
RPCs, configuration, daemon protocol and persisted workflows remain compatible.

Parallel ownership: control-plane ORM migration (including #2048), frontend,
serialization analyzer, and pipeline re-review are **not this slice**. Do not
reopen the work already merged in control-plane #2036/#2037/#2039 or Reliant
#2273/#2276/#2277. Those removals, implementations and error conversions are
present on the audited main commits.

### Method and limits

Read each project's **pinned** `architecture`, `services`, `api`, `testing`,
`dev`, `deploy` skills and `project capabilities` / `project libraries`.
Pins: Reliant `v1.2.1-0.20261108035756-6d8c0ab6008f`; control-plane
`v1.2.1-0.20261108014235-88cb28c2f5ab`. The six skill bodies were identical.
`project libraries lifecyclekit observe` confirms the supervision/telemetry
extension APIs; no replacement framework is needed.

Ran `reliant forge project audit --json` in each checkout, then checked source
behind material findings. This is static inspection, **not** a full lint,
generation-convergence proof, live dev/deploy exercise, production telemetry
freshness check or exhaustive authorization review. No full gates or deploys
were run. Audit can regenerate the descriptor; both working trees stayed clean
after it. On this run stdout had a buf-resolution line before its JSON (reported
as Forge defect `cb2a3cb7-4c6a-4c16-8d3d-91b1ec63ff40`); do not consume that
capture as pristine JSON or interpret failure to parse as an app finding.

## Already equivalent — retain, do not rebuild

| Concern | Verified evidence / boundary |
| --- | --- |
| Handler shape and Deps | `internal/handlers/project/service.go` has `Deps`, constructor validation, a pb-through `*Service`, generated unimplemented embedding and registration. Consumer-owned repository roles live in `deps.go`. Optional collaborators are explicitly marked. Audit finds implementations for every managed RPC, no orphan stub services, no CRUD-shape stubs and no scaffold TODO markers (37 services). Presence is not proof of business correctness. |
| Composition | `internal/app/roots_{api,worker,gateway}.go`, owned `compose.go`/provider files and generated `graph_gen.go`/`routes_gen.go` already use Forge roots. `cmd/reliant/cmd/serve.go` is the owned process policy; `roots_gen.go` preserves `server api`, `server worker`, `server gateway`. `roots_api_test.go` and neighboring root tests pin routing, reach and stop ordering. No migration back to `Infra` is appropriate here. |
| Errors | Both handler Connect-error and per-handler error-mapper rules are enforced at error in `forge.yaml`. `internal/grpc/user_chain.go` uses `serverkit.NewChain`, which installs `svcerr.Interceptor` innermost. Unknown causes are redacted by the library, not exposed as raw driver text. #2276/#2277 already replaced the historical patterns. |
| Authentication | `internal/grpc/user_chain.go` requires an auth interceptor, wraps it as the user audience and feeds generated `middleware.UnauthenticatedProcedures` into `NewUserAuth`. Product JWT/API-key/`rlat_` resolution and caller context are app seams, not missing Forge auth. `internal/handlers/project/rpc_create_project.go` resolves `auth.MustGetUserID`; project methods pass caller IDs into repository operations. Daemon listener authentication is separate, intentionally. |
| Generated ownership | Audit: 570 certified files, zero modified/missing/orphan files; one declared disowned frontend hook barrel (frontend owner). `gen/`, stores, graph/routes, config and mocks remain generated. Do not hand-edit them. A descriptor being current is narrower than all generators converging. |
| Config/codegen | `proto/reliant/config/v1`, generated `pkg/config`, `config.LoadReach` and `serverkitConfig` supply per-role typed config and process validation. Audit finds no scalar Deps config findings. `forge.yaml` explicitly declares non-Forge generators as ordered `generate.hooks` with write sets (tools, schemas, integrations, etc.); these are valid extension seams, not dead sqlc. |
| Database | `forge.yaml` projects applied migrations into `internal/store`; `internal/db` remains the app repository facade. #2273 removed dead statement-executor/sqlc leftovers. Custom SQL in owned `*_repo_ext.go` is a supported Forge path, not a migration failure. |
| Dev/deploy | `deploy/kcl/workloads.k` declares builds, commands, probes, resources and migration job once; `dev/main.k` binds the standalone product stack, `demo/main.k` the demo. Three product roles are deliberate. control-plane imports sibling workloads at its pin, rather than Reliant inventing a second platform prod environment. `.air.{api,worker,gateway}.toml` provide hot reload. See `dev-docs/forge-demo-cutover.md` and control-plane `deploy/kcl/workloads.k` sibling section. |
| Tests | `Taskfile.yml` offers scoped cached `test:short`, affected selection, full package, integration and e2e lanes; `sdk/go` is its own module with explicit test tasks. Generated handler helpers/mocks coexist with real product tests. `testkit`/shared Postgres and slow-test marking are the paved path. No need to convert every meaningful hand-written assertion into a scaffold row. |
| Production telemetry | `internal/observability/observability.go` delegates SDK setup to Forge `observe.Setup`; the process installs it before constructing clients and flushes after supervision. `user_chain.go` supplies RPC trace/log/metrics/recovery/request-id layers with product classification; NATS/Temporal propagation remains in `internal/observability/{propagation,temporal}.go`. Product metrics and Prometheus registry are necessary additions, not a second OTLP stack. This verifies wiring, not delivery from live prod. |

## First bounded fix in this slice

`runRoot` initialized telemetry before schema migration, but handed its only
flush callback to the root supervisor **after** migration. A migration error
returned without flushing; a failure before supervision could likewise bypass
it. The process now immediately defers the **same once-only callback** it passes
to supervision. Running roots still flush after their nodes stop; early failures
also flush; shutdown retains the Provider's independent 10-second context rather
than using the canceled process context. No command, config, wire or schema
changes. `serve_telemetry_test.go` tests the two cleanup paths, error retention,
and the owned wiring's registration order without starting an exporter or DB.

## Prioritized remaining slices

1. **Control-plane startup composition/lifetime (design required).** It still
   uses `Infra`/`NewComponents`/`ServeSpec`, not generated roots. Its serve path
   constructs infra/components before `serverkit.Run` starts telemetry. A
   candidate must map all commands, optional clients, shared-state ownership,
   session-backed leases and failure cleanup before converting. See the sibling
   checklist `control-plane/docs/forge-backend-migration.md`. Do not run a broad
   codemod and assume behavioral equivalence.
2. **Control-plane typed Deps and optional contracts.** Audit reports 174 scalar
   Deps fields across 72 packages and 145 optional-dep dereference findings across
   25 packages. These are triage lists, **not 319 proven defects**. Start one
   package at a time: separate real configuration from instance identity/runtime
   data; prove constructor invariants and deployed variants before making a
   collaborator required or adding a guard. Never silently return success from
   an operation missing its required collaborator. Coordinate with ORM owner.
3. **Reliant user-chain policy parity (owner decision).** `user_chain.go`
   deliberately selects `NoValidate`, `NoAudit`, and unbounded Connect payloads
   (`ReadMaxBytes`/`SendMaxBytes = -1`). Thus it is not fully default-policy
   equivalent even though it uses the scaffold mechanism. Enabling proto rules
   or Forge's default cap can reject existing v1 clients and attachment/file
   flows. Auditing needs a principal adapter plus retention/privacy/delivery
   decisions, not Forge's default anonymous principal. Inventory real methods,
   payloads and validation rules, then propose a compatible rollout with tests.
4. **Authorization analyzer integration, not blind auth rewrites.** Reliant's
   audit warns all 331 authenticated methods lack `middleware.GetUser`; it does
   not recognize `auth.MustGetUserID`. control-plane's eight owner-scoped errors
   include `deploy.callerOrgID` and an app-wired secret access authorizer; source
   proves these resolve callers and scope org IDs. Check each call path and
   negative tests before marking resolvers or documenting genuinely global RPCs.
   No blanket suppression, no finding count presented as a tenant escape.
5. **Test/telemetry closure evidence.** Scoped tests accompany each migration;
   live collector continuity, startup failure diagnostics, shutdown flush, NATS
   and Temporal parent propagation need targeted runtime proofs when those
   boundaries change. Static generated-file certification cannot prove these.
   Full release gates remain the designated pipeline operator's work.

### Necessary app-specific seams — not backlog

- CLI and daemon own user filesystem access; API/worker/gateway do not. Keep the
  daemon/NATS routing and retry/recovery contract; no generic CRUD on workflows,
  tool execution, chat recovery, billing or deploy orchestration.
- Temporal workers, workflow/activity semantics and domain-specific long-lived
  stream drains remain owned components under Forge supervision.
- Per-audience auth, machine wake, RPC deadline exceptions, product error classes,
  update feeds, proxy routes, platform K8s operators, and SQL query extension
  seams are intentional. Reduce duplicate infrastructure, not product semantics.
- Release-train/re-review machinery is owned elsewhere; no changes here.

### Missing Forge capability / tooling friction

No missing primitive was established for lifecycle, middleware, typed config,
ORM extension SQL, multi-root binaries, telemetry, or declarative deploy. Forge
already has `fix composition-graph`, `fix narrow-deps`, `fix connect-errors`,
`--config-deps`, `--optional-deps-guard`, `--tracing`, `--graph` and explicit
caller-resolver markers. Applicability is not permission to mass-rewrite.
The audit JSON stdout contamination above is a genuine tool defect, reported
rather than normalized into project tooling. Historical audit prose is not
proof that Forge lacks a capability today.

## Decisions needed before broader implementation

- **CP roots:** migrate the existing explicit composition incrementally onto
  Forge roots (recommended: generated reach/boot/close ownership; larger reviewed
  lifecycle diff), or retain its legacy composition temporarily and harden its
  owned boot/lifetime explicitly (smaller changes, continuing manual reach work).
  Preserve command surfaces either way. Owner must choose before implementation.
- **Reliant policy:** retain v1-compatible behavior while adding per-method
  bounds/validation and a principal-aware audit rollout (recommended), versus a
  global default switch that risks breaking existing clients. Decide the product
  payload and audit/privacy contract first; a migration assignment is not approval.

## Maintenance / acceptance

For each slice record exact base, changed owned inputs, intentional seams and
scoped build/test commands in its PR. Generate only when inputs require it and
commit generated results; never patch generated output. Re-fetch main before
claiming a remaining item. Keep this ledger and the CP checklist linked; remove
resolved candidates with the landing PR reference. No whole gates, queue labels,
merge commands or polling are part of an author slice.
