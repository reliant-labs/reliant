# Fixture for cmd/reliant/cmd TestOrgInstallationTriggersGateReachesTheTriggerHandler:
# the API server's env as a deployment projects it, once at the schema default
# (the rollout gate off, as every env ships until reliant#2653) and once with
# orgs.github_installation_triggers_enabled set. It goes through the same
# config_gen.workloadEnvMap and workloads.api_server binding every env uses, so
# a default that flips, an env-map entry that drops, or the variable leaving
# the api root's reach fails the test.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen
import workloads

_process = lambda api: config_gen.ApiServerConfig -> config_gen.ReliantProcess {
    config_gen.ReliantProcess {
        reliant_config = config_gen.ReliantConfig {}
        server_process_config = config_gen.ServerProcessConfig {}
        database_config = config_gen.DatabaseConfig {}
        api_server_config = api
    }
}

# The API server's env with the orgs gates at their schema default.
gate_default = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {}), workloads.api_server)

# The API server's env with installation-bound triggers turned on.
gate_on = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {orgs = {github_installation_triggers_enabled = True}}), workloads.api_server)
