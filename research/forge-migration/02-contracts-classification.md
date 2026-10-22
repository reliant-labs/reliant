# 02 — Contracts classification (P6 input)

Research only; no code changed. Checkout: `fm-plan-ab0d8ac0/reliant` (main). forge `v0.1.45-0.20261011192000-6c0c3edb1322` (`reliant forge version`).
Counts come from a python scan of non-test `.go` files plus `go list` (scripts reproduced in §6). Feeds PLAN.md §4 P6 and depends on P5b.

## 0. Headline numbers (measured)

| fact | value | how |
|---|---|---|
| top-level `internal/*` dirs with Go | **106** (the brief says 104; `bgprocess`, `permission` and `envutil` exist, the last two with 0 importers) | scan |
| leaf Go packages under `internal/` (`go list ./internal/...`) | **214** (204 dirs with non-test Go) | go list / find |
| existing `contract.go` | 12 (accesstokenclient, gitcredentialclient, integrations/ghusers being fixed in PR #740) | `find internal -name contract.go` |
| `db.Repository` methods | **442** in one interface (`internal/db/repository.go:13`, 934 lines) | regex count |
| …of which already declared on a `core.*Store` | 313; 129 are only on Repository | cross-ref `internal/db/core/*.go` |
| …used outside `internal/db` | 286 distinct; **156 never called outside db** (dead or db-internal) | call-name scan |
| `pgdb.Querier` (sqlc) | 350 methods (`internal/db/postgres/generated/querier.go:13`) | — |
| test-local stub types `type (fake|stub|mock|spy|recording)…` | 224 in `_test.go` files | grep |
| test structs embedding `db.Repository` (partial fakes that panic on unexpected calls) | 36 | grep `^\s+db\.Repository$` |
| `*fake*`/`*mock*` files | 8 (`triggers/fake_test.go`, `integrations/{gmail,webhook}/fake_test.go`, `llm/drivers/local/inventory_fake_test.go`, `llm/tools/websearch_mock_test.go`, `workflow/runtime/mocked_compaction.go`, `workflow/reference/stub.go`, `workflow/scenario/runner/blackbox_ref_mock_test.go`) | find |

## 1. Package table (top-level unit; multi-package units are noted)

Category key: **S** single-impl service → `contract.go` · **M** multi-impl strategy (the interface is the contract) · **A** adapter `// forge:outbound-io` · **P** pure → `//forge:exclude-contract: <why>` · **T** test-support · **W** worker/supervised (`internal/workers`-shaped, exempt) · **C** composition/wiring (moves into `internal/app`/`cmd` in P3c/P7) · **H** handler layer (P3 owns it) · **D** data layer (P5b owns it).
Exported column: exported funcs+methods / exported types / exported interfaces. IO column: imports detected (`http`, `pgx`, `sql`, `nats`, `connect`, `grpc`, `temporal`, `exec`). Consumers: the count of distinct importing units, with the first 6.

| package | #pkgs | LOC | cat | exported f/t/i | contract.go | IO | consumers | notes |
|---|---|---|---|---|---|---|---|---|
| accesstokenclient | 1 | 504 | A | 12/10/2 | Y | http | 3: grpc llm tokenauthority | has contract.go, non-canonical `New` (client.go:43) — PR #740 |
| accountpurge | 1 | 309 | S | 2/1/0 | - | sql | 1: grpc | deletes across DB; one entry point |
| agentruns | 1 | 204 | S | 6/3/2 | - | connect | 1: serverworker | Connect client to own API → service, not adapter |
| analytics | 1 | 1368 | A | 55/19/2 | - | http | 5: auth grpc serverapi serverworker workflow | outbound HTTP (statsig/posthog-style); 55 exported funcs → narrow first |
| askuser | 1 | 226 | P | 5/4/0 | - | – | 3: cmd/reliant execfollow workflow | question vocabulary/formatting |
| attachment | 1 | 470 | P | 10/1/0 | - | – | 4: grpc llm threads workflow | attachment shaping; no I/O |
| auth | 3 | 2752 | S+A | 39/13/2 | - | exec,http | 13: cmd/reliant accesstokenclient agentruns automationcred forgecred grpc | 3 pkgs: auth (JWT/session service), oauthcallback+oauthhelper = adapters (http) |
| automationcred | 1 | 64 | S | 4/1/0 | - | sql | 3: grpc serverworker workflow | SQL-backed; 1 private iface |
| bgprocess | 1 | 384 | W | 3/0/0 | - | – | 0:  | background process supervisor; no importers outside cmd wiring |
| builddefaults | 1 | 93 | P | 1/0/0 | - | – | 3: cmd/reliant auth daemoninstance | ldflags defaults |
| cel | 1 | 387 | P | 16/3/0 | - | – | 0:  | CEL helpers |
| certs | 1 | 198 | P | 2/1/0 | - | – | 2: serverapi servergateway | cert loading (local files) |
| cgroupmem | 1 | 373 | W | 13/5/0 | - | – | 3: daemon llm toolexec | Run loop (watcher) |
| chatmarkers | 1 | 191 | P | 5/1/0 | - | – | 2: llm workflow | marker parsing |
| cliauth | 1 | 386 | S | 17/3/1 | Y | exec,http | 3: cmd/reliant forgecred grpc | contract.go exists; Deps.Dirs concrete (lint ✗ contract.go:65); CLI-only → F2 |
| cmdutil | 1 | 294 | P | 5/1/0 | - | exec | 4: daemon grpc mcp toolexec | exec arg helpers; os/exec = local, not outbound |
| config | 1 | 1338 | S | 27/31/3 | - | sql | 11: configadapter daemon grpc launch llm mcp | product config store (sql); 31 types → split vocab vs store |
| configadapter | 1 | 57 | C | 2/0/0 | - | – | 2: grpc serverworker | glue between config and db |
| connections | 1 | 2868 | S | 70/29/3 | Y | http | 4: grpc integrations serverapi serverworker | contract.go exists (canonical-ish); 6 private ifaces; 16 tests |
| connectorgrant | 1 | 752 | S | 18/7/2 | Y | sql | 2: grpc mcpserver | contract.go exists; Store iface contract.go:128 |
| controlplane | 1 | 407 | A | 10/10/1 | - | connect,http | 5: grpc llm mcpserver serverapi serverworker | Connect/http client to control-plane |
| copypath | 1 | 221 | P | 3/2/0 | - | – | 4: grpc llm toolexec workflow | path copy logic (local fs) |
| ctxkeys | 1 | 38 | P | 0/2/0 | - | – | 1: llm | context keys |
| daemon | 3 | 3776 | M | 66/47/6 | - | exec | 5: llm mcp rctx toolexec workflow | 3 pkgs; Client/Executor/FileSystem/CommandSender ifaces with local+remote impls |
| daemonactivity | 1 | 421 | A | 8/2/1 | - | nats | 2: grpc servergateway | NATS publisher |
| daemonevents | 1 | 223 | A | 7/3/0 | - | nats | 3: grpc queueddelivery servergateway | NATS publisher |
| daemoninstance | 1 | 359 | P | 10/1/0 | - | – | 2: cmd/reliant toolexec | instance id/paths |
| daemonliveness | 1 | 183 | A | 2/2/1 | - | nats | 1: toolexec | NATS + local Repository iface (reachable.go:61) |
| daemonoffline | 1 | 88 | P | 2/0/0 | - | connect | 1: workflow | connect error classification |
| daemonpolicy | 1 | 1105 | P | 13/3/0 | - | – | 4: connectorgrant llm mcpserver toolexec | policy evaluation, 1.1k LOC, no I/O |
| daemonquery | 1 | 489 | A | 14/6/2 | - | nats | 2: daemonliveness servergateway | NATS request/reply |
| daemonstate | 1 | 565 | W | 7/4/1 | - | nats | 2: grpc servergateway | NATS + Run loop |
| db | 5 | 40407 | D | 1284/548/26 | - | pgx,sql,temporal | 36: cmd cmd/reliant agentruns automationcred bgprocess configadapter | see §2; 5 pkgs incl. sqlc `generated` (16.5k LOC) — P5b owns |
| debugserver | 1 | 49 | C | 1/0/0 | - | http | 2: serverapi serverworker | pprof server wiring |
| diff | 1 | 36 | P | 1/0/0 | - | – | 1: llm | 36 LOC |
| diskwatch | 1 | 1842 | W | 24/9/0 | - | exec,http | 5: daemon grpc llm toolexec worktreesweep | Run loop + exec/http |
| drain | 1 | 190 | C | 9/2/0 | - | http | 3: serverapi servergateway serverworker | http drain helper → serverkit in P7 |
| envutil | 1 | 28 | P | 2/0/0 | - | – | 0:  | 28 LOC, 0 importers → candidate delete |
| errclass | 1 | 70 | P | 4/0/0 | - | temporal | 4: grpc logging telemetry workflow | error classification |
| execfollow | 1 | 1420 | S | 11/11/1 | - | exec | 2: cmd/reliant tools | exec follower; CLI side |
| features | 3 | 1294 | M | 76/11/3 | - | http | 1: llm | Provider/Registry ifaces; providers subpkg = impls |
| filepreview | 1 | 428 | P | 7/3/0 | - | – | 3: grpc llm toolexec | preview rendering |
| filetree | 1 | 569 | P | 4/3/0 | - | – | 3: grpc mcp toolexec | local fs walk |
| fileutil | 1 | 301 | P | 4/2/0 | - | exec | 2: daemon toolexec | local fs |
| forgecred | 1 | 587 | S | 4/6/2 | Y | connect | 1: cmd/reliant | contract.go exists; CLI-only → F2 |
| gitcredentialclient | 1 | 214 | A | 9/6/1 | Y | http | 4: grpc integrations serverapi serverworker | contract.go non-canonical (client.go:36) — PR #740 |
| gitref | 1 | 72 | P | 2/0/0 | - | – | 2: gitutil grpc | ref parsing |
| gitutil | 1 | 784 | P | 16/1/0 | - | exec | 1: toolexec | local git via exec (not outbound) |
| grpc | 3 | 44149 | H | 490/65/3 | - | connect,http,nats,sql,temporal | 3: serverapi servergateway serverworker | handler layer: P3 owns; 77 files use db.Repository |
| instanceid | 1 | 438 | P | 7/0/0 | - | – | 6: cmd/reliant daemoninstance llm temporal toolexec workersetup | id derivation |
| integrations | 12 | 9298 | S/A | 146/74/19 | Y | http | 8: connections grpc llm serverapi serverworker triggers | 12 pkgs: ghaccess/ghusers/webhook have contract.go; gmail,httpaction,connauth,ghdelegated = A; catalog,manifest,tmpl,catalogindex = P/S |
| launch | 1 | 2564 | S | 40/25/11 | Y | temporal | 4: agentruns grpc serverworker triggers | contract.go exists; 11 exported ifaces in store.go:17-85 (consumer-shaped already) |
| llm | 31 | 62947 | M/A | 1048/393/30 | - | exec,http,sql,temporal | 24: cmd/reliant agentruns bgprocess config daemon db | 31 pkgs; drivers/* = A (one per provider), drivers/registry = strategy registry, tools = strategy (Tool iface), models = P |
| localfs | 1 | 52 | P | 11/2/1 | - | – | 2: filetree grpc | 52 LOC; FS iface (1) w/ single impl |
| localprobe | 1 | 658 | A | 15/5/0 | - | http | 3: llm modelendpoints toolexec | http probes of local model servers |
| logging | 1 | 1485 | P | 37/6/0 | - | temporal | 47: cmd/reliant analytics auth bgprocess certs cgroupmem | 47 importers; slog setup → observe in P7; exclude |
| mcp | 8 | 4204 | M | 85/43/4 | - | exec,http | 4: grpc llm rctx toolexec | 8 pkgs; Runtime/Client ifaces (stdio/http transports) |
| mcpconfig | 1 | 62 | S | 1/0/0 | - | sql | 1: grpc | sql read of project MCP cfg |
| mcpserver | 1 | 2153 | S | 25/22/10 | - | http | 1: grpc | 10 exported ifaces; Deps.Limiter concrete (lint ✗ server.go:128) |
| modelendpoints | 1 | 862 | S | 13/7/3 | - | http | 1: grpc | Store iface manager.go:33 + http probing |
| modelprefs | 1 | 158 | P | 8/2/1 | - | – | 3: grpc llm workflow | prefs resolution |
| models | 1 | 1389 | P | 38/17/2 | - | – | 5: llm threads toolexec workflow tools | subpkgs only (message …); domain vocab |
| natsutil | 1 | 32 | C | 1/0/0 | - | nats | 4: serverapi servergateway serverworker streaming | 32 LOC NATS connect helper |
| netguard | 1 | 313 | P | 9/2/0 | - | http | 6: grpc integrations llm mcp modelendpoints workflow | SSRF guard (dialer policy); http import is types only — verify |
| netports | 1 | 284 | W | 5/1/0 | - | – | 2: cmd/reliant toolexec | port allocator loop |
| nomachine | 1 | 53 | P | 3/0/0 | - | – | 4: grpc llm toolexec workflow | sentinel errors |
| observability | 1 | 903 | C | 21/2/0 | - | http,nats,temporal | 11: db debugserver grpc llm serverapi servergateway | otel/sentry setup → P7 observe |
| ospath | 1 | 251 | P | 4/0/0 | - | – | 3: filepreview grpc llm |  |
| osutil | 1 | 730 | P | 14/1/0 | - | exec | 6: daemon gitutil llm pkgmgr terminal toolexec | exec of local tools |
| pdfutil | 1 | 57 | P | 2/0/0 | - | – | 3: daemon llm toolexec |  |
| permission | 1 | 28 | P | 0/1/0 | - | – | 0:  | 0 importers → delete? |
| pid1 | 1 | 221 | C | 2/0/0 | - | – | 1: cmd | init reaper for cmd |
| pkgmgr | 1 | 1462 | S | 43/14/1 | - | exec | 3: bgprocess grpc toolexec | package manager detection; exec + db (2 Repo methods) |
| preset | 1 | 790 | P | 17/5/0 | - | – | 4: cmd/reliant grpc launch workflow | preset resolution |
| provetest | 1 | 1691 | S | 1/10/0 | - | exec | 1: llm | prove_test tool engine; exec |
| ptr | 1 | 48 | P | 6/0/0 | - | – | 3: grpc threads workflow | generic ptr helpers |
| queueddelivery | 1 | 225 | A | 5/5/4 | - | nats | 1: serverapi | NATS + Store iface queueddelivery.go:45 |
| rctx | 1 | 190 | P | 14/3/0 | - | – | 3: llm toolexec workflow | request ctx accessors |
| repo | 1 | 473 | S | 4/3/1 | - | exec | 5: grpc llm toolexec workflow workspacecreate | repo detection; exec + 4 Repo methods |
| runenv | 1 | 77 | P | 4/1/0 | - | – | 5: analytics features llm logging telemetry |  |
| runs | 1 | 811 | S | 11/10/4 | Y | – | 2: grpc serverworker | contract.go exists (Repository/TemporalClient/… contract.go:22-60) |
| serverapi | 1 | 941 | C | 7/1/0 | - | http | 1: cmd/reliant | role entrypoint → forge Serve (P3c) |
| servergateway | 1 | 1291 | C | 5/4/0 | - | http,nats,sql | 1: cmd/reliant | role entrypoint (P3c) |
| serverworker | 1 | 665 | C | 1/1/0 | - | http,temporal | 1: cmd/reliant | role entrypoint (P3c) |
| skills | 4 | 1748 | P | 23/20/0 | - | – | 4: cmd/reliant llm toolexec workflow | 4 pkgs; catalog parsing (embed) |
| streaming | 1 | 1006 | M | 22/15/4 | - | nats | 6: grpc serverapi servergateway serverworker workersetup workflow | StreamingHub (NATS vs in-proc) |
| telemetry | 1 | 1302 | M | 31/9/4 | - | – | 7: auth grpc llm logging serverapi serverworker | ErrorReporter etc. (sentry vs noop) |
| temporal | 3 | 1072 | A | 31/12/2 | - | grpc,sql,temporal | 4: grpc serverapi serverworker workflow | 3 pkgs: client wiring A; claimcheck = S (Store iface codec.go:81); temporaltest = T |
| terminal | 1 | 667 | S | 15/5/0 | - | exec | 2: grpc toolexec | pty sessions; exec |
| threads | 1 | 3341 | S | 33/20/3 | - | sql | 6: grpc launch llm serverworker workersetup workflow | already declares Repository (service.go:18) — best first candidate |
| tokenauthority | 1 | 708 | S | 23/5/1 | Y | sql | 6: grpc llm serverapi servergateway serverworker workflow | contract.go exists |
| tokens | 1 | 256 | P | 16/2/0 | - | – | 1: llm | token counting |
| toolbindings | 1 | 347 | P | 10/3/1 | - | – | 1: workflow | binding resolution |
| toolexec | 5 | 24254 | M/S | 139/57/14 | - | connect,exec,http,nats | 8: cmd/reliant grpc queueddelivery serverapi servergateway serverworker | 5 pkgs; 14 exported ifaces (DaemonRouter…); daemonruntime 16.8k LOC registry |
| triggers | 4 | 6100 | S/W | 137/73/19 | - | http,sql,temporal | 6: grpc integrations serverapi serverworker workersetup workflow | 4 pkgs; Poller loops = W; triggerspec = P; 12 ifaces |
| triggertools | 1 | 223 | S | 3/2/1 | - | connect | 1: serverworker | Connect client to own API |
| validation | 1 | 574 | P | 49/7/1 | - | – | 3: config grpc preset | 49 funcs; pure validators |
| vault | 1 | 489 | S | 19/6/2 | - | sql | 6: connections db grpc integrations serverapi serverworker | sealing over sql |
| version | 1 | 167 | P | 4/1/0 | - | – | 8: cmd/reliant analytics auth grpc llm observability |  |
| videojobs | 1 | 128 | S | 6/4/1 | - | sql | 3: llm serverapi serverworker | sql job store |
| workersetup | 1 | 430 | C | 5/2/0 | - | temporal | 2: serverapi serverworker | temporal worker registration (P3c) |
| workflow | 27 | 77788 | S/M/P | 820/321/13 | - | grpc,http,sql,temporal | 18: cmd/reliant config db grpc launch llm | 27 pkgs; runtime/activities/handlers = Temporal activities (W-ish); yaml,validation,model,cel,analysis = P |
| workspacecreate | 1 | 558 | S | 5/6/2 | - | – | 3: grpc llm workflow | 5 Repo methods |
| worktreepath | 1 | 51 | P | 2/0/0 | - | – | 2: grpc workspacecreate |  |
| worktreereclaim | 1 | 2701 | S | 25/10/0 | - | exec | 2: toolexec worktreesweep | 2.7k LOC; exec git |
| worktreesweep | 1 | 1063 | W | 12/10/4 | - | – | 1: grpc | sweep loop; Store iface sweep.go:69 |

Tally (top-level units): S≈33, A≈12, M≈7, P≈38, W≈7, C≈10, H 1, D 1. The 5 big multi-package units (llm 31, workflow 27, integrations 12, mcp 8, toolexec 5) need a per-subpackage pass in their wave. Subpackage breakdown, from `find … -printf %h`:
- **llm**: `drivers/<provider>` ×14 = A (one adapter per vendor: anthropic, openai, gemini, vertexai, openrouter, codex, copilot, antigravity, claude, imagegen, videogen, local, reliant, replay); `drivers/*wire` ×4 + `drivers/drivererrors`, `accumulator`, `cache`, `models` (3.6k), `tools/names`, `tools/toolcatalog` = P; `drivers/registry` = strategy registry (`Register…`) → exclude with reason; `drivers/mock_static` = T; `tools` (95 files, 28.6k LOC, 18 exported ifaces, and the `Tool` iface implemented by ~90 tools) = M; `tools/shell` = S; `tools/cmd/toolconfiggen` = generator (exclude).
- **workflow**: `runtime` (25.5k) = S, which needs pre-splitting; `runtime/activities/handlers` (17.6k, 58 files) = Temporal activity set (W-shaped; the contract is the activity struct's Deps); `runtime/activities`, `runtime/schema` and `validation` (10.3k) are registries (`Register…`) → exclude with reason; `yaml`, `model`, `analysis`, `cel`, `core`, `reference`, `workflowref`, `messageconv`, `stopreason`, `machinewait`, `builtin`, `v2/schema`, `runtime/activities/types` = P; `reconciliation` (3.7k) = S; `scenario`, `scenario/runner` = S/T; `lifecycle`, `threadcancel`, `threadwake`, `workflowsource` = S (small seams); `yaml/cmd/yamlbindingsgen` = generator.
- **integrations**: `ghaccess`, `ghusers`, `webhook` have contracts; `gmail`, `httpaction` (3 ifaces), `connauth`, `ghdelegated`, `webhook/github` = A; `catalog`, `manifest`, `tmpl` = P; `catalogindex` = S (`ConnectionLister` service.go:22).
- **toolexec**: root (6.2k, 14 exported ifaces such as `DaemonRouter`) = S whose ifaces are really consumer seams; `daemonruntime` (16.8k, `Register…`) = registry; `daemonstate` = W; `bootstrap`, `transport` = C/A.
- **db**: `db`, `db/core` (21 store ifaces), `db/postgres` (hand stores), `db/postgres/generated` (sqlc), `db/migrationcheck` (P/T). All of it is P5b's.
- **daemon**: root = M (`Client`, `Executor`, `FileSystem`, `CommandSender`, `OOMChecker`, `NoSpaceExplainer`); `configloader` = P; `rootwatch` = W.
- **mcp**: root = M (`Runtime`, `Client`: stdio/http transports); `serverclient` = A; `service`, `runtime` = S; `catalog`, `compat`, `protocol`, `telemetry` = P.
- **temporal**: root = A (client dial); `claimcheck` = S (`Store` codec.go:81); `temporaltest` = T.
- **auth**: root = S; `oauthcallback`, `oauthhelper` = A.
- **triggers**: root = S (12 ifaces: `Repo`, `EventRepo`, `Launcher`, `ScheduleClient`, `Poller`…) with Poller loops = W; `triggerspec` = P; `workflowevent` = S (6 ifaces); `runevents` = S.

**Lint-verified today** (`reliant forge lint --conventions --scope internal/accesstokenclient --scope internal/mcpserver --scope internal/cliauth`):
```
✗ internal/cliauth/contract.go:65 [forgeconv-deps-are-interfaces] Deps field "Dirs" has concrete type credentials.Dirs
✗ internal/mcpserver/server.go:128 [forgeconv-deps-are-interfaces] Deps field "Limiter" has concrete type *Limiter
⚠ internal/accesstokenclient/client.go:43 [forgeconv-internal-package-contract-names] … needs a constructor with the signature `func(Deps) Service` …
```

## 2. `internal/db` Repository

> **Outcome (P6, superseded by `10-repo-split-plan.md`).** The split shipped as
> that plan's option A, not as step 3 below: `*db.Repo` stayed the one concrete
> persistence type, every consumer declares the role it calls in its own
> package, and `db.Repository` (463 methods by then), its `var _` assertion and
> the remaining implementation-side `core.*Store` interfaces are deleted (lane
> Y). The accessors return concrete stores (`db.ConnectionStore`,
> `db.IntegrationWriteLedger`). The census below is the 2026-10 input, kept
> for the record.

**Shape.** One interface `Repository` (`internal/db/repository.go:13`) with **442 methods**, no embeds. The only implementation is `*db.Repo` (`internal/db/repo.go:116`, plus `repository_impl.go`, which is over 5.1k lines). Beneath it sit 21 `core.*Store` interfaces in `internal/db/core/` (313 methods; overlap by name) and the sqlc `Querier` (350).

core store sizes: WorkflowCatalogStore 39 · WorkflowStore 36 · TriggerStore 34 · SettingStore 32 · MessageStore 27 · PlanTaskStore 22 · ProjectStore 18 · ConnectionStore 18 · ThreadStore 17 · WorktreeStore 16 · ChatStore 15 · AgentMessageStore 15 · ToolCallStore 13 · ContextWindowStore 11 · RunEventStore 9 · ApprovalStore 6 · RepoStore 6 · SecretsTx 5 · InboxStore 4 · AttachmentStore 4 · RunStore 2.

**Consumers.** Non-test files naming `db.Repository`: grpc/services 77, llm/tools 89, workflow/runtime/activities/handlers 68, llm/drivers 12, then 27 more packages with 1–6 files each (31 packages in total). Distinct Repository methods each consumer actually calls (scan of `.Method(` names), with the dominant core store:

| consumer | methods | dominant aggregates |
|---|---|---|
| grpc/services | 179 | (repo-only) 44, SettingStore 24, WorkflowCatalogStore 22, ProjectStore 16, TriggerStore 11, WorktreeStore 10 |
| workflow/runtime/activities/handlers | 74 | repo-only 23, WorkflowStore 9, AgentMessage 6, Message 6, Chat 5, Thread 5 |
| llm/tools | 52 | WorkflowCatalog 13, PlanTask 10, repo-only 11, Trigger 4 |
| workflow/reconciliation | 23 | WorkflowStore 8, AgentMessage/Thread/ToolCall 3 each |
| threads | 13 | Message 7 (already narrowed: `threads.Repository` service.go:18) |
| runs | 13 | WorkflowStore 5 (already `runs.Repository` contract.go:22) |
| llm/drivers | 9 | SettingStore 9 |
| workflow | 9 | WorkflowStore 6 |
| bgprocess 8 · modelendpoints 7 · llm/drivers/local 6 · daemonstate 5 · workspacecreate 5 | ≤8 | small |
| messageconv 4 · runtime 4 · toolexec 4 · repo 4 · filepreview/mcpconfig/connections/workflowevent 3 · shell/pkgmgr 2 · configadapter/serverapi/temporal/toolbindings/workersetup 1 | ≤4 | trivially narrowable now |

Packages already holding consumer-declared narrow interfaces, which is the target pattern: `threads.Repository` (service.go:18), `runs.Repository`, `launch` store.go:17–85 (8 role ifaces: `ChatWriter`, `TriggerEventStore`, `WorkflowStore`, `ProjectLookup`, `Transactor`…), `daemonliveness.Repository` (reachable.go:61), `worktreesweep.Store` (sweep.go:69), `modelendpoints.Store` (manager.go:33), `queueddelivery.Store` (:45), `webhook.{HooksStore,EventsStore,Store}`, `ghaccess.Store`, `connectorgrant.Store`, `catalogindex.ConnectionLister`, and in grpc/services `forgeNameReader/Writer`, `chatWorktreeReader`, `projectScenariosReader`, `projectConfigPushedAtReader`; plus `llm/drivers.antigravityTokenStore`. `internal/db/chat_update_emitters.go:2-5` already documents the "consumer declares" intent.

**Proposed split (no big-bang, sized against P5b):**
1. **Now (P6 wave 1–3, before P5b):** every consumer with ≤13 methods swaps its field type `db.Repository` → a local unexported interface listing only what it calls (as `threads`/`launch` already do). This is pure type narrowing: `*db.Repo` still satisfies it and nothing in `internal/db` changes, so it doesn't collide with P5b lanes. That's ~24 packages, each S. The method *signatures* still name `db.*`/`core.*` types, and P5b's entity collapse may re-type them, which is cheap at ≤13 lines per package.
2. **Not now:** grpc/services (179), activities/handlers (74), llm/tools (52), reconciliation (23). These get narrowed **per P3b service lane** (grpc) and **per P5b aggregate lane** (others), against the generated stores. Doing it earlier writes the interfaces twice (PLAN §4 P5b last bullet).
3. **Post-P5b target shape:** each P5b aggregate lane (chat/message/thread, workflow/run, project/worktree/repo, trigger, connection, tokens/auth, settings/presets/model-endpoints) exposes its generated `<Entity>Store` plus `_repo_ext.go` methods. Consumers declare role interfaces over *those* (`api/role-interface`). A wide consumer (handlers, tools) takes several 2–10-method role fields in Deps, one per aggregate, rather than one Repository. `core.*Store` interfaces are then deleted, since they're implementation-side declarations, which the rule forbids.
4. **Delete `db.Repository` last**, once its consumer count reaches 0 (track with `rg -c 'db\.Repository\b' internal`). Its 156 methods with no external caller are deleted or folded into `_repo_ext.go` during P5b.
5. **Test fallout:** the 36 `struct{ db.Repository; … }` partial fakes shrink automatically. Once the field is a narrow interface, the embed goes and the fake becomes a generated `Mock<Role>` or a 2-method stub.

Do **not** generate a mock for `db.Repository`. A 442-method `MockRepository` is the "mega-mock" PLAN P6 rules out, and it would re-couple every test to the full surface.

## 3. Mock opportunities (12 sampled test files)

| file | stubbing today | verdict |
|---|---|---|
| grpc/services/tools_daemon_lease_test.go:131,200,416 | 3 structs embed `db.Repository`, override 1–2 methods, record calls | **Recorder mock** once the handler takes a narrow role iface (P3b) |
| workflow/runtime/activities/handlers/preflight_wait_test.go:23,53,74 | `waitFakeRouter`, `markerRepo{db.Repository}`, `machineStateRouter` | router → generated `toolexec` `MockDaemonRouter`; markerRepo → role mock |
| llm/drivers/model_substitution_test.go:158 | `providerKeysRepo{db.Repository}` | recorder mock of a SettingStore role (9 methods) |
| launch/launcher_restart_test.go:167 | `failingLookups{db.Repository}` | launch already has role ifaces → `launch/mock_gen.go` (exists after generate) |
| threads/interrupt_test.go:30-66 | hand `recordingThreadInterruptSignaler`/`Canceler` (captures args + `failErr`) | **exactly** `contractkit.Recorder` + `XxxFunc` → replace |
| daemonquery/responder_test.go:18 | `fakeSource{calls []string}` | recorder mock |
| mcpserver/audit_lifecycle_test.go:20 | `stubResolver` | recorder mock (after `Limiter` becomes an iface) |
| workflow/reconciliation/background_processes_test.go:23 | `fakeProcessDaemons` | recorder mock |
| toolexec/daemon_lifecycle_publisher_test.go:11 | `recordingLifecycleNATSConn` | recorder mock of a 1-method publisher iface |
| triggers/fake_test.go:20 (261 lines) | `fakeRepo` with `sync.Mutex` + maps, dedupe semantics (`CreateTriggerEvent` → inserted bool) | **keep stateful fake**: it models the DB uniqueness invariant the tests rely on |
| integrations/webhook/fake_test.go:15 | `fakeStore`/`fakeIntake` with maps + `seen` dedupe | **keep** (stateful) |
| integrations/gmail/fake_test.go:35 (373 lines) | httptest fake of the Gmail API | **keep**: this is the adapter-test pattern the `adapter` skill prescribes (stub server, not mock) |
| llm/drivers/local/inventory_fake_test.go:11 | value-type fake returning canned maps | recorder mock (stateless) |
| temporal/sdk_logger_test.go:21 | `recordingHandler` slog handler | keep: it's a stdlib-interface sink, not a contract |

Rule for the rollout: **stateless canned-return or call-capturing stub → generated mock; anything with maps + mutex that models invariants (uniqueness, ordering, dedupe) → keep, and assert it satisfies the contract with `var _ X = (*fake)(nil)`**. By the type-name scan, about ¾ of the 224 stub types are the stateless kind. The 36 `db.Repository`-embedding fakes are the biggest win, because embedding means an un-overridden call panics on a nil iface. The generated mock instead returns a zero value or records the call, so these tests become less brittle.

## 4. Waves (disjoint package sets; P6 rules: one PR per package, warn mode)

Per-package proof (no forge.yaml needed; runs in about 10–20s):
```
reliant forge lint --conventions --contract --scope internal/<pkg>   # last line must be ✅
go build ./internal/<pkg>/... && go vet ./internal/<pkg>/...           # scoped; machine is shared
make test-short PKGS=./internal/<pkg>/...
```
The build stays green because each PR adds `contract.go` (`Service`, `Deps`, `New(Deps) Service`) and keeps the existing concrete constructor as a thin wrapper (or retypes the callers in the same PR when they're ≤3 files). The consumer-side narrowing touches only the consumer's own package. Until forge.yaml lands (P1 1g) `mock_gen.go` can't be generated, so waves 1+ need P1 done. Wave 0 doesn't.

| wave | packages (disjoint) | size | gate / notes |
|---|---|---|---|
| **0 (now, no forge.yaml)** | PR #740 trio; `cliauth` Deps.Dirs → iface; `mcpserver` Deps.Limiter → iface; delete `permission`, `envutil` (0 importers); add `//forge:exclude-contract: <why>` to ~38 **P** packages (one PR, header-only) | S | conventions lint clean on these scopes |
| **1 seams with existing narrow ifaces** | `threads`, `runs`, `launch`, `connections`, `connectorgrant`, `tokenauthority`, `queueddelivery`, `worktreesweep`, `modelendpoints`, `daemonliveness` | S each | mostly rename to `Service`/`Deps`; first real `mock_gen.go`; replace `threads/interrupt_test.go` recorders |
| **2 llm** | `llm/drivers/<provider>` ×14 (one lane each, A + `forge:outbound-io`), `llm/drivers` root (M: ImageGenerator/VideoGenerator), `llm/drivers/registry` (exclude: registry) | M | lanes never touch `llm/tools` |
| **3 daemon + mcp + streaming/telemetry** | `daemon`, `daemon/rootwatch`, `mcp` (+subs), `streaming`, `telemetry`, `controlplane`, `daemonquery`, `daemonactivity`, `daemonevents`, `daemonstate` | M | M-category: move existing ifaces into contract.go, mark `//forge:contract` where not named `Service` |
| **4 integrations + temporal + auth** | `integrations/{gmail,httpaction,connauth,ghdelegated,webhook/github,catalogindex}`, `temporal`, `temporal/claimcheck`, `auth`, `auth/oauthcallback`, `auth/oauthhelper`, `analytics`, `localprobe`, `gitcredentialclient`(post-#740) | M | adapters: httptest tests already exist (gmail) |
| **5 toolexec + triggers** | `toolexec` root, `toolexec/daemonstate`, `triggers`, `triggers/workflowevent`, `triggers/runevents`, `triggertools`, `agentruns` | L | toolexec's 14 ifaces are consumed by grpc/serverapi: split into consumer-side declarations, so coordinate with P3a |
| **6 workflow (non-handler)** | `workflow/reconciliation`, `workflow/lifecycle`, `workflowsource`, `threadcancel`, `threadwake`, `scenario`, `workflow` root | L | registries (`runtime/activities`, `runtime/schema`, `validation`) get exclude-with-reason |
| **7 small S leftovers** | `accountpurge`, `automationcred`, `config`, `execfollow`, `mcpconfig`, `pkgmgr`, `provetest`, `repo`, `terminal`, `vault`, `videojobs`, `workspacecreate`, `worktreereclaim`, `forgecred` | S each | fully parallel |
| **8 after P3b/P5b** | `workflow/runtime`, `runtime/activities/handlers`, `llm/tools`, `grpc/services` → handlers; Repository role split (§2 step 2–4) | L | per-service / per-aggregate lanes |
| **excluded / structural** | **W**: bgprocess, cgroupmem, diskwatch, netports, daemonstate loop → `internal/workers/` shape in P3c. **C**: serverapi/gateway/worker, workersetup, observability, debugserver, drain, natsutil, pid1, configadapter → `internal/app` in P3c/P7. **D**: db → P5b | — | not contract.go |

Highest seam value first: wave 1 is cheapest and proves the toolchain. Waves 2–5 cover llm, daemon, mcp, integrations, temporal and toolexec, the order PLAN P6 names. workflow and threads come in waves 6 and 1 respectively; the rest of workflow waits for its runtime split.

## 5. Forge gaps and open questions

**Gaps (exact commands):**
- **G1 — `--contract` lane is silent without forge.yaml.** `reliant forge lint --contract --scope internal/accesstokenclient` → `✅ 1 gating linter(s) passed`, although that package has a known non-canonical constructor that `--conventions` flags (⚠ client.go:43). Likewise `--contract --conventions --strict --scope internal/threads` → ✅, even though `threads` has exported methods and no contract.go. The contract-gap analyzer evidently doesn't run, or reports nothing, without `contracts:` config, so the per-package gate is useless until P1 1g. *Fix:* run the contract lane with defaults when there's no forge.yaml (as `--conventions` already does in warn mode), or print "skipped: no forge.yaml".
- **G2 — `--strict` doesn't escalate `internal-package-contract-names` warnings without forge.yaml.** The message itself says "`forge lint --strict` gates it", but `--strict` only promotes the auth-posture and could-not-run lanes per `--help`. Needs confirming once forge.yaml exists.
- **G3 — no tool lists contract candidates.** Classification required a hand scan. A `reliant forge project audit --contracts` (per package: exported surface, outbound-IO calls, multi-impl ifaces, suggested category) would make P6 mechanical; the analyzers for `forge-exclude-contract-outbound-io`/`-multi-impl` already compute it.
- **G4 — no "narrow a wide interface" helper.** The Repository split needs "which of iface X's methods does package P call". This is a gopls query forge could expose (e.g. `reliant forge project shapes --uses db.Repository --in internal/threads`).
- **G5 — mock of a 442-method interface.** If `db` ever gets a contract.go, `mock_gen.go` would emit ~442 `XxxFunc` fields. A size cap/warning ("interface >N methods: declare role interfaces at consumers") would point people at `api/role-interface`.
- F2 (CLI-only contract packages) is already tracked. It applies to `cliauth`, `forgecred`, `execfollow` and `askuser`.

**Open questions:**
1. Activity sets (`workflow/runtime/activities/handlers`, 58 files): should forge treat a Temporal activity struct like a worker (exempt), or as an S with `Deps`? This decides wave 8's shape.
2. Registries (`llm/drivers/registry`, `toolexec/daemonruntime`, `workflow/validation`, `runtime/schema`): use exclude-with-reason, or does `llm/tools.Tool` (~90 impls) count as `-multi-impl` and get refused as an exclude? The latter would force a contract.go whose "Service" is the `Tool` interface.
3. `os/exec` of local git/pkg managers (gitutil, osutil, worktreereclaim, terminal) isn't outbound-IO per the skill. Do we still want a seam (an exec runner iface) for testability? I recommend yes for worktreereclaim and terminal only.
4. Daemon-side vs server-side components (G/F2): do daemon-only packages (daemon, toolexec/daemonruntime, diskwatch) get their own composition root, or `//forge:external-component`?
5. Should the 156 Repository methods with no external caller be deleted in a P5a/P6 pre-pass, which would cut P5b's port surface by about 35%?

## 6. Reproduce
- Package table: the python scan in this research session walks `internal/*/`, counting non-test LOC, regex `^func (\(..\) )?[A-Z]`, `^type [A-Z]`, `^type [A-Z]\w* interface`, outbound import markers, and importer units via `"github.com/reliant-labs/reliant/internal/<x>` in non-test files.
- Repository method/consumer map: regex `^\t([A-Z]\w*)\(` inside `type Repository interface` × `.Name(` calls per package, outside `internal/db`.
- Lint: `reliant forge lint --conventions [--contract] --scope internal/<pkg>` (outputs quoted above).
