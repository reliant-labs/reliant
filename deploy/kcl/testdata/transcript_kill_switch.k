# Fixture for cmd/reliant/cmd TestTranscriptKillSwitchReachesTheAPIServer:
# the API server's env as a deployment projects it, once with the transcript
# kill switch thrown (transcript.tool_body_stubs = "off") and once with the
# schema default. It goes through the same config_gen.workloadEnvMap and
# workloads.api_server binding every env uses (dev/main.k, control-plane's
# clusters), so dropping RELIANT_TRANSCRIPT_TOOL_BODY_STUBS from the env map
# or from the api root's reach fails the test.
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
    }
}

# The API server's env with the kill switch thrown.
stubs_off = config_gen.workloadEnvMap(_process(config_gen.ReliantConfig {transcript = {tool_body_stubs = "off"}}), workloads.api_server)

# The API server's env with transcript left at its schema default.
stubs_default = config_gen.workloadEnvMap(_process(config_gen.ReliantConfig {}), workloads.api_server)
