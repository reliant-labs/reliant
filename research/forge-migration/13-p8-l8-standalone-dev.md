# P8 L8 — reliant's standalone `forge env up dev`: spike + result

Status: **built and proven** on branch `fm-w26-p8-l8-standalone-dev` (decision
D-P8-1 (b), `12-p8-devtooling-plan.md` §0 finding 1, §8 L8, §9). Measured in
the kata sandbox (no k3d, no nested k8s), with no control-plane process
running for this stack.

## 1. Spike: can the three roles run with no control plane?

Run first by hand against loopback infra: forge's cached embedded postgres,
nats-server built from go.mod's `nats-server/v2`, and the Temporal CLI dev
server the Go SDK caches. Then the same way under `forge env up dev`.

| Concern | Result | Kind of break |
|---|---|---|
| `server api` with `AUTH_MODE=apikey` | boots, `/health` reports `auth_mode: apikey`. A bearer equal to `AUTH_API_KEY` maps to one deterministic user id | none |
| `server worker` | boots, registers on `reliant-workflows` | none |
| `server gateway` | boots. Logs one INFO line, "DAEMON_STATE stream not found yet — waiting for control plane", and keeps retrying with backoff. That stream only carries **managed** (control-plane workspace) daemons, so a self-hosted stack never needs it | none (log noise only) |
| Daemon token validation | no `RELIANT_CONTROL_PLANE_URL` ⇒ self-hosted token authority. `TokenService/CreateToken {kind: DAEMON}` under the api key mints an `rlat_` token; `reliant daemon start --token --grpc-url <gateway>` connects and registers | none |
| LLM keys | no provider is configured by default. A **model endpoint** (`ModelEndpointService/CreateModelEndpoint`, any OpenAI-compatible URL, `DIRECT`) serves a chat with no API key… | |
| …but `StartChat` refused it | `invalid_argument: no API keys configured` | **reliant bug**, fixed (`internal/llm/drivers/model_selection.go`): `ValidateModelSelector` resolved every selector against the API-key registry, which knows nothing of local/endpoint models. A `local.IsSelector` selector is now left to the run, which resolves it and fails with the explained `local.UnavailableError` |
| Temporal | dev server, namespace `reliant` | **forge gap**, fixed in forge#1199: `forge.HostInfra` had only postgres + zitadel. New `engine = "temporal"` |
| NATS | JetStream needed (`DAEMON_COMMANDS`, streaming replay) | **forge gap**, fixed in forge#1199: new `engine = "nats"` |
| Postgres 16 | `HostInfra.version = "16"` 404'd (embedded-postgres fetches by full release) | **forge bug**, fixed in forge#1199: a major resolves to its pinned release (16 → 16.9.0) |
| `AUTH_API_KEY` in `FileSecrets.generate` | never generated, so api refused to start ("AUTH_MODE=apikey requires AUTH_API_KEY") | **forge bug**, fixed in forge#1199: `generate` skipped keys whose only ref is optional |
| Web app against an apikey server | showed the Supabase sign-in | **reliant bug**, fixed (`web/vite.config.ts`): `useAuthMode` fetches `${origin}/health`, and under the Vite same-origin transport `/health` was not proxied, so it got index.html and assumed supabase |
| Activity heartbeats | a `CallLLM` heartbeat RPC can time out under heavy machine load (the activity retries and the turn completes) | inherent to a loaded dev box, mitigated: the Temporal engine runs SQLite in WAL / `synchronous=normal` |

Nothing inherent blocks it. The two forge gaps and two forge bugs are fixed
upstream; the two reliant bugs are fixed in this branch.

## 2. What was built

- `deploy/kcl/dev/main.k`: host-only. No `deploy/k3d.yaml`, no cluster
  target. `forge.HostInfra` postgres 16 + nats + temporal;
  `reliant-migrate` (job, gates everything); `reliant-api-server`,
  `reliant-temporal-worker` and `daemon-gateway` under air
  (`.air.api.toml`, `.air.worker.toml`, new `.air.gateway.toml`);
  `reliant-web-deps` (job) then `reliant-web` (Vite).
- `deploy/kcl/dev/config.k`: the typed config values (ports, `auth_mode =
  apikey`, temporal, TLS off), projected per root through
  `config_gen.workloadEnvMap`, so a role is handed only the config its root
  reaches. The DSNs (`DATABASE_URL`, `NATS_URL`) are credentials in the schema
  and are overridden in main.k with loopback values.
