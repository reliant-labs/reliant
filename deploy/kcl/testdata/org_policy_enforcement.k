# Fixture for cmd/reliant/cmd TestOrgPolicyEnforcementSwitchReachesTheProcess:
# the org-policy enforcement switches, integrations, MCP, approvals and machines
# (ServerProcessConfig
# org_policy_enforcement, control-plane docs/design/org-policies.md §7) as a
# deployment projects them into the API server and the worker: at the schema
# default (audit: count, refuse nothing — every enforcement point ships so),
# as this repo's dev and demo envs render them, flipped to enforce, and off. It goes
# through the same config_gen.workloadEnvMap and workloads bindings every env
# uses, so a default that flips, an env-map entry that drops, or the variable
# leaving a root's reach fails the test. prod's ServerProcessConfig is composed
# by control-plane's lib/reliant_process.k, which sets no org_policy_enforcement
# field, so it takes the schema default the *_default selectors pin.
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

_enforce = config_gen.ServerProcessConfig {org_policy_enforcement = {integrations = "enforce", approvals = "enforce", machines = "enforce", mcp = "enforce"}}
_off = config_gen.ServerProcessConfig {org_policy_enforcement = {integrations = "off", approvals = "off", machines = "off", mcp = "off"}}

api_default = config_gen.workloadEnvMap(_process(config_gen.ServerProcessConfig {}), workloads.api_server)
worker_default = config_gen.workloadEnvMap(_process(config_gen.ServerProcessConfig {}), workloads.temporal_worker)
dev_api = config_gen.workloadEnvMap(devcfg.reliant_process, workloads.api_server)
dev_worker = config_gen.workloadEnvMap(devcfg.reliant_process, workloads.temporal_worker)
demo_api = config_gen.workloadEnvMap(democfg.reliant_process, workloads.api_server)
demo_worker = config_gen.workloadEnvMap(democfg.reliant_process, workloads.temporal_worker)
api_enforce = config_gen.workloadEnvMap(_process(_enforce), workloads.api_server)
worker_enforce = config_gen.workloadEnvMap(_process(_enforce), workloads.temporal_worker)
api_off = config_gen.workloadEnvMap(_process(_off), workloads.api_server)
worker_off = config_gen.workloadEnvMap(_process(_off), workloads.temporal_worker)
