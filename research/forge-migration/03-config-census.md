# 03 — Config census + typed-config/secrets plan (feeds P4)

Read-only research. Sources: reliant/control-plane/forge checkouts under
`fm-plan-ab0d8ac0/` at current main. The forge binary was
`v0.1.45-0.20261011192000-6c0c3edb1322`. Unless noted, every `file:line` is
relative to `reliant/`. The census came from the commands in §0, not from
reading files by hand.

## TL;DR

- **147 non-test `os.Getenv/LookupEnv` call sites**, up from the 138 in the brief. That's because main has grown since: `internal/serverworker/connections.go`, `internal/serverapi/github_access.go` and `internal/integrations/webhook/wiring.go` now take an injected `getenv`. The census below has **~100 distinct names**. There are ~85 real config knobs, ~10 ambient OS variables (HOME, SHELL, …) that are *not* config, and 3 dynamic families: `RELIANT_FEATURE_*`, `RELIANT_OAUTH_<ID>_CLIENT_{ID,SECRET}`, and MCP `${VAR}` expansion.
- **There are four readers today, not one.** The same knob is read in up to three ways:
  - raw `os.Getenv`;
  - the flag helpers `serverEnvOrDefault*` (`cmd/reliant/commands/server.go:32-58`) and `envOrDefault*` (`cmd/reliant/commands/root.go:63-90`);
  - `builddefaults.Value` (env > ldflags > neutral; `internal/builddefaults/defaults.go:86`);
  - injected `getenv` funcs.

  For example, `DATABASE_URL` is a flag default (`server.go:96,144,187`) *and* a raw read in `internal/db/connect.go:111`. `INTERNAL_SERVICE_SECRET` has **6 readers** and `RELIANT_CONTROL_PLANE_URL`'s 3-name fallback chain is **copy-pasted 6×**.
- **`internal/envutil` is dead**: it has no non-test callers.
- **control-plane sets 41 distinct names on reliant processes across envs**, and every one of them must keep its exact spelling (rollout rule 3):
  - dev `_reliant_host_env` (`control-plane/deploy/kcl/dev/main.k:1326-1427`), the api extras (`:2754-2790`) and the gateway pod (`:2876-2935`);
  - `lib/env.k` `reliant_base_env/reliant_api_env/daemon_gateway_env` (`:227-395`);
  - prod extras (`prod/main.k:476-560`);
  - the workspace operator's daemon pod env (`control-plane/internal/operators/workspace/workspace_controller.go:2016-2318`). That last one is **Go, not KCL**, and is easy to miss.
- **One dev-set name is a silent no-op today.** control-plane sets `CORS_ORIGINS` on reliant (`dev/main.k:1397`), but reliant reads only `CORS_ALLOWED_ORIGINS` (`cmd/reliant/commands/server.go:111`), so dev runs with the default `*`.
- **Other set-but-unread names:** `AUTO_MIGRATE`, `APP_URL`, `SUPABASE_URL` (read only by analytics, so not the inbound auth it is commented as), `SERVICE_NAME`/`OTEL_SERVICE_NAME`, `GATEWAY_URL`, `RELIANT_PROXY_HOST`.
- **F3 is confirmed and worse than "missing".** `binary_config {binary: "x server api"}` and `{binary: "nonexistent"}` both generate silently. That contradicts forge's own contract (`forge/proto/forge/v1/forge.proto:396-397`: "a binary that has no cmd/<name> directory is a generate-time error"). Repro in §6.
- **Recommendation: keep one binary and add role-scoped config to forge (F3a/F3b/F3c in §2.3).** The daemon and CLI run on user machines and get `config.Load` with a file layer. The product user config (`config/reliant-schema.json`, `~/.reliant`) stays bespoke.

## 0. How the census was produced (re-runnable)

```bash
cd reliant
# literal-name reads
rg -n --type go -g '!*_test.go' -g '!gen/**' -o 'os\.(Getenv|LookupEnv)\("[A-Za-z0-9_]+"\)' .
# const-name reads (resolved by hand via `rg 'Const\s*='`)
rg -n --type go -g '!*_test.go' -g '!gen/**' -o 'os\.(Getenv|LookupEnv)\([A-Za-z_][A-Za-z0-9_.]*\)' .
# helper reads
rg -n --type go -g '!*_test.go' '(serverEnvOrDefault\w*|envOrDefault\w*|envInt|getEnv|getenv|builddefaults\.Value)\("[A-Z]' .
# total
rg -c --type go -g '!*_test.go' -g '!gen/**' 'os\.(Getenv|LookupEnv)\(' . | awk -F: '{s+=$2}END{print s}'   # → 147
# role attribution: which role entrypoint imports each reading package
for p in <pkg>; do rg -l "reliant/$p\"" internal cmd | xargs dirname | sort -u \
  | grep -E 'serverapi|serverworker|servergateway|cmd/reliant|daemonruntime|workersetup|activities'; done
# control-plane setters
grep -n 'forge.EnvVar' control-plane/deploy/kcl/{dev/main.k,lib/env.k,prod/main.k}
rg -n 'Name: "[A-Z_]+"' control-plane/internal/operators/workspace/workspace_controller.go
```

### Role key

Roles were attributed by which entrypoint package imports the reader:

| Role | Entrypoint packages | Command |
|---|---|---|
| **A** = api | `internal/serverapi`, `internal/grpc` | `reliant server api` |
| **W** = temporal worker | `internal/serverworker`, `internal/workersetup`, `internal/workflow/runtime/activities*` | `reliant server worker` |
| **G** = gateway | `internal/servergateway` | `reliant server gateway` |
| **D** = daemon | `internal/daemon`, `internal/toolexec/daemonruntime` | user machine or workspace pod |
| **C** = end-user CLI | `cmd/reliant/commands` | — |
| **T** | tests / dev tools only | — |

`internal/llm/tools` is imported by A, W and D; `internal/logging` by every role. Monolith mode (`reliant server`) = A+W+G in one process.

### Where control-plane sets each name

These are the codes the "CP sets" column uses:

| Code | Location |
|---|---|
| **dev-h** | `dev/main.k` `_reliant_host_env` (1326-1427), shared by host api + worker |
| **dev-a** | `dev/main.k` api-only extras (2754-2790) |
| **dev-wk** | `dev/main.k` worker overrides (1448-1450) |
| **dev-gw** | `dev/main.k` daemon-gateway pod (2876-2935) |
| **lib-b** | `lib/env.k` `reliant_base_env` (227-277): api + worker, all cloud envs |
| **lib-a** | `lib/env.k` `reliant_api_env` (316-342) |
| **lib-g** | `lib/env.k` `daemon_gateway_env` (366-395) |
| **lib-db** | `lib/env.k` DB/pgbouncer `client_env` (217-221): `DATABASE_URL`, `DATABASE_DIRECT_URL` |
| **prod** | `prod/main.k` extras (476-560) |
| **cfg** | control-plane's own `config_gen.k` `appConfigEnvMap`, which every dev host process gets via `_host_env` (`dev/main.k:925-927`); this is how `INTERNAL_SERVICE_SECRET` reaches the host reliant |
| **op** | workspace operator daemon pod env (`control-plane/internal/operators/workspace/workspace_controller.go`) |

`workloads.k:635-683` (`reliant_api_server`, `reliant_temporal_worker`, `daemon_gateway`) declares **no env**. It contributes only `command = ["reliant","server","api"|"worker"]` and ports 9090/9190/`RELIANT_HEALTH_PORT`. All env comes from the codes above.

