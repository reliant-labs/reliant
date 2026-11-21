# Gateway deployment projection, consumed by TestMachineStatsSelectionFromKCL.
# Exercise workloadEnvMap as consumers do, not a hand-built environment.
import config_gen
import workloads

_process = lambda gateway: config_gen.GatewayConfig -> config_gen.ReliantProcess {
    config_gen.ReliantProcess {
        reliant_config = config_gen.ReliantConfig {}
        server_process_config = config_gen.ServerProcessConfig {}
        database_config = config_gen.DatabaseConfig {}
        api_server_config = config_gen.ApiServerConfig {}
        worker_config = config_gen.WorkerConfig {}
        gateway_config = gateway
    }
}

cases = {
    default = config_gen.workloadEnvMap(_process(config_gen.GatewayConfig {}), workloads.daemon_gateway)
    single = config_gen.workloadEnvMap(_process(config_gen.GatewayConfig {
        machine_stats_orgs = "org-platform"
    }), workloads.daemon_gateway)
    multiple = config_gen.workloadEnvMap(_process(config_gen.GatewayConfig {
        machine_stats_orgs = "org-platform,org-ci"
        machine_stats_label = "pool=ci"
    }), workloads.daemon_gateway)
}
