# Fixture for cmd/reliant/cmd TestStallWatchdogSwitchReachesTheReconciler:
# the API server's env (the reconciler runs there) as a deployment projects
# it, at the schema default (the stall watchdog off, as every env ships until
# it is turned on), as this repo's dev and demo envs render it, and with
# reconciler.stall_watchdog set to report and to enforce. It goes through the
# same config_gen.workloadEnvMap and workloads.api_server binding every env
# uses, so a default that flips, an env-map entry that drops, or the variable
# leaving the api root's reach fails the test. prod's ServerProcessConfig is
# composed by control-plane's lib/reliant_process.k, which sets no reconciler
# field, so it takes the schema default watchdog_default pins.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen
import workloads
import dev.config as devcfg
import demo.config as democfg

_process = lambda server: config_gen.ServerProcessConfig -> config_gen.ReliantProcess {
    config_gen.ReliantProcess {
        reliant_config = config_gen.ReliantConfig {}
        server_process_config = server
        database_config = config_gen.DatabaseConfig {}
        api_server_config = config_gen.ApiServerConfig {}
        worker_config = config_gen.WorkerConfig {}
    }
}

watchdog_default = config_gen.workloadEnvMap(_process(config_gen.ServerProcessConfig {}), workloads.api_server)
dev_api = config_gen.workloadEnvMap(devcfg.reliant_process, workloads.api_server)
demo_api = config_gen.workloadEnvMap(democfg.reliant_process, workloads.api_server)
watchdog_report = config_gen.workloadEnvMap(_process(config_gen.ServerProcessConfig {reconciler = {stall_watchdog = "report"}}), workloads.api_server)
watchdog_enforce = config_gen.workloadEnvMap(_process(config_gen.ServerProcessConfig {reconciler = {stall_watchdog = "enforce"}}), workloads.api_server)
