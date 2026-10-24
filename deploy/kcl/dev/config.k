# Per-environment app config VALUES for reliant's own dev stack (user-owned —
# edit here). One typed instance per binary subcommand, each carrying only the
# values THAT process reads; the schemas and env projections are generated
# from proto/reliant/config/v1 (config_gen.k). Only fields this env pins are
# set — every other field inherits its schema default.
#
# The infrastructure DSNs (DATABASE_URL, NATS_URL) are credentials in the
# schema, so they are not set here: main.k composes them from this machine's
# ports and they override the secret reference. RELIANT_VAULT_KEY and
# AUTH_API_KEY come from secrets/dev.yaml, which `forge env up dev` generates.

import config_gen
import .ports

_web_origin = "http://localhost:${ports.web}"

# The `reliant server api` process.
api_server_config: config_gen.ApiServerConfig = {
    grpc_port = ports.api
    health_port = ports.api_health
    pprof_port = ports.api_pprof
    bind_address = "127.0.0.1"
    cors_allowed_origins = _web_origin
    # One shared bearer key: there is no identity provider in this stack.
    auth_mode = "apikey"
    auth_api_key = config_gen.ConfigSecretRef {name = "reliant-secrets", key = "AUTH_API_KEY"}
}

# Every process at or below `reliant auth`, `reliant daemon`, `reliant project`, `reliant trigger`, `reliant workflow`.
cli_config: config_gen.CLIConfig = {
}

# Every `reliant` process (the binary's root config).
reliant_config: config_gen.ReliantConfig = {
    runtime = {environment = "dev"}
}

# The `reliant daemon start` process.
daemon_config: config_gen.DaemonConfig = {
}

# The `reliant daemon status` process.
daemon_status_config: config_gen.DaemonStatusConfig = {
}

# The `reliant daemon stop` process.
daemon_stop_config: config_gen.DaemonStopConfig = {
}

# The `reliant daemon logs` process.
daemon_logs_config: config_gen.DaemonLogsConfig = {
}

# The `reliant daemon register` process.
daemon_register_config: config_gen.DaemonRegisterConfig = {
}

# Every process at or below `reliant server`, `reliant db migrate`.
database_config: config_gen.DatabaseConfig = {
    database_driver = "postgres"
}

# The `reliant server gateway` process.
gateway_config: config_gen.GatewayConfig = {
    daemon_port = ports.gateway
    health_port = ports.gateway_health
    bind_address = "127.0.0.1"
}

# The `reliant preview-url` process.
preview_url_config: config_gen.PreviewURLConfig = {
}

# Every process at or below `reliant server`. control_plane is left unset:
# with no RELIANT_CONTROL_PLANE_URL every role takes its self-hosted path
# (control_plane_config.proto).
server_process_config: config_gen.ServerProcessConfig = {
    temporal = {
        host = "127.0.0.1"
        port = ports.temporal
        namespace = "reliant"
    }
    tls = {
        disable = True
    }
    # Logs and certs, under the checkout's gitignored .forge/.
    data_dir = ".forge/data/dev"
    # A developer's machine: fetch and model endpoints may reach localhost and
    # the LAN (an Ollama on the host). Every other env leaves this off.
    network = {
        allow_private_egress = True
    }
}

# The `reliant server worker` process.
worker_config: config_gen.WorkerConfig = {
    health_port = ports.worker_health
    pprof_port = ports.worker_pprof
}

reliant_process: config_gen.ReliantProcess = {
    reliant_config = reliant_config
    cli_config = cli_config
    database_config = database_config
    server_process_config = server_process_config
    api_server_config = api_server_config
    daemon_config = daemon_config
    daemon_status_config = daemon_status_config
    daemon_stop_config = daemon_stop_config
    daemon_logs_config = daemon_logs_config
    daemon_register_config = daemon_register_config
    gateway_config = gateway_config
    worker_config = worker_config
    preview_url_config = preview_url_config
}
