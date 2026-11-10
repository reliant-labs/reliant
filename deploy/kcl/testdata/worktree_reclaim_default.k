# Fixture for cmd/reliant/cmd TestWorktreeReclaimDefaultReachesEveryServerRoot:
# each server root's env as a deployment projects it, from a ReliantConfig that
# leaves worktree_reclaim at its schema default, and once with the mode set
# explicitly. It goes through the same config_gen.workloadEnvMap and workload
# bindings every env uses, so dropping RELIANT_WORKTREE_RECLAIM from the env map
# or from a root's reach, or changing the schema default away from dry-run,
# fails the test.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen
import workloads

_process = lambda reliant: config_gen.ReliantConfig -> config_gen.ReliantProcess {
    config_gen.ReliantProcess {
        reliant_config = reliant
        server_process_config = config_gen.ServerProcessConfig {}
        database_config = config_gen.DatabaseConfig {}
        api_server_config = config_gen.ApiServerConfig {}
        worker_config = config_gen.WorkerConfig {}
        gateway_config = config_gen.GatewayConfig {}
    }
}

# worktree_reclaim left at its schema default, per server root.
default_api = config_gen.workloadEnvMap(_process(config_gen.ReliantConfig {}), workloads.api_server)
default_worker = config_gen.workloadEnvMap(_process(config_gen.ReliantConfig {}), workloads.temporal_worker)
default_gateway = config_gen.workloadEnvMap(_process(config_gen.ReliantConfig {}), workloads.daemon_gateway)

# An explicit mode reaches the process as written.
mode_off = config_gen.workloadEnvMap(_process(config_gen.ReliantConfig {worktree_reclaim = {mode = "off"}}), workloads.api_server)
