# Fixture for cmd/reliant/cmd TestProjectMovesGateReachesTheProjectHandler:
# the API server's env as a deployment projects it, once at the schema default
# (moving a project between orgs off, as every env ships until control-plane's
# ProjectOrgMoveInternalService is in prod and the rollback target) and once
# with orgs.project_moves_enabled set. It goes through the same
# config_gen.workloadEnvMap and workloads.api_server binding every env uses, so
# a default that flips, an env-map entry that drops, or the variable leaving
# the api root's reach fails the test.
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

# The API server's env with the orgs gates at their schema default.
gate_default = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {}), workloads.api_server)

# The API server's env with project moves turned on.
gate_on = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {orgs = {project_moves_enabled = True}}), workloads.api_server)

# The API server's env as this repo's own environments render it: dev and demo
# pin no orgs gate, so they ship it off. (prod's ApiServerConfig is composed by
# control-plane's lib/reliant_process.k, which sets no orgs field either, so it
# takes the schema default gate_default pins.)
dev_api = config_gen.workloadEnvMap(devcfg.reliant_process, workloads.api_server)
demo_api = config_gen.workloadEnvMap(democfg.reliant_process, workloads.api_server)
