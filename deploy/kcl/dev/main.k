"""
reliant's own `dev` environment: the whole product on THIS machine, as host
processes, with no control plane and no container runtime.

    forge env up dev      # migrate, then api + worker + gateway, and the web app
    forge env down dev    # stops this checkout's stack only

WHAT RUNS. The three server roles (`reliant server api|worker|gateway`, the
same entrypoints prod runs) under air, so each one rebuilds and restarts when
a .go file changes; a `reliant db migrate up` job that gates them; and the
frontends/web Vite dev server. Underneath, forge runs the infrastructure as
supervised host processes (`forge.HostInfra`): postgres 16 (prod's major),
NATS with JetStream, and the Temporal dev server. Nothing here needs docker or k3d, so
it runs in a sandboxed cloud workspace and in a public clone that has no
control-plane checkout.

NO CONTROL PLANE. RELIANT_CONTROL_PLANE_URL is unset, so every server role
takes its self-hosted path (proto/reliant/config/v1/control_plane_config.proto):
reliant's own token store mints and validates daemon tokens, and LLM providers
are the per-user API keys and model endpoints entered in Settings. The API
authenticates with one shared bearer key (AUTH_MODE=apikey); forge generates it
into secrets/dev.yaml on first `forge env up dev`. Sign in to the web app with:

    grep AUTH_API_KEY secrets/dev.yaml

Then connect a daemon to this stack's gateway (the ports are the ones the
`forge env up` summary prints; ports.k derives them):

    KEY=$(sed -n 's/^AUTH_API_KEY: *//p' secrets/dev.yaml)
    curl -s localhost:<api>/reliant.v1.TokenService/CreateToken \
      -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
      -d '{"kind":"TOKEN_KIND_DAEMON","name":"dev"}'          # prints .token
    echo <token> | RELIANT_SERVER_URL=http://localhost:<api> \
      reliant daemon start --token --grpc-url http://localhost:<gateway>

and give the account a model: an API key under Settings, or a model endpoint
(any OpenAI-compatible server, Ollama included) — no provider key is needed.

control-plane's `forge env up dev` (../control-plane) remains the FULL-platform
stack — billing, the LLM gateway, managed workspaces — and needs k3d. This one
is the product alone. Until control-plane imports reliant's declarations (P8b,
research/forge-migration/12-p8-devtooling-plan.md), the two wire the same env
names by hand; keep them in step.

PARALLEL STACKS. Every port is `allocate_port(base, worktree)` (ports.k): the
primary checkout gets the bases, each linked worktree a disjoint 100-port
block, at most forge's default 8 stacks (`dev_stack.max_stacks`, the same cap
as control-plane's). Stack N owns 21N00-21N99. Each stack also keeps its own
data (postgres, NATS, Temporal under the checkout's .forge/hostinfra/), so two
worktrees never share a database, and `forge env down dev` stops only the
checkout it runs in. `forge env devstack list` shows the blocks; `forge env
devstack prune` reclaims a deleted worktree's.

LOGS. `.forge/logs/dev/<workload>.log` in this checkout: reliant-api-server.log,
reliant-temporal-worker.log and daemon-gateway.log (named as in
control-plane's stack), and frontend_web.log (browser console included). Host
infra logs to its data dir (.forge/hostinfra/<name>/<engine>.log).
"""

import forge
import forge.workloads as fw
import config_gen
import ..workloads as wl
import .config as appcfg
import .ports

# ── Runner ───────────────────────────────────────────────────────────────────
# air by default (hot reload). `-D host_runner=go-run` runs each role once,
# with no reload, on a machine without air.
_host_runner = option("host_runner", type="str", default="air", help="Host launch runner for the server roles: air (default, hot reload) or go-run")

_host = lambda air: str, listen: [int] -> forge.OnHost {
    forge.OnHost {
        runner = _host_runner
        if _host_runner == "air":
            air_config = air
        listen_ports = listen
    }
}

# ── Env ──────────────────────────────────────────────────────────────────────
# Each workload's env, layered last-wins: the typed config its root reaches
# (config.k, through config_gen.workloadEnvMap), then this machine's facts.
# The DSNs are credentials in the schema (a SecretRef by default); here they
# are composed from the ports above and override it, because a host process
# on this machine dials loopback, and a stale port in a secret store is
# exactly what connects an app to another stack's database.
_machine = {
    DATABASE_URL = "postgres://postgres:postgres@localhost:${ports.postgres}/reliant?sslmode=disable"
    NATS_URL = "nats://127.0.0.1:${ports.nats}"
}
_env = lambda w: fw.Workload -> {str:} {
    _cfg = config_gen.workloadEnvMap(appcfg.reliant_process, w)
    _own = w.env or {}
    _merged = {_k: _own[_k] if _k in _own else (_machine[_k] if _k in _machine else _v) for _k, _v in _cfg}
    {**_merged, **{_k: _v for _k, _v in _own if _k not in _merged}}
}

