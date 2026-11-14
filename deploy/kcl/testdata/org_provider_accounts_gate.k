# Fixture for cmd/reliant/cmd TestOrgProviderAccountsGateReachesTheService:
# the API server's env as a deployment projects it, at the schema default (the
# rollout gate off, as every env ships until the routing that serves org
# accounts is in every replica), as this repo's dev and demo envs render it,
# and with orgs.provider_accounts_enabled set. It goes through the same
# config_gen.workloadEnvMap and workloads.api_server binding every env uses, so
# a default that flips, an env-map entry that drops, or the variable leaving
# the api root's reach fails the test. prod's ApiServerConfig is composed by
# control-plane's lib/reliant_process.k, which sets no orgs field, so it takes
# the schema default gate_default pins.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen
import workloads
import dev.config as devcfg
import demo.config as democfg

_process = lambda api: config_gen.ApiServerConfig -> config_gen.ReliantProcess {
    config_gen.ReliantProcess {
        reliant_config = config_gen.ReliantConfig {}
        server_process_config = config_gen.ServerProcessConfig {}
        database_config = config_gen.DatabaseConfig {}
        api_server_config = api
    }
}

gate_default = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {}), workloads.api_server)
dev_api = config_gen.workloadEnvMap(devcfg.reliant_process, workloads.api_server)
demo_api = config_gen.workloadEnvMap(democfg.reliant_process, workloads.api_server)
gate_on = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {orgs = {provider_accounts_enabled = True}}), workloads.api_server)
# The workspace-seats gate (orgs.provider_workspace_seats_enabled) is separate
# and later: it waits for the routing that honours a seat's org to be in the
# rollback target too.
seats_on = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {orgs = {provider_accounts_enabled = True, provider_workspace_seats_enabled = True}}), workloads.api_server)
