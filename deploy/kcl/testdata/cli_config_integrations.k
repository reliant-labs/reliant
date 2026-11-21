# Fixture for cmd/reliant/cmd TestIntegrationsLoadsCLIConfigThroughKCL: a workload's
# env for `reliant integrations list`, projected by the same
# config_gen.reliantProcessEnvMap every env's `reliant` process uses. The integrations
# group is one of CLIConfig's subtrees (cli_config.proto), so its env carries
# the CLI connection.
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

integrations_list = config_gen.reliantProcessEnvMap(_process, ["reliant", "integrations", "list", "--integration", "github"], [])
