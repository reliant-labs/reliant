# Fixture for cmd/reliant/cmd TestPreviewLoadsCLIConfigThroughKCL: a `reliant
# preview` workload declared with no cli_config. The preview group loads CLIConfig,
# so reliantProcessEnvMap refuses it rather than render a process with no
# connection. Kept apart from cli_config_preview.k because the refusal fails the
# whole file's evaluation.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen

missing_cli_config = config_gen.reliantProcessEnvMap(config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {}
}, ["reliant", "preview", "access", "list", "build-box"], [])
