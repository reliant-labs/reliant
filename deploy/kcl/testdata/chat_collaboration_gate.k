# Fixture for cmd/reliant/cmd TestChatCollaborationGateReachesTheSharingHandler:
# the API server's env as a deployment projects it, once at the schema default
# (granting people access to a chat off, as every env ships until every API
# server and worker, and the rollback target, read chat_members) and once with
# chat_collaboration.enabled set. It goes through the same
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

# The API server's env with the chat collaboration gate at its schema default.
gate_default = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {}), workloads.api_server)

# The API server's env with chat collaboration turned on.
gate_on = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {chat_collaboration = {enabled = True}}), workloads.api_server)

# The API server's env as this repo's own environments render it: dev and demo
# pin no chat collaboration gate, so they ship it off. (prod's ApiServerConfig
# is composed by control-plane's lib/reliant_process.k, which sets no
# chat_collaboration field either, so it takes the schema default gate_default
# pins.)
dev_api = config_gen.workloadEnvMap(devcfg.reliant_process, workloads.api_server)
demo_api = config_gen.workloadEnvMap(democfg.reliant_process, workloads.api_server)
