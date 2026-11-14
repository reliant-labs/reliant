# Fixture for cmd/reliant/cmd TestOrgUsageControlPlaneReachesTheOrgUsageHandler:
# the API server's env as a hosted deployment projects it, with the control
# plane composed the way control-plane's deploy/kcl/lib/reliant_process.k
# composes it for prod (server_process_config.control_plane.url = the
# in-cluster admin-server, :74; .internal_service_secret = the app config's
# internal-service secret, :32, which prod binds to control-plane-internal /
# secret in deploy/kcl/prod/config.k:424). It goes through the same
# config_gen.workloadEnvMap and workloads.api_server binding every env uses, so
# an env-map entry that drops, or either variable leaving the api root's
# reach, fails the test.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen
import workloads

_hosted = config_gen.ReliantProcess {
    reliant_config = config_gen.ReliantConfig {}
    server_process_config = config_gen.ServerProcessConfig {
        control_plane = {
            url = "http://admin-server.control-plane-prod.svc.cluster.local:8090"
            internal_service_secret = {name = "control-plane-internal", key = "secret"}
        }
    }
    database_config = config_gen.DatabaseConfig {}
    api_server_config = config_gen.ApiServerConfig {}
}

# control-plane deploys this repo's api root with INTERNAL_SERVICE_SECRET opted
# in (its deploy/kcl/workloads.k:871-874 reliant_api_server config_secrets,
# ["INTERNAL_SERVICE_SECRET", "NATS_PASSWORD", "RELIANT_VAULT_KEY"]): a secret
# reaches only the workloads that opt in, and the root must reach it.
_cp_api = workloads.api_server | {config_secrets = ["INTERNAL_SERVICE_SECRET", "NATS_PASSWORD", "RELIANT_VAULT_KEY"]}

hosted_api = config_gen.workloadEnvMap(_hosted, _cp_api)
