# Fixture for cmd/reliant/cmd TestModelsLoadsCLIConfigThroughKCL: a `reliant models`
# workload declared with no cli_config. The models group loads CLIConfig, which has
# no required field, so reliantProcessEnvMap projects it as its defaults (forge
# #2659): the same env as an explicit `cli_config = config_gen.CLIConfig {}`.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen

_argv = ["reliant", "models", "list"]

missing_cli_config = config_gen.reliantProcessEnvMap(config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {}
}, _argv, [])

default_cli_config = config_gen.reliantProcessEnvMap(config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {}
    cli_config = config_gen.CLIConfig {}
}, _argv, [])
