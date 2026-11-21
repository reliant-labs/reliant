# Fixture for cmd/reliant/cmd TestDaemonTypeProjectsThroughKCL: RELIANT_DAEMON_TYPE
# in the env of a `reliant daemon start` process and of a `reliant auth login`
# process, projected by the same config_gen.reliantProcessEnvMap every env's
# `reliant` process uses. DaemonConfig.daemon_type is the declaration for
# `daemon start` (as it always was), AuthConfig.daemon_type the one for
# `reliant auth`; no process loads both, so each env has one source.
#
# Not an environment: there is no main.k here, and nothing imports this file.

import config_gen

_process = lambda daemon: str, auth: str -> config_gen.ReliantProcess {
    config_gen.ReliantProcess {
        reliant_config = config_gen.ReliantConfig {}
        cli_config = config_gen.CLIConfig {}
        daemon_config = config_gen.DaemonConfig {daemon_type = daemon}
        auth_config = config_gen.AuthConfig {daemon_type = auth}
    }
}

_daemon_start = ["reliant", "daemon", "start"]
_auth_login = ["reliant", "auth", "login"]

cases = {
    # DaemonConfig's field alone: what a KCL config written before AuthConfig sets.
    daemon_start_daemon_field = config_gen.reliantProcessEnvMap(_process("managed", ""), _daemon_start, [])
    # AuthConfig's field does not reach `daemon start`.
    daemon_start_auth_field = config_gen.reliantProcessEnvMap(_process("", "managed"), _daemon_start, [])
    # AuthConfig's field alone.
    auth_login_auth_field = config_gen.reliantProcessEnvMap(_process("", "managed"), _auth_login, [])
    # DaemonConfig's field does not reach `reliant auth`.
    auth_login_daemon_field = config_gen.reliantProcessEnvMap(_process("managed", ""), _auth_login, [])
}
