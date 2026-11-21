# Fixture for cmd/reliant/cmd TestMCPLoadsCLIConfigThroughKCL: a workload's
# env for `reliant mcp list`, projected by the same
# config_gen.reliantProcessEnvMap every env's `reliant` process uses. The mcp
# group is one of CLIConfig's subtrees (cli_config.proto), so its env carries
# the CLI connection; a command outside every subtree (`reliant version`)
# carries none of it, with the same ReliantProcess.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen

_process = config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {}
    cli_config = config_gen.CLIConfig {
        connection.server_url = "https://api.example.test"
        connection.gateway_url = "https://gateway.example.test"
    }
}

mcp_list = config_gen.reliantProcessEnvMap(_process, ["reliant", "mcp", "list", "--project", "shop"], [])
outside_subtrees = config_gen.reliantProcessEnvMap(_process, ["reliant", "version"], [])
