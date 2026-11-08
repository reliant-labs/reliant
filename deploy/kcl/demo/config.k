# Per-environment app config VALUES (user-owned — edit here).
# This project declares PER-BINARY configs: one typed instance below per
# binary (or binary subcommand), each carrying only the values THAT process
# reads. The schemas and their env projections are generated from
# proto/reliant/config/v1/config.proto;
# add or change FIELDS in the proto, values here. Only fields this env pins
# are set — every other field inherits its schema default.

import config_gen

# The `reliant server api` process.
api_server_config: config_gen.ApiServerConfig = {
}

# Every process at or below `reliant auth`, `reliant daemon`, `reliant github`, `reliant machine`, `reliant project`, `reliant trigger`, `reliant workflow`.
cli_config: config_gen.CLIConfig = {
}

# Every `reliant` process (the binary's root config).
reliant_config: config_gen.ReliantConfig = {
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

# Every process at or below `reliant server`, `reliant db migrate`, `reliant db vault`.
database_config: config_gen.DatabaseConfig = {
}

# The `reliant server gateway` process.
gateway_config: config_gen.GatewayConfig = {
}

# The `reliant preview-url` process.
preview_url_config: config_gen.PreviewURLConfig = {
}

# Every process at or below `reliant server`.
server_process_config: config_gen.ServerProcessConfig = {
}

# Every process at or below `reliant db vault`.
vault_maintenance_config: config_gen.VaultMaintenanceConfig = {
}

# The `reliant server worker` process.
worker_config: config_gen.WorkerConfig = {
}

# Every `reliant` process's config, for config_gen.reliantProcessEnvMap: a workload's env is
#     config_gen.reliantProcessEnvMap(appcfg.reliant_process, (w.command or []) + (w.args or []), w.config_secrets)
reliant_process: config_gen.ReliantProcess = {
    reliant_config = reliant_config
    cli_config = cli_config
    database_config = database_config
    server_process_config = server_process_config
    vault_maintenance_config = vault_maintenance_config
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

# ── Frontend runtime config (PUBLIC — served to the browser) ──────────
# Projected into each frontend's config.js. These values reach the browser
# at RUNTIME, not baked into the bundle, so one built bundle can be
# promoted between environments.
import frontend_config_gen

reliant_web_config: frontend_config_gen.ReliantWebConfig = {
}
