# Fixture for cmd/reliant/cmd TestIntegrationsLoadsCLIConfigThroughKCL: a `reliant
# integrations` workload declared with no cli_config. The integrations group loads CLIConfig,
# so reliantProcessEnvMap refuses it rather than render a process with no
# connection. Kept apart from cli_config_integrations.k because the refusal fails the
# whole file's evaluation.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen

missing_cli_config = config_gen.reliantProcessEnvMap(config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {}
}, ["reliant", "integrations", "list"], [])
