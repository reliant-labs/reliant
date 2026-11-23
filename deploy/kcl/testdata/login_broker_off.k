# Fixture for cmd/reliant/cmd TestLoginBrokerStaysOffWithoutAnIdentity: the
# API server's env as a deployment projects it, through the same
# config_gen.workloadEnvMap and workloads.api_server binding every env uses.
# With no managed identity declared (the schema default, which prod takes:
# control-plane's lib/reliant_process.k composes reliant-api's
# ApiServerConfig and sets no identity field yet, and this repo's demo,
# desktop and release envs) nothing names a native-broker client, so `server
# api` mounts no /auth/* routes and opens no auth_sessions store. identity_set
# is the control: an identity's client id, as forge.IdentityRef fills it.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen
import workloads
import demo.config as democfg
import desktop.config as desktopcfg
import release.config as releasecfg

_process = lambda api: config_gen.ApiServerConfig -> config_gen.ReliantProcess {
    config_gen.ReliantProcess {
        reliant_config = config_gen.ReliantConfig {}
        server_process_config = config_gen.ServerProcessConfig {}
        database_config = config_gen.DatabaseConfig {}
        api_server_config = api
    }
}

identity_default = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {}), workloads.api_server)
demo_api = config_gen.workloadEnvMap(democfg.reliant_process, workloads.api_server)
desktop_api = config_gen.workloadEnvMap(desktopcfg.reliant_process, workloads.api_server)
release_api = config_gen.workloadEnvMap(releasecfg.reliant_process, workloads.api_server)
identity_set = config_gen.workloadEnvMap(_process(config_gen.ApiServerConfig {oidc_client_id = "native-broker-client"}), workloads.api_server)
