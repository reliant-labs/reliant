# Fixture for cmd/reliant/cmd TestPipelineSwitchReachesTheWorkerDispatcher
# and TestPipelineSwitchReachesTheAPIServer: the worker's and the API
# server's env as a deployment projects it, at the schema default (the
# pipeline switch off, as every env ships until the pipeline is enabled), as
# this repo's dev and demo envs render it, and with pipeline.enabled set. It
# goes through the same config_gen.workloadEnvMap and workloads.temporal_worker
# binding every env uses, so a default that flips, an env-map entry that
# drops, or the variable leaving the worker root's reach fails the test.
# prod's ServerProcessConfig is composed by control-plane's
# lib/reliant_process.k, which sets no pipeline field, so it takes the schema
# default switch_default pins.
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
        worker_config = config_gen.WorkerConfig {}
    }
}

switch_default = config_gen.workloadEnvMap(_process(config_gen.ServerProcessConfig {}), workloads.temporal_worker)
dev_worker = config_gen.workloadEnvMap(devcfg.reliant_process, workloads.temporal_worker)
demo_worker = config_gen.workloadEnvMap(democfg.reliant_process, workloads.temporal_worker)
switch_on = config_gen.workloadEnvMap(_process(config_gen.ServerProcessConfig {pipeline = {enabled = True}}), workloads.temporal_worker)

switch_default_api = config_gen.workloadEnvMap(_process(config_gen.ServerProcessConfig {}), workloads.api_server)
dev_api = config_gen.workloadEnvMap(devcfg.reliant_process, workloads.api_server)
demo_api = config_gen.workloadEnvMap(democfg.reliant_process, workloads.api_server)
switch_on_api = config_gen.workloadEnvMap(_process(config_gen.ServerProcessConfig {pipeline = {enabled = True}}), workloads.api_server)