## 1. Census

`S` = secret. "Def" = the default in code. A dash in "CP sets" means control-plane sets nothing.

### 1a. Server process (A/W/G)

| Env var | Read at | Roles | Def | Type | S | CP sets | Notes |
|---|---|---|---|---|---|---|---|
| GRPC_PORT | cmd/reliant/commands/server.go:89 (flag `--grpc-port`) | A | 9090 | int | | dev-a 2754 | |
| HEALTH_PORT | server.go:92 (A), :158 (W), :182 (G) | A W G | 8081 / 8081 / **8080** | int | | dev-a 2758, dev-wk 1449, dev-gw 2879, lib-g 395 | per-role default differs |
| PPROF_PORT | server.go:90, :159 | A W | 6060 | int | | dev-a 2762, dev-wk 1450 | |
| PPROF_ADDR | internal/servergateway/run.go:138 | G | unset | string | | dev-gw ~2905 | gateway spells pprof differently from A/W |
| BIND_ADDRESS | server.go:91, :183 | A G | 0.0.0.0 | string | | dev-h 1404, dev-gw | |
| TOOLS_DAEMON_PORT | server.go:181 (G `--daemon-port`); cmd/reliant/commands/daemon.go:935 (D `--port`, string!) | G D | 9190 | int (D: string) | | dev-gw | **same name, two meanings and types** |
| DATABASE_DRIVER | server.go:95, 143, 186; internal/db/connect.go:103; internal/db/repo.go:273 | A W G C | postgres | enum | | dev-h 1327, dev-gw | dead knob: only `postgres` is valid |
| DATABASE_URL | server.go:96, 144, 187; internal/db/connect.go:111; internal/db/testutil.go:223 (T); tools/reliant-dev/*.go (T) | A W G C | "" (required) | string | **S** | dev-h 1337, dev-gw, lib-db 217/221 (secret_ref) | |
| DATABASE_DIRECT_URL | server.go:97, 145, 188; connect.go:112 | A W G | "" | string | **S** | lib-db | |
| RELIANT_DB_MAX_OPEN_CONNS | internal/db/connect.go:167 (`envInt`) | A W G | 25 | int | | dev-h 1342 | |
| RELIANT_DB_MAX_IDLE_CONNS | connect.go:168 | A W G | 10 | int | | dev-h 1343 | |
| RELIANT_DB_SCHEMA_WAIT_SECONDS | connect.go:286 | A W G | 300 | int (seconds) | | dev-gw ~2935 | should be a Duration |
| DATA_DIR | server.go:98, 146, 189 | A W G | ./data | path | | lib-b 255, lib-g 373 | |
| TEMPORAL_HOST | server.go:101, 149 | A W | localhost | string | | dev-h 1350, lib-b 231 | |
| TEMPORAL_PORT | server.go:102, 150 | A W | 7233 | int | | dev-h 1351, lib-b 232 | |
| TEMPORAL_NAMESPACE | server.go:103, 151 | A W | reliant | string | | dev-h 1357, lib-b 233 | |
| NATS_URL | server.go:106, 154, 192 | A W G | "" (required) | string | S-ish (dev-gw embeds creds in URL) | dev-h 1345, dev-gw, lib-b 228, lib-g 366 | |
| NATS_USER | internal/natsutil/connect.go:28 | A W G | "" | string | | dev-h 1348, lib-b 229 (secret_ref), lib-g 367 | |
| NATS_PASSWORD | natsutil/connect.go:28 | A W G | "" | string | **S** | dev-h 1349, lib-b 230 (secret_ref), lib-g 368 | |
| STREAMING_DRIVER | server.go:107, 155 | A W | nats | enum memory/nats | | dev-h 1344, lib-b 227 | |
| CORS_ALLOWED_ORIGINS | server.go:111 (default `corsDefault := "*"`, :110) | A | * | csv | | lib-a 336 | **dev sets `CORS_ORIGINS` (1397), which reliant never reads** |
| TLS_CERT_FILE / TLS_KEY_FILE | server.go:114-115, 195-196; daemon.go:941-942 | A G D | "" | path | | — | |
| DISABLE_TLS | server.go:116, 197 | A G | false | bool | | dev-h 1405, lib-a 335, lib-g 372 | |
| JWT_PUBLIC_KEY | server.go:119 | A | "" | PEM | (public) | lib-a 331, lib-g 374 | **lib-g sets it on the gateway, but the gateway has no such flag** (server.go:181-197): dead |
| JWT_PUBLIC_KEY_FILE | server.go:120 | A | "" | path | | — | |
| RELIANT_JWT_PUBLIC_KEY | internal/serverapi/run.go:122 | A | "" | PEM | | — | duplicate of JWT_PUBLIC_KEY (fallback) |
| RELIANT_JWKS_URL | server.go:121 + serverapi/run.go:128 | A | "" | url | | dev-h 1361, lib-a 316 | read twice (flag + raw fallback) |
| ALLOWED_EMAIL_DOMAINS | server.go:80 | A | "" | csv | | — | |
| AUTH_REQUIRE_EMAIL_VERIFIED_CLAIM | internal/auth/jwt.go:220 (const :96) | A | — | bool | | lib-a 342 | |
| AUTH_MODE | internal/auth/middleware.go:28 | A | supabase | enum apikey/supabase | | — | self-host only |
| AUTH_API_KEY | auth/middleware.go:76; internal/grpc/interceptors/auth.go:122; internal/grpc/server.go:377 | A | "" | string | **S** | — | 3 readers |
| RELIANT_AUTHORIZATION_SERVER | internal/grpc/server.go:242, :522 (const internal/cliauth/cliauth.go:232) | A | "" | url | | dev-a ~2786, prod 556 | |
| PUBLIC_URL | serverapi/run.go:211, 382, 419; serverworker/run.go:325; integrations/webhook/wiring.go:129 (injected getenv) | A W | "" | url | | dev-a ~2780, lib-a 317, prod 518 | |
| MCP_OAUTH_ISSUERS | serverapi/run.go:420 | A | "" | csv | | dev-a ~2781, lib-a 318 | |
| INTERNAL_SERVICE_SECRET | grpc/server.go:249; serverapi/run.go:222; serverworker/run.go:189; tokenauthority/authority.go:35; serverworker/connections.go:67; serverapi/github_access.go:100 | A W G | "" | string | **S** | cfg (control-plane `config_gen.k:441,466` → `_host_env`); dev-gw via cfg | **6 readers** |
| RELIANT_CONTROL_PLANE_URL → CONTROL_PLANE_API_URL → CONTROL_PLANE_BASE_URL | tokenauthority/authority.go:24; internal/controlplane/client.go:180, 194; grpc/server.go:984; serverworker/connections.go:123; serverapi/github_access.go:126 | A W G | "" (controlplane/client.go:180 falls back to `defaultBaseURL`) | url | | dev-h 1358, dev-gw ~2897, lib-b 240, lib-g 381 | **one 3-name chain copy-pasted 6×**; only the first name is ever set by CP |
| RELIANT_API_BASE_URL | internal/llm/drivers/reliant_base_url.go:33 | W A | — | url | | dev-h 1359, lib-b 249 | |
| RELIANT_VAULT_KEY | internal/vault/boot.go:54 (const envkey.go:13) | A W | "" (required when hosted) | `v1:<b64>` | **S** | dev-h 1335, lib-b 277 (secret_ref) | |
| RELIANT_OAUTH_<ID>_CLIENT_ID / _CLIENT_SECRET | internal/connections/providers.go:32-34 (name built), :309-310; webhook/wiring.go:113 | A W | "" | string | **S** (secret) | prod (`reliant-slack`, `reliant-gmail-oauth` secret_refs) | **dynamic family**: one pair per integration manifest |
| RELIANT_SLACK_SIGNING_SECRET | webhook/wiring.go:97 (const webhook/slack.go:47) | A W | "" | string | **S** | prod | |
| RELIANT_GITHUB_WEBHOOK_SECRET | webhook/wiring.go:105; serverapi/github_access.go:60, 86 (const webhook/github/parse.go:41) | A W | "" | string | **S** | prod | |
| RELIANT_TEST_INTEGRATION_SECRET | webhook/wiring.go:134 | A W (test hook) | "" | string | S | — | test-only seam in prod code |
| WORKSPACE_BASE_DOMAIN | internal/llm/tools/preview_url.go:87 | A W D | — | string | | dev-h 1422, dev-gw, lib-b 270 | |
| WORKSPACE_URL_SCHEME | preview_url.go:97 | A W D | https | string | | dev-h 1423 | |
| WORKSPACE_URL_PORT | preview_url.go:102 | A W D | "" | int | | dev-h 1424 | |
| PROXY_HOST | preview_url.go:84 | A W D | "" | string | | — | the comment at dev/main.k:1418 mentions `RELIANT_PROXY_HOST`, which nothing reads |
| MAX_UPLOAD_SIZE | internal/grpc/services/attachment.go:36 | A | 10 MiB | int64 bytes | | — | |
| RELIANT_DISABLE_FORGE_API | grpc/server.go:962 | A | false | bool | | — | |
| RELIANT_PAYLOAD_BLOB_GC_HORIZON | internal/temporal/claimcheck/postgres.go:104 (const :32) | A W | 720h | duration | | — | |
| RELIANT_WORKFLOW_TIMEOUT_SECONDS | internal/workflow/constants.go:54 (package `var` init!) | A W | — | int seconds | | — | read at package init, before any loader can run |
| RELIANT_ENV | internal/runenv/runenv.go:43; observability.go:80 | all | "" → prod | enum | | dev-h 1425, dev-gw | `runenv.Get()` is called ad hoc everywhere |
| NODE_ENV | runenv/runenv.go:45 | all | — | enum | | — | fallback for RELIANT_ENV; drop |
| ENVIRONMENT | observability.go:80 | all | — | string | | — | 3-name chain `SENTRY_ENVIRONMENT > ENVIRONMENT > RELIANT_ENV` |
| SENTRY_ENABLED | internal/telemetry/init.go:41 | A W (+D, C via telemetry) | true unless "false" | bool | | prod (`_backend_sentry_env`) | |
| SENTRY_DSN | telemetry/init.go:46 | A W | "" | string | **S** | prod (secret_ref `control-plane-sentry`) | |
| SENTRY_ENVIRONMENT | telemetry/init.go:66; observability.go:80 | A W | "" | string | | prod | |
| SENTRY_TRACES_SAMPLE_RATE | telemetry/init.go:52 | A W | 0 | float | | prod | |
| OTEL_EXPORTER_OTLP_ENDPOINT | internal/observability/observability.go:56 | A W G | "" | url | | — | |
| OTEL_ENABLED | observability.go:63 | A W G | false | bool | | — | |
| PROMETHEUS_ENABLED | observability.go:64 | A W G | true | bool | | — | |
| STATSIG_CLIENT_KEY | internal/features/init.go:79; internal/analytics/client.go:47 (const :44) | A W | "" | string | **S** (CP stores it as `server_key`) | prod 510 (secret_ref) | |
| STATSIG_SERVER_SECRET_KEY | features/init.go:86-87; features/providers/statsig.go:81 | A W (dev only) | "" | string | **S** | — | |
| STATSIG_ENVIRONMENT | features/init.go:88-89; statsig.go:98 | A W | production | string | | — | |
| RELIANT_FEATURE_<KEY> | internal/features/providers/environment.go:56, 75, 86, 102, 119 (prefix :31) | A W D | — | dynamic | | — | **dynamic family**: stays bespoke (it is the flag provider, not config) |
| SUPABASE_URL / SUPABASE_ANON_KEY | internal/analytics/client.go:152-153 | A W | "" | url / key | anon key: public | dev-h 1360 (URL only) | |
| RELIANT_ANALYTICS_DISABLED | analytics/client.go:121 | A W D C | false | bool | | — | |
| RELIANT_APP_DATA_DIR | analytics/client.go:894; internal/config/loader.go:44 | D C | OS default | path | | — | |
| VERTEXAI_PROJECT / VERTEXAI_LOCATION | internal/llm/drivers/vertexai/driver.go:80-81, 108-109; vertexai/claude.go:539-540 | W A | — | string | | — | each read 3× |
| RELIANT_LLM_STREAM_IDLE_TIMEOUT | internal/llm/transport.go:280 (const :274) | W | 90s | duration | | — | read per call |
| RELIANT_LLM_STREAM_PROGRESS_TIMEOUT | transport.go:379 (:371) | W | 10m | duration | | — | |
| RELIANT_LLM_STREAM_CONTENT_STALL_TIMEOUT | transport.go:395 (:336) | W | 5m | duration | | — | |
| RELIANT_LLM_STREAM_MID_BLOCK_STALL_TIMEOUT | internal/llm/stream_stall.go:108 (:89) | W | derived from budget | duration | | — | |
| RELIANT_DEBUG_OPENAI_RESPONSES_INPUT / _TOOL_SCHEMAS | internal/llm/drivers/openai/driver.go:741, 821 | W | off | bool | | — | debug |
| RELIANT_INSTANCE_ID | internal/instanceid/instanceid.go:171 (const :66) | W D C | generated | string | | — | |
| LOG_FORMAT | internal/logging/logger.go:83, 139 (const :75) | all | text | enum json/text | | — | |
| RELIANT_LOG_LEVEL | logging/file_handler.go:95, 101, 128 (const :74) | all | info | enum | | — | |
| DEBUG / RELIANT_DEV_DEBUG | file_handler.go:66, 133 | all | false | bool | | — | legacy aliases for the debug log level |
| RELIANT_LOG_DIR | (electron only; `dev/main.k:2423`) | Electron | — | path | | dev | not read by Go |

### 1b. Daemon (D) — user machine or workspace pod; also CLI-launched

| Env var | Read at | Def | Type | S | Set by | Notes |
|---|---|---|---|---|---|---|
| DAEMON_GRPC_URL | cmd/reliant/commands/daemon.go:937 | "" | url | | op 2060 | |
| DAEMON_TLS_MODE | daemon.go:943 | "" | enum tls/insecure_tls_skip_verify/h2c/disabled | | — | |
| DAEMON_SERVER_MODE | daemon.go:946 | false | bool ("true") | | op 2063 | |
| DAEMON_LISTEN_PORT | daemon.go:947 | 9190 | int | | — | |
| TOOLS_DAEMON_PORT | daemon.go:935 | "9190" | string | | — | collides with G's int flag (§1a) |
| DAEMON_DATA_DIR | daemon_instance.go:50 (const :40) | "" | path | | op 2021 | |
| DAEMON_WORKING_DIR | internal/toolexec/daemonruntime/runtime.go:335 | cwd | path | | op 2020 | |
| DAEMON_FRONTEND_PORT | runtime.go:249; daemonruntime/preview_forwarder.go:298 | — | int | | op 2022 | |
| DAEMON_RUNTIME_TYPE | runtime.go:354 | — | string | | op 2298 | |
| RELIANT_DAEMON_TYPE | daemonruntime/daemontype.go:50 (const :36) | local | enum | | op 2032 (`managed`) | |
| RELIANT_DAEMON_NON_INTERACTIVE | daemon.go:123 | false | bool | | — | |
| RELIANT_INSTANCE_ACCOUNT | daemon.go:582, daemon_instance.go:51 (const :59) | "" | string | | — | |
| RELIANT_INSTANCE_WORKSPACE | internal/daemoninstance/daemoninstance.go:151 (const :94); daemon_instance.go:87 | "" | string | | — | |
| RELIANT_WORKSPACE_ID / RELIANT_ORG_ID | internal/logging/daemon_logger.go:81, 84 (const :21-22) | "" | string | | — | log fields only |
| PROJECT_PATH | runtime.go:1815 | — | path | | — | |
| RELIANT_WORKTREE_DIR | internal/daemon/configloader/loader.go:434 | ~/.reliant/worktrees | path | | — | |
| RELIANT_PREVIEW_URL_TEMPLATE | cmd/reliant/commands/preview.go:27 | "" | string | | op 2318 | |
| RELIANT_CHROME_BROWSER_URL | internal/mcp/manager.go:1420 | "" | url | | — | |
| RELIANT_CHILD_PRIORITY | internal/osutil/priority.go:25 (const :22) | low | enum | | — | |
| RELIANT_USER_CONFIG_DIR | internal/config/loader.go:29 | OS default | path | | — | **locates the product user config**; stays |
| RELIANT_SKIP_TLS_VERIFY | cmd/reliant/commands/connection.go:332 | off ("1") | bool | | — | |
| FRONTEND_PORT / RELIANT_WEB_ORIGIN | internal/auth/oauthhelper/oauthhelper.go:495, 502 | — | int / url | | — (FRONTEND_PORT set for control-plane's own procs) | D (`daemonruntime/cmd_oauth_helper.go`) + C (`auth_serve.go`) |
| FORGE_HOME | internal/cliauth/cliauth.go:137 | ~/.forge | path | | — | forge-owned name |
| FORGE_CREDENTIAL_HELPER | cmd/reliant/commands/forge_credential.go:88, 126 (`cloudcred.HelperEnv`) | — | string | | — | **forge-owned protocol**; not reliant config |
| (MCP `${VAR}` expansion) | configloader/loader.go:239, 474; grpc/services/mcp.go:655, 700 | — | — | | user | user-controlled, not config; stays |

### 1c. End-user CLI (C) — also used by Electron and the operator

| Env var | Read at | Def | S | Set by | Notes |
|---|---|---|---|---|---|
| RELIANT_SERVER_URL | connection.go:99, 138 (const :48); internal/daemoninstance/daemoninstance.go:141 (const :98) | ldflags `builddefaults.ServerURL` → `NeutralServerURL` | | op 2057 | 3-layer: env > build-time > neutral |
| RELIANT_GATEWAY_URL | connection.go:103, 187 (const :49) | ldflags | | op 2058 (plus `GATEWAY_URL` 2059, unread) | |
| RELIANT_TOKEN | connection.go:152; auth.go:138 (const :54) | "" | **S** | — | flag would leak it to shell history |
| RELIANT_AUTH_URL / RELIANT_AUTH_KEY | internal/auth/oauth.go:27, 31 (`builddefaults.Value`) | ldflags | publishable (not secret) | dev-h 1393-1394 | also allowlisted into the daemon env, `internal/daemonpolicy/env.go:129-130` |

### 1d. Ambient OS variables — not config, keep `os.Getenv` behind an allowlist

`HOME` (diskwatch/targets.go:112, copilot/auth.go:391, 394, terminal/shell.go:141), `SHELL` (cmdutil/finder.go:52, terminal/shell.go:25, 44), `USERPROFILE`/`HOMEDRIVE`/`HOMEPATH` (terminal/shell.go:129, 132), `LOCALAPPDATA` (copilot/auth.go:388), `XDG_CONFIG_HOME` (copilot/auth.go:385-386, cliauth.go:138), `HOSTNAME` (observability.go:53), `DOCKER_HOST` (diskwatch/consumers.go:187), `GITHUB_TOKEN` (copilot/auth.go:371).

These describe the host, not this process's configuration. Declaring them would let a KCL value override the user's `$HOME`. Forge's guardrail needs a per-file opt-out for them: `//nolint:forbidigo` works today.

### 1e. Test/dev-tool only

- `REQUIRE_TEST_DB` (db/testutil.go:286), `TEMPORAL_DEBUG` (temporal/temporaltest/temporaltest.go:60), `DATABASE_URL` in testutil.
- `tools/reliant-dev/*`: `OPENROUTER_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`, `XAI_API_KEY`, `RELIANT_PROBE_API_KEY` (models_probe_run.go:104-108).
- Test `t.Setenv` only: `RELIANT_TEST_*`, `REPLAY_VERBOSE`, `GIT_CONFIG_*`.

None of these go in config.proto. P5a's pgtest move removes `REQUIRE_TEST_DB` and testutil's `DATABASE_URL`.

### 1f. Set by control-plane but read by nothing in reliant

| Name | Set at | Reality |
|---|---|---|
| CORS_ORIGINS | dev/main.k:1397 | reliant reads `CORS_ALLOWED_ORIGINS` (server.go:111). **Dev CORS is effectively `*`.** Fix in control-plane (rename), independently of P4. |
| AUTO_MIGRATE | dev/main.k:1426 | no reader (reliant migrates via `reliant db migrate up`) |
| JWT_PUBLIC_KEY on gateway | lib/env.k:374 | gateway has no JWT flag (server.go:181-197) |
| GATEWAY_URL | operator workspace_controller.go:2059 | no reliant reader |
| SERVICE_NAME / OTEL_SERVICE_NAME | dev-gw | reliant passes service names in code |
| RELIANT_PROXY_HOST | comment only, dev/main.k:1418 | reliant reads `PROXY_HOST` |

Keep these names working anyway, unchanged (rule 3), until control-plane drops them in its own PR.

### 1g. Dead or duplicate code that P4 deletes

- `internal/envutil` has 0 non-test callers.
- Three flag-helper twins: `serverEnvOrDefault*` (server.go:32-58), `envOrDefault*` (root.go:63-90) and `envInt` (db/connect.go:223). All three are replaced by `config.RegisterFlags`.
- The 6× `RELIANT_CONTROL_PLANE_URL` chain becomes one field.
- `DATABASE_URL`/`DATABASE_DRIVER` raw reads in `db/connect.go:103-112` sit beside the flags.
- `RELIANT_JWT_PUBLIC_KEY` and `RELIANT_JWKS_URL` raw fallbacks (serverapi/run.go:122, 128) sit beside the flags.
- `NODE_ENV` and `ENVIRONMENT` fallbacks.

## 2. Proposed config proto layout

### 2.1 Shape

One file per role-scope under `proto/config/v1/`. Shared blocks are DEFINITIONS that each role composes; this is the pattern forge documents in the scaffold header, `config.proto` lines 33-66. Tags are fresh, because reliant has no prior config proto.

```proto
// ---- shared blocks (no binary_config) ---------------------------------
message LogConfig {            // every role
  string level  = 1 [(forge.v1.config) = {env_var:"RELIANT_LOG_LEVEL", flag:"log-level", default_value:"info", allowed_values:["debug","info","warn","error"]}];
  string format = 2 [(forge.v1.config) = {env_var:"LOG_FORMAT", flag:"log-format", default_value:"text", allowed_values:["text","json"]}];
  bool   debug  = 3 [(forge.v1.config) = {env_var:"RELIANT_DEV_DEBUG"}];          // DEBUG alias: see F3d
}
message RuntimeConfig {        // every role
  string environment = 1 [(forge.v1.config) = {env_var:"RELIANT_ENV", role: MODE}]; // NODE_ENV fallback dropped (PR 9)
  string instance_id = 2 [(forge.v1.config) = {env_var:"RELIANT_INSTANCE_ID"}];
  bool analytics_disabled = 3 [(forge.v1.config) = {env_var:"RELIANT_ANALYTICS_DISABLED"}];
}
message TelemetryConfig {      // A W G
  bool   sentry_enabled = 1 [(forge.v1.config)={env_var:"SENTRY_ENABLED", default_value:"true"}];
  string sentry_dsn     = 2 [(forge.v1.config)={env_var:"SENTRY_DSN", sensitive:true, optional:true}];
  string sentry_environment = 3 [(forge.v1.config)={env_var:"SENTRY_ENVIRONMENT"}];
  double sentry_traces_sample_rate = 4 [(forge.v1.config)={env_var:"SENTRY_TRACES_SAMPLE_RATE", default_value:"0"}];
  string otlp_endpoint  = 5 [(forge.v1.config)={env_var:"OTEL_EXPORTER_OTLP_ENDPOINT"}];
  bool   otel_enabled   = 6 [(forge.v1.config)={env_var:"OTEL_ENABLED"}];
  bool   prometheus_enabled = 7 [(forge.v1.config)={env_var:"PROMETHEUS_ENABLED", default_value:"true"}];
}
message DatabaseConfig {       // A W G (+ `reliant db migrate`)
  string url        = 1 [(forge.v1.config)={env_var:"DATABASE_URL", flag:"db-url", required:true, sensitive:true}];
  string direct_url = 2 [(forge.v1.config)={env_var:"DATABASE_DIRECT_URL", flag:"db-direct-url", sensitive:true, optional:true}];
  string driver     = 3 [(forge.v1.config)={env_var:"DATABASE_DRIVER", flag:"db-driver", default_value:"postgres", allowed_values:["postgres"]}];
  int32  max_open_conns = 4 [(forge.v1.config)={env_var:"RELIANT_DB_MAX_OPEN_CONNS", default_value:"25"}];
  int32  max_idle_conns = 5 [(forge.v1.config)={env_var:"RELIANT_DB_MAX_IDLE_CONNS", default_value:"10"}];
  int32  schema_wait_seconds = 6 [(forge.v1.config)={env_var:"RELIANT_DB_SCHEMA_WAIT_SECONDS", default_value:"300"}];
}
```

`sensitive` fields get no flag, per the secrets skill. **That breaks the existing `--db-url` / `--db-direct-url` flags.** See open question Q2. The recommendation is to keep the flags for one release, as `optional` non-sensitive aliases, or to drop them; nothing in control-plane passes them.

More shared blocks:

| Block | Roles | Fields (env name) |
|---|---|---|
| **NatsConfig** | A W G | `url` (NATS_URL, required, **sensitive**: dev-gw embeds creds in it), `user` (NATS_USER), `password` (NATS_PASSWORD, sensitive), `streaming_driver` (STREAMING_DRIVER, nats; allowed memory/nats) |
| **TemporalConfig** | A W | TEMPORAL_HOST (localhost), TEMPORAL_PORT (7233), TEMPORAL_NAMESPACE (reliant), `payload_gc_horizon` Duration (RELIANT_PAYLOAD_BLOB_GC_HORIZON, 720h), `workflow_timeout_seconds` (RELIANT_WORKFLOW_TIMEOUT_SECONDS) |
| **ControlPlaneConfig** | A W G | `url` (RELIANT_CONTROL_PLANE_URL), `api_base_url` (RELIANT_API_BASE_URL), `internal_service_secret` (INTERNAL_SERVICE_SECRET, sensitive). The two legacy aliases are covered by F3d. |
| **ServerListenConfig** | A G | BIND_ADDRESS, HEALTH_PORT (default **per role**, so set via F3c), TLS_CERT_FILE, TLS_KEY_FILE, DISABLE_TLS, DATA_DIR |
| **PreviewConfig** | A W D | WORKSPACE_BASE_DOMAIN, WORKSPACE_URL_SCHEME (https), WORKSPACE_URL_PORT, PROXY_HOST |
| **LLMConfig** | W (A for settings validation) | the four RELIANT_LLM_STREAM_* Durations, VERTEXAI_PROJECT, VERTEXAI_LOCATION, the two RELIANT_DEBUG_OPENAI_* bools |
| **FeatureConfig** | A W | STATSIG_CLIENT_KEY (sensitive), STATSIG_SERVER_SECRET_KEY (sensitive), STATSIG_ENVIRONMENT (production), SUPABASE_URL, SUPABASE_ANON_KEY (not sensitive: publishable) |
| **IntegrationsConfig** | A W | RELIANT_VAULT_KEY (sensitive), RELIANT_SLACK_SIGNING_SECRET, RELIANT_GITHUB_WEBHOOK_SECRET (both sensitive), PUBLIC_URL. The `RELIANT_OAUTH_<ID>_*` family stays dynamic (see below). |

**Role messages** (each `binary_config`, see §2.3):

| Message | Composes | Own leaves |
|---|---|---|
| **ApiServerConfig** | Log, Runtime, Telemetry, Database, Nats, Temporal, ControlPlane, ServerListen, Preview, Feature, Integrations, LLM | GRPC_PORT (9090), PPROF_PORT (6060), CORS_ALLOWED_ORIGINS (*), JWT_PUBLIC_KEY, JWT_PUBLIC_KEY_FILE, RELIANT_JWKS_URL, ALLOWED_EMAIL_DOMAINS, AUTH_REQUIRE_EMAIL_VERIFIED_CLAIM, AUTH_MODE, AUTH_API_KEY (sensitive), RELIANT_AUTHORIZATION_SERVER, MCP_OAUTH_ISSUERS, MAX_UPLOAD_SIZE, RELIANT_DISABLE_FORGE_API |
| **WorkerConfig** | the same blocks minus ServerListen | HEALTH_PORT (8081), PPROF_PORT (6060) |
| **GatewayConfig** | Log, Runtime, Telemetry, Database, Nats, ControlPlane, ServerListen | TOOLS_DAEMON_PORT (9190, int), HEALTH_PORT default **8080**, PPROF_ADDR |
| **MonolithConfig** (`reliant server`) | the union | only ApiServerConfig's own leaves, so they aren't redeclared |
| **DaemonConfig** (`reliant daemon …`) | Log, Runtime, Preview | everything in §1b except the FORGE_* names, MCP expansion and RELIANT_USER_CONFIG_DIR; plus RELIANT_SERVER_URL, RELIANT_GATEWAY_URL, RELIANT_SKIP_TLS_VERIFY, RELIANT_APP_DATA_DIR, RELIANT_CHROME_BROWSER_URL, RELIANT_CHILD_PRIORITY. The daemon's `--port` becomes `DAEMON_LISTEN_PORT`; keep reading TOOLS_DAEMON_PORT as a deprecated alias until the operator stops (it doesn't set it today). |
| **CliConfig** (root, every other subcommand) | Log, Runtime | RELIANT_SERVER_URL, RELIANT_GATEWAY_URL, RELIANT_TOKEN (sensitive, no flag), RELIANT_AUTH_URL, RELIANT_AUTH_KEY, RELIANT_SKIP_TLS_VERIFY, RELIANT_APP_DATA_DIR, FRONTEND_PORT, RELIANT_WEB_ORIGIN |

What stays bespoke (no proto):

- `RELIANT_FEATURE_*`. It is a feature-flag *provider* with an open key space.
- `RELIANT_OAUTH_<ID>_CLIENT_{ID,SECRET}`. The key space is defined by the integration manifests. Keep `connections.OAuthClientEnv` and give it a `//nolint:forbidigo` with a reason. Long-term, forge could add a `map<string,string>` config with an `env_prefix`; that is F3e, low priority.
- MCP `${VAR}` expansion, ambient OS vars (§1d), `FORGE_HOME` and `FORGE_CREDENTIAL_HELPER` (forge's own names), `RELIANT_USER_CONFIG_DIR`, and the product user config.

**Build-time defaults** (`builddefaults` ldflags for RELIANT_SERVER_URL / RELIANT_GATEWAY_URL / RELIANT_AUTH_*) need a layer forge doesn't model: proto default < **ldflags default** < file < env < flag. Today forge's `default_value` is a literal. That is F3f in §2.3.

### 2.2 Mapping onto forge today

- `config.Load(cmd, msg)` already does defaults → file (`--config` / `FORGE_CONFIG`) → env → flags (`forge/pkg/config/filelayer.go:5-22`).
- `RegisterFlagsScoped(ScopeShared|ScopeOwn)` already exists to put composed-block flags on the root and own leaves on a subcommand (`forge/pkg/config/loader.go:154-216`). The runtime library is therefore **already subcommand-capable**. The gap is entirely in the annotation/codegen/KCL layer.
- Per-binary generation already emits `Register<Msg>Flags(cmd)` + `Load<Msg>(cmd)` + a KCL `schema <Msg>` + `<msg>EnvMap(c, config_secrets)` (verified in a throwaway project; see §6). So reliant can **hand-call** `RegisterApiServerConfigFlags(serverAPICmd)` today, whatever `binary:` says.

### 2.3 Exactly what forge must add (F3)

- **F3a — validate `binary`.** `binary_config.binary` must resolve, or generation errors. Today any string is accepted silently (repro §6), which breaks the documented contract at `forge/proto/forge/v1/forge.proto:396-397`.
- **F3b — role/subcommand scope.** Add `repeated string command = 2;` (cobra path below the binary) to `BinaryConfigOptions`:
  ```proto
  message BinaryConfigOptions {
    string binary = 1;                 // cmd/<name>
    repeated string command = 2;       // e.g. ["server","api"]; empty = the binary root
  }
  // reliant:
  option (forge.v1.binary_config) = {binary:"reliant", command:["server","api"]};
  ```
  Key `(binary, command)`. A root config for the same binary (empty `command`) is the persistent/shared surface. Generate:
  1. `pkg/config`: `RegisterReliantServerApiFlags(cmd)` / `LoadReliantServerApi(cmd)`. Same as today, but named from the path.
  2. A lint, **`forgeconv-config-command-reach`**: the cobra command at that path must call the generated `Load…`. That makes "declared but unloaded" a lint error. Forge already has a config-reach lane to extend.
  3. KCL: `ApiServerConfig` schema + env map as today. **The deploy binding moves to the workload**: `fw.Workload{command=["reliant","server","api"], config=cfg.api_server}`. Forge resolves the config by matching `command[1:]` to `command`, so a workload carries only its role's env. That is exactly control-plane's hand-maintained `_reliant_worker_host_env = [e for e in _reliant_host_env if e.name not in [...]]` (dev/main.k:1448), derived instead of hand-filtered.
- **F3c — per-role defaults for a shared block field.** `HEALTH_PORT` is 8081 (api, worker) but 8080 (gateway). Allow a role message to override a composed block's default:
  ```proto
  ServerListenConfig listen = 1 [(forge.v1.config_defaults) = {key:"health_port", value:"8080"}];
  ```
  The alternative is to keep HEALTH_PORT as an own leaf per role, which needs no forge change. Do that first and add F3c only if a second case appears.
- **F3d — env aliases** (`repeated string env_var_aliases`, first set wins; lint warns when only an alias is set). This is needed to collapse the `RELIANT_CONTROL_PLANE_URL > CONTROL_PLANE_API_URL > CONTROL_PLANE_BASE_URL` chain and `RELIANT_LOG_LEVEL` vs `DEBUG`/`RELIANT_DEV_DEBUG` without breaking a setter (rule 3). Without it, reliant has to keep a hand-written shim.
- **F3e (later)** — prefix-map config for open key spaces (`RELIANT_OAUTH_*`, `RELIANT_FEATURE_*`).
- **F3f — build-time default layer.** A field option `build_default: true` generates an `-ldflags -X` target that sits between the proto default and the file layer. It replaces `internal/builddefaults`. The alternative is to keep `builddefaults` and pass its value as the proto default at registration. That needs a `RegisterFlagsWithDefaults(cmd, msg, map)` hook in `pkg/config`, which is smaller; prefer it.

**Devil's advocate: split binaries instead (PLAN Q3).** Using `cmd/reliant-server` + `cmd/reliant` would fit forge with zero F3b work. But:

- control-plane's `workloads.k:639,653` and `dev/main.k:2748` hard-code `reliant server api|worker`, and the gateway image ENTRYPOINT is `reliant server gateway`. A split is a 3-PR rule-3 dance across two repos plus Dockerfiles.
- Monolith mode (`reliant server`) and the desktop binary still need all roles in one process. That forces a third binary or the same per-command problem again.
- Forge's own control-plane is *already* "one binary, many subcommands" (control-plane `proto/config/v1/config.proto:30-44`), and it dodged the issue only by sharing one AppConfig across all subcommands, which leaks secrets to every role.

So F3b fixes forge for its own dogfood project too. **Recommend F3a+F3b+F3d in forge first (rule 7); F3c/e/f are optional.**

## 3. Secrets

**Sensitive fields:**

| Group | Fields |
|---|---|
| Database and transport | DATABASE_URL, DATABASE_DIRECT_URL, NATS_URL (when creds are embedded), NATS_PASSWORD |
| Service-to-service and vault | INTERNAL_SERVICE_SECRET, RELIANT_VAULT_KEY |
| Auth | AUTH_API_KEY |
| Telemetry and flags | SENTRY_DSN, STATSIG_CLIENT_KEY (CP stores the *server* key under it, prod/main.k:510), STATSIG_SERVER_SECRET_KEY |
| Integrations | RELIANT_SLACK_SIGNING_SECRET, RELIANT_GITHUB_WEBHOOK_SECRET, RELIANT_OAUTH_*_CLIENT_SECRET (bespoke family), RELIANT_TEST_INTEGRATION_SECRET (should move out of prod code) |
| CLI | RELIANT_TOKEN |

**Not sensitive despite their names:** SUPABASE_ANON_KEY and RELIANT_AUTH_KEY (publishable `sb_publishable_…`, inline at dev/main.k:1394), JWT_PUBLIC_KEY, and the `RELIANT_OAUTH_*_CLIENT_ID`s.

**How control-plane injects them today, and how that maps onto forge:**

| Today (control-plane) | Forge model | Change needed |
|---|---|---|
| Cloud: `forge.EnvVar{secret_ref=…, secret_key=…}` → k8s `secretKeyRef` (lib/env.k:217-230, 277; prod/main.k 476-510) | `sensitive:true` → `config_gen.k` `ConfigSecretRef{name,key}` default `{reliant-secrets, <env lower>}`; projected as `valueFrom.secretKeyRef` | None in reliant until F6 (control-plane consuming reliant's `config_gen.k`). Until then CP keeps hand-writing the refs, **names unchanged**. Nothing breaks, because the env NAME is the contract. |
| Dev host: secret_ref values come from control-plane's `secrets/dev.yaml` via `FileSecrets` (`RELIANT_VAULT_KEY` dev/main.k:1335, `INTERNAL_SERVICE_SECRET` via `_host_env`/cfg), or inline literals (DATABASE_URL 1337, NATS_PASSWORD 1349) | `FileSecrets{path="secrets/dev.yaml"}` | Reliant's *own* `scripts/dev.sh` stack would gain `reliant/secrets/dev.yaml` only when reliant gets its own `deploy/kcl/dev` (P8). For the control-plane-driven stack, no change. |
| `INTERNAL_SERVICE_SECRET` reaches reliant **only because** control-plane's `appConfigEnvMap` projects CP's AppConfig onto every host process (`_host_env`, dev/main.k:925-927) | F6: control-plane imports reliant's `config_gen.k` and binds `ApiServerConfig.control_plane.internal_service_secret` to the same `control-plane-secrets/internal_service_secret` ref | This coupling is accidental and fragile: it breaks if CP ever annotates its AppConfig per-binary. **Flag for F6.** |
| `forge doctor --signal deploy` "Deploy Secrets" check | Works once the names are declared sensitive in reliant's proto | Once P4 lands, run it against control-plane's render in CI. |

There is also a secrets lint win. Today a secret read by a role that shouldn't have it is invisible: `JWT_PUBLIC_KEY` on the gateway is harmless, but `INTERNAL_SERVICE_SECRET` reaches every host process. Per-role messages plus F3b workload binding make the set explicit.

## 4. Daemon and CLI on user machines (no KCL)

- **Loading.** `reliant daemon start|run` calls `config.LoadDaemon(cmd)` (generated). Precedence is proto default < (F3f build default) < config file (`--config` / `FORGE_CONFIG`) < env < flag. Forge's file layer is LOUD-on-missing when a path is given and silent when none is (`filelayer.go:18-25`), which is right for a user machine.
- **Config file location.** Forge reads a file only when `--config` or `FORGE_CONFIG` is set. Reliant must **not** point that at `~/.reliant/config.*`: that file is the *product* user config (`config/reliant-schema.json`, located via `RELIANT_USER_CONFIG_DIR`, `internal/config/loader.go:29,44`), with its own schema, MCP servers, presets, etc. Keep them disjoint. The process-config file is opt-in and has no default path; Electron passes env vars as today.
- **`FORGE_CONFIG` as the env name** for reliant's daemon is odd for end users. Open question Q3: ask forge for a per-project override of `ConfigPathEnv` (e.g. `RELIANT_CONFIG`) via `forge.yaml config.path_env`. Small, and it goes into F3.
- **Workspace pods** (the operator sets the §1b names at `workspace_controller.go:2016-2318`) are the same DaemonConfig, set via env. The operator is Go code in control-plane, so rule 3 covers **Go string literals**, not just KCL. The P4 check must grep both (§5).
- **What stays bespoke:**
  - product user config and `RELIANT_USER_CONFIG_DIR`;
  - MCP `${VAR}` expansion (configloader/loader.go:239, 474; grpc/services/mcp.go:655, 700);
  - ambient OS vars;
  - `RELIANT_FEATURE_*`;
  - FORGE_HOME / FORGE_CREDENTIAL_HELPER;
  - the daemon's env allowlist for child processes (`internal/daemonpolicy/env.go:97,129-130`). That one passes the environment through rather than reading config.
- **Telemetry on user machines.** SENTRY_* / analytics are read by D and C via `internal/telemetry` and `internal/analytics`. They live in shared blocks so both the server and the daemon get them.

## 5. PR-by-PR rollout

Every PR leaves env names untouched.

**Universal check (run in CI as a test from PR 1 on), `TestEnvContract`:** a pinned file `research/forge-migration/env-contract.txt` (or `internal/configcontract/testdata`) lists every name control-plane sets. It is generated by:

```bash
grep -ho 'name = "[A-Z_0-9]*"' ../control-plane/deploy/kcl/{dev/main.k,lib/env.k,prod/main.k,e2e/main.k,dev-k8s/main.k}
rg -o 'Name: "[A-Z_]+"' ../control-plane/internal/operators/workspace/workspace_controller.go
```

The test asserts each name that reliant read before P4 is still either an `env_var`/alias in `proto/config/v1/*.proto` or an allowlisted bespoke read. This mechanizes rule 3.

**Per-PR gate:** `go build ./...`, `make test-short PKGS=<touched>`, `reliant forge lint --scope proto/config --scope pkg/config`. For PRs touching server flags, also `go run ./cmd/reliant server api --help` and diff the flag list against the previous version (no flag removed).

| # | PR | Size | Owns (disjoint) | Proves nothing broke |
|---|---|---|---|---|
| F | **forge first:** F3a (validate binary) + F3b (`command` path + reach lint) + F3d (aliases) + `config.path_env`. Release, then pin in reliant (rule 7). | M | forge `internal/codegen/config*`, `pkg/config`, `proto/forge/v1/forge.proto`, linter | forge `task test`; e2e fixture of a one-binary multi-command project |
| 1 | Add `proto/config/v1/{shared,api,worker,gateway,monolith,daemon,cli}.proto` and generated `pkg/config`. **No call site changes.** `forge.yaml config: {enforce_typed_access: warn, loader_package: pkg/config}`. Add `TestEnvContract`. Delete `internal/envutil` (0 callers). | M | `proto/config/`, `pkg/config/`, `forge.yaml` | build; contract test green; `forge lint` warn only |
| 2 | Server flag registration: replace `serverEnvOrDefault*` in `cmd/reliant/commands/server.go` with `Register*Flags` + `Load*`. Map loaded values into the existing `serverapi/serverworker/servergateway.Options`. **Identical flag names** (`--grpc-port`, `--db-url`, …). | M | `cmd/reliant/commands/server.go` | `server {api,worker,gateway} --help` flag diff is empty; `forge env up dev` boots api+worker (rule 4) |
| 3 | Daemon/CLI flags: `envOrDefault*` in daemon.go/daemon_instance.go/connection.go → `DaemonConfig`/`CliConfig`; `builddefaults` through the defaults hook | M | `cmd/reliant/commands/{daemon,daemon_instance,connection,auth,preview,root}.go`, `internal/builddefaults` | `reliant daemon --help` diff; Electron dev launch; operator env names (contract test) |
| 4a | DB: `internal/db/connect.go` takes `DatabaseConfig` (envInt, raw DATABASE_* reads gone) | S | `internal/db/connect.go`, `repo.go` | `DATABASE_URL=… REQUIRE_TEST_DB=1 make test-short PKGS=./internal/db/...` |
| 4b | Control-plane client: one `ControlPlaneConfig` passed into `tokenauthority`, `controlplane`, `grpc/server.go:984`, `serverworker/connections.go`, `serverapi/github_access.go`. Alias chain via F3d. | M | those 5 files | unit tests that inject getenv already exist (`github_access_test.go`), so convert them to a config struct |
| 4c | Telemetry/observability/runenv: `TelemetryConfig`, `RuntimeConfig` | S | `internal/telemetry`, `internal/observability`, `internal/runenv` | test-short |
| 4d | LLM: stream timeouts + vertex + openai debug → `LLMConfig` injected into the transport | M | `internal/llm/**` (22 sites) | test-short `./internal/llm/...` |
| 4e | Features/analytics: `FeatureConfig`; `RELIANT_FEATURE_*` keeps bespoke reads with nolint | S | `internal/features`, `internal/analytics` | test-short |
| 4f | Auth/API leaves: AUTH_*, JWT fallbacks, MCP_OAUTH, PUBLIC_URL, MAX_UPLOAD_SIZE, RELIANT_DISABLE_FORGE_API, AUTHORIZATION_SERVER | M | `internal/auth`, `internal/serverapi`, `internal/grpc`, `internal/grpc/services/attachment.go`, `internal/cliauth` | test-short; api boot in dev |
| 4g | Integrations/vault: `IntegrationsConfig`; OAuth family stays bespoke | S | `internal/vault`, `internal/integrations/webhook`, `internal/connections` | test-short |
| 4h | Daemon runtime: DAEMON_*, preview, mcp/chrome, osutil, logging fields | M | `internal/toolexec/daemonruntime`, `internal/daemon*`, `internal/mcp/manager.go`, `internal/osutil`, `internal/logging`, `internal/llm/tools/preview_url.go` | test-short; workspace pod in dev-k8s |
| 4i | Temporal/workflow: GC horizon, **`WorkflowExecutionTimeout` package-var init → injected** | S | `internal/temporal/claimcheck`, `internal/workflow/constants.go` | test-short |
| 5 | Annotate ambient-OS reads (§1d) and dynamic families with `//nolint:forbidigo // <reason>`; flip `enforce_typed_access: error` | S | scattered one-liners | `reliant forge lint` (unscoped) green |
| 6 | Delete the duplicate env fallbacks (NODE_ENV, ENVIRONMENT, RELIANT_JWT_PUBLIC_KEY, DEBUG): **only names control-plane does not set** (contract test enforces) | S | `pkg/config` proto | contract test |
| CP-1 | (control-plane, independent, can go first) rename `CORS_ORIGINS` → `CORS_ALLOWED_ORIGINS` on reliant env; drop AUTO_MIGRATE/APP_URL/JWT_PUBLIC_KEY-on-gateway | S | control-plane `deploy/kcl/dev/main.k`, `lib/env.k` | `forge env render dev` diff; dev CORS now enforced, so check the web app still loads |
| CP-2 | (after F6) control-plane consumes reliant's `config_gen.k`; delete the hand-filtered `_reliant_worker_host_env` | M | control-plane KCL | rendered env per workload byte-identical except removals |

**Ordering and parallel lanes:**

1. PR F, then PR 1.
2. PRs 2 and 3 run in parallel (disjoint files in `cmd/reliant/commands`).
3. 4a–4i run in parallel: 9 lanes, each owning only its packages. Each lane's role entrypoint receives the block from PR 2/3. The interim pattern, until the composition root lands in P3c, is that the entrypoint calls `pkg/config.Load…` once and passes the sub-block down. Don't use a global.
4. PR 5, then PR 6.

CP-1 is independent of everything else.

**Hard cases:**

- **`WorkflowExecutionTimeout`** (workflow/constants.go:53) is evaluated at package init, and **the four LLM stream timeouts are read per call so tests/operators can flip them live** (transport.go:276-278 comment). Injecting them changes "live" to "boot-time". That's acceptable (no CP setter), but say so in the PR.
- **`runenv.Get()`** is called from 6 packages without plumbing. Keep `runenv` as the one place that holds the loaded `RuntimeConfig` (set once at boot) rather than threading it everywhere. The alternative is threading it through the call chains to those 6 packages.

## 6. Forge gaps hit, and open questions

**Repro, F3a (silent invalid binary).** In a throwaway project:

```bash
d=$(mktemp -d); cd $d && reliant forge project new x --mod example.com/x --disable frontend   # exit 0; cmd/ = [x]
cat >> x/proto/config/v1/config.proto <<'EOF'
message ServerApiConfig {
  option (forge.v1.binary_config) = {binary: "x server api"};
  string jwks_url = 1 [(forge.v1.config) = {env_var: "RELIANT_JWKS_URL", flag: "jwks-url"}];
}
EOF
cd x && reliant forge generate     # ✅ Code generation complete — 10 file(s) written, 4 changed
grep -n ServerApi pkg/config/config_gen.go   # :108 "ServerApiConfig is the x server api binary's configuration." + Register/Load emitted
sed -i 's/"x server api"/"nonexistent"/' proto/config/v1/config.proto && reliant forge generate
# ✅ Code generation complete — 10 file(s) written, 3 changed   ← no error
grep -c ServerApi deploy/kcl/config_gen.k pkg/config/config_gen.go   # 7, 13
```

The contract violated is `forge/proto/forge/v1/forge.proto:396-397`: "A message annotated with a binary that has no cmd/<name> directory is a generate-time error". The silver lining is that the runtime pieces reliant needs (scoped flags, `Load<Msg>`, the KCL env map) already generate for any message. F3b is about making the binding *meaningful and checked* (workload projection plus reach lint), not about new runtime.

**Other friction:**

- `reliant forge project libraries config` worked from `forge/`. PLAN F10 notes it fails from `control-plane/`; I didn't re-test that.
- The secrets skill says sensitive fields register **no flag**. Reliant has `--db-url`, `--db-direct-url` and `--nats-url` flags that are in use in docs/scripts. That is a behavior change that forge forces.

**Open questions:**

1. **Q1 binary shape (PLAN Q3).** This file recommends one binary plus F3b. Confirm.
2. **Q2.** OK to drop `--db-url` / `--db-direct-url` (sensitive ⇒ no flag)? control-plane never passes them. Check `scripts/dev.sh` and `.air*.toml` before PR 2.
3. **Q3.** Should forge add a per-project `ConfigPathEnv` override (`RELIANT_CONFIG` instead of `FORGE_CONFIG`) for user-machine binaries?
4. **Q4.** `STATSIG_CLIENT_KEY` holds a *server* key in prod (`prod/main.k:510`, `secret_key = "server_key"`) while code treats it as a client key (`features/init.go:79`). Rename it in a rule-3 3-step, or document it?
5. **Q5.** Fix `CORS_ORIGINS` (CP-1) now, independent of P4? It means dev CORS is wide open today.
6. **Q6.** F6 coupling: reliant's `INTERNAL_SERVICE_SECRET` in dev arrives only through control-plane's own AppConfig projection. Should P4 add an explicit `forge.EnvVar` for it on `_reliant_host_env`, so it survives control-plane per-binary-izing its config?
7. **Q7.** Should `RELIANT_TEST_INTEGRATION_SECRET` (prod code path, webhook/wiring.go:134) be removed in P4, or stay as a declared test seam?

## Dependencies on other areas

- P1, for `forge.yaml` and `pkg/` to exist.
- P3c, where the composition root replaces the interim "entrypoint passes sub-blocks".
- P8/F6, for control-plane to consume reliant's `config_gen.k`.
- P5a, where pgtest removes `REQUIRE_TEST_DB` and testutil's `DATABASE_URL`.
