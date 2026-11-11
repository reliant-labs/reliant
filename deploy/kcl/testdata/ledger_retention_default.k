# Fixture for cmd/reliant/cmd TestLedgerRetentionDefaultReachesTheWorker:
# the worker's env as a deployment projects it, from a WorkerConfig that leaves
# the ledger retention settings at their schema defaults, and once with each
# set explicitly. It goes through the same config_gen.workloadEnvMap and
# workload binding every env uses, so dropping a retention setting from the env
# map, or moving a default away from "0s" (deletion off), fails the test.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen
import workloads

_process = lambda worker: config_gen.WorkerConfig -> config_gen.ReliantProcess {
    config_gen.ReliantProcess {
        reliant_config = config_gen.ReliantConfig {}
        server_process_config = config_gen.ServerProcessConfig {}
        database_config = config_gen.DatabaseConfig {}
        api_server_config = config_gen.ApiServerConfig {}
        worker_config = worker
        gateway_config = config_gen.GatewayConfig {}
    }
}

# Retention left at its schema defaults: deletion off, a one-hour start delay.
default_worker = config_gen.workloadEnvMap(_process(config_gen.WorkerConfig {}), workloads.temporal_worker)

# Explicit settings reach the worker as written.
enabled_worker = config_gen.workloadEnvMap(_process(config_gen.WorkerConfig {
    chat_update_retention = "720h"
    step_execution_retention = "2160h"
    ledger_retention_start_delay = "0s"
}), workloads.temporal_worker)