# ── The web app ──────────────────────────────────────────────────────────────
# frontends/web, the forge-adopted Vite app: `forge env up` dev-serves it
# (`npm run dev` on `port`), installs its node_modules when they are missing or
# older than the lockfile, and mirrors its browser console into
# .forge/logs/dev/frontend_web.log. Its name and path are the ones forge
# already discovers on disk (`forge project frontends`), so this declaration
# adds a runtime and a port, not a second identity for the same directory.
_web_origin = "http://localhost:${ports.web}"

_web = forge.Frontend {
    name = "web"
    type = "vite"
    path = "frontends/web"
    dev_runner = "npm"
    port = ports.web
    runtime = forge.OnHost {}
    env_vars = [
        # Vite proxies /reliant.v1.*, /health and the terminal WebSocket here.
        forge.EnvVar {name = "VITE_API_URL", value = "http://localhost:${ports.api}"}
        # What the onboarding's `reliant daemon start` command dials.
        forge.EnvVar {name = "VITE_GATEWAY_URL", value = "http://localhost:${ports.gateway}"}
        forge.EnvVar {name = "VITE_DISABLE_TLS", value = "true"}
        forge.EnvVar {name = "VITE_APP_URL", value = _web_origin}
        # VITE_CONTROL_PLANE_API_URL is deliberately unset: the web app's one
        # control-plane gate (src/services/controlPlane/config.ts) then takes
        # its self-hosted path.
    ]
}

# ── Bindings ─────────────────────────────────────────────────────────────────
# What a host process cannot carry, taken back off workloads.k's pod facts: a
# local process has no pod to mount into or rank (forge refuses `volumes` and
# `priorityClassName` on OnHost by name), no kubelet to probe it, and no
# container limit to size resources or GOMEMLIMIT from. release_dependencies
# come off too: this local stack is started whole by `forge env up` and never
# cut as a release, so no cohort is ever planned from them here.
_host_only = {
    release_dependencies = Undefined
    probes = Undefined
    resources = Undefined
    volumes = Undefined
    priorityClassName = Undefined
    go_memory_limit_percent = Undefined
}

_migrate = wl.migrate | _host_only | {
    runtime = forge.OnHost {runner = "go-run"}
}
_migrate = _migrate | {env = _env(_migrate)}

_api = wl.api_server | _host_only | {
    runtime = _host(".air.api.toml", [ports.api, ports.api_health])
    env = {PUBLIC_URL = "http://localhost:${ports.api}"}
}
_api = _api | {env = _env(_api)}

_worker = wl.temporal_worker | _host_only | {
    runtime = _host(".air.worker.toml", [ports.worker_health])
}
_worker = _worker | {env = _env(_worker)}

_gateway = wl.daemon_gateway | _host_only | {
    runtime = _host(".air.gateway.toml", [ports.gateway, ports.gateway_health])
}
_gateway = _gateway | {env = _env(_gateway)}

_bundle = forge.Bundle {
    project = "reliant"
    # This machine's own stack: forge creates everything it runs, so it may
    # hold plaintext dev secrets (FileSecrets).
    lifecycle = "local"
    workloads = [_migrate, _api, _worker, _gateway]
    frontends = [_web]
    infra = [
        forge.HostInfra {
            name = "postgres"
            port = ports.postgres
            database = "reliant"
            # Prod's major (Cloud SQL POSTGRES_16).
            version = "16"
        }
        forge.HostInfra {
            name = "nats"
            engine = "nats"
            port = ports.nats
            nats_monitor_port = ports.nats_monitor
        }
        forge.HostInfra {
            name = "temporal"
            engine = "temporal"
            port = ports.temporal
            temporal_namespaces = ["reliant"]
            temporal_ui_port = ports.temporal_ui
        }
    ]
    secret_provider = forge.FileSecrets {
        path = "secrets/dev.yaml"
        generate = {
            "RELIANT_VAULT_KEY" = forge.GeneratedSecret {bytes = 32, encoding = "base64", prefix = "v1:"}
            "AUTH_API_KEY" = forge.GeneratedSecret {bytes = 24, encoding = "hex", prefix = "rdev_"}
        }
    }
}

output = forge.render(_bundle)
