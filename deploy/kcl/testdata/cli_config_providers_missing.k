# Fixture for cmd/reliant/cmd TestProvidersLoadsCLIConfigThroughKCL: a
# `reliant providers` workload declared with no cli_config. The providers group
# loads CLIConfig, so reliantProcessEnvMap refuses it rather than render a
# process with no connection. Kept apart from cli_config_providers.k because
# the refusal fails the whole file's evaluation.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen

missing_cli_config = config_gen.reliantProcessEnvMap(config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {}
}, ["reliant", "providers", "list"], [])
