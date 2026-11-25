# Missing optional CLIConfig projects its default connection without credentials.
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen

missing_cli_config = config_gen.reliantProcessEnvMap(config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {}
}, ["reliant", "providers", "list"], [])