- `deploy/kcl/dev/ports.k`: every port, `allocate_port(base, worktree)`.
- `deploy/kcl/workloads.k`: the processes that exist (the three
  `//forge:root`s via `config_gen.root_of`, plus the migration), under
  control-plane's workload names. forge would otherwise scaffold one workload
  per proto service (36 processes that do not exist).
- `secrets/dev.yaml` is **not committed** (gitignored). `forge env up dev`
  generates `AUTH_API_KEY` and `RELIANT_VAULT_KEY` into it on first run.

### Why web/ is a host process, not a `forge.Frontend`

A `forge.Frontend` at an in-tree path is also a forge **codegen target**. A
`forge generate` with one declared wrote forge's hooks, UI kit and
`web/buf.gen.yaml` into `web/` (178 files, measured). That is P9's adoption
(`git mv web frontends/web`, R0 pending forge fixes), not this lane's. So
`reliant-web` is an `OnHost` workload running `npm run dev` in `web/`. The
path is stated once (`_web_path` in main.k), so P9's move changes one line and
can then turn it into a `forge.Frontend`. The cost until then: forge's
`[browser:*]` console mirror (the app's own telemetry still writes it to
`reliant-web.log`) and forge's npm freshness check, which the
`reliant-web-deps` job stands in for.

## 3. Parallel stacks (§9)

Same primitive and cap as control-plane: `allocate_port(base, worktree)`, a
100-port block per worktree, `dev_stack.max_stacks` 8 (forge's default). All
bases sit in 21000-21099, so stack N owns 21N00-21N99 and the whole range is
21000-21799. That is clear of every control-plane dev base (3000-3799,
6060-6961, 8081-8898, 19000-19794, 30880-31587, 38080+, 39190+), of
5433/5434, and of the kernel's ephemeral range (32768+). Each stack's
postgres/NATS/Temporal data lives under its own checkout's
`.forge/hostinfra/`, so two worktrees never share a database. Host-only means
no k3d pre-mapping, so the 8-stack cap is forge's default rather than a hard
limit; raising it is one `dev_stack.max_stacks` line.

## 4. Proof (2026-10-22)

- `forge env up dev` (forge#1199 build): postgres 16.9 :21140, nats :21150,
  temporal :21160; migrate `none -> 20261022144035`; api :21110, worker :21120,
  gateway :21130, web :21100 all `up`.
- Chat round-trip (stack 1): `CreateToken` → `reliant daemon start --token` →
  model endpoint at a scripted OpenAI-compatible server → `StartChat`.
  Messages: `USER "run the proof"` → `ASSISTANT TOOL_CALL shell {"command":
  "echo l8-roundtrip-$(hostname) | tee l8-proof.txt"}` → `TOOL TOOL_RESULT
  {"stdout":"l8-roundtrip-ws-ws-2aab1465\n","exit_code":0}` → `ASSISTANT "The
  daemon ran it…"`. The file exists in the daemon's workspace. Worker log
  `[CallLLM] Usage … provider=local model=l8-fake success=true`; gateway log
  `[🔧 ToolsDaemon] Daemon registered`.
- Two worktrees at once: a second worktree came up on block 2 (212xx), with
  its own postgres data dir, a different `AUTH_API_KEY`, and 0 projects
  against stack 1's 2. In a browser, its web app shows the API-key sign-in;
  after sign-in the onboarding printed `RELIANT_SERVER_URL=http://localhost:21210
  RELIANT_GATEWAY_URL=http://localhost:21230 reliant daemon start --token`.
  Running exactly that with a UI-generated token connected, and the
  onboarding advanced to the model step.
- `forge env down dev` in worktree 2 closed all seven of its ports. Stack 1's
  seven stayed up, and `forge env ps` lists only stack 1.

## 5. Open items

- **Pin**: the env needs a reliant forge pin that contains forge#1199.
  Until then the pinned forge cannot compile `engine = "nats"`. L1 is
  re-pinning (≥254e5e80); that pin must be ≥ forge#1199's merge.
- **forge friction, not fixed here**: `forge env devstack prune` takes about
  10 minutes on a machine with ~227 worktrees of the repo. It spawns git per
  worktree, roughly O(n²) through the worktree-key disambiguation. Its output
  is correct.
- control-plane's dev stack keeps wiring reliant's env by hand (P8b/F6). The
  workload names here match its `wl.reliant_api_server`,
  `wl.reliant_temporal_worker` and `wl.daemon_gateway`.
- L4 (docs) should point `reliant.md` / the `dev-logs` skill at this stack's
  log names (`.forge/logs/dev/reliant-*.log` inside reliant), and at ports.k
  for its ports.
