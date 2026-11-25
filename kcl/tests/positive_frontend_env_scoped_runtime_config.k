"""
Positive test — the per-environment values a web frontend's otel_gen.ts reads
(OTEL_ENDPOINT, ENVIRONMENT, OTEL_REPLAY) reach the runtime config document
however the frontend declares them, because otel_gen.ts no longer falls back to
the build variables:

  * `config.otel_endpoint` / `config.environment` render as OTEL_ENDPOINT /
    ENVIRONMENT, and still render their build variables;
  * a literal {NEXT_PUBLIC_,VITE_}OTEL_ENDPOINT / ENVIRONMENT / OTEL_REPLAY in
    `env_vars` is carried too, and outranks `config`;
  * an author's own runtime_config entry outranks both;
  * a secret-backed build variable is never carried (config.js is public);
  * React Native, which has no runtime config document, gets nothing;
  * a hosted frontend's spec carries OTEL_ENDPOINT alone: the control plane
    writes its ENVIRONMENT, and a fixed spec literal would override it.
"""

import forge

_ref = forge.EnvVar {name = "VITE_ENVIRONMENT", secret_ref = "web-secrets", secret_key = "environment"}

_all = forge.render(forge.Bundle {
    project = "demo", env = "prod"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
    frontends = [
        forge.Frontend {
            name = "cfg", path = "cfg", type = "vite", port = 5173
            runtime = forge.OnHost {}
            config = forge.FrontendConfig {otel_endpoint = "/_otel", environment = "staging"}
        }
        forge.Frontend {
            name = "vars", path = "vars", type = "nextjs", port = 3000
            runtime = forge.OnHost {}
            config = forge.FrontendConfig {otel_endpoint = "/from-config"}
            env_vars = [
                forge.EnvVar {name = "NEXT_PUBLIC_OTEL_ENDPOINT", value = "/from-env-var"}
                forge.EnvVar {name = "NEXT_PUBLIC_ENVIRONMENT", value = "qa"}
                forge.EnvVar {name = "NEXT_PUBLIC_OTEL_REPLAY", value = "true"}
            ]
        }
        forge.Frontend {
            name = "own", path = "own", type = "vite", port = 5174
            runtime = forge.OnHost {}
            config = forge.FrontendConfig {environment = "staging"}
            env_vars = [forge.EnvVar {name = "VITE_OTEL_REPLAY", value = "true"}]
            runtime_config = {ENVIRONMENT = "mine", OTEL_REPLAY = "false"}
        }
        forge.Frontend {
            name = "secret", path = "secret", type = "vite", port = 5175
            runtime = forge.OnHost {}
            env_vars = [_ref]
        }
        forge.Frontend {
            name = "mobile", path = "mobile", type = "rn", port = 8081
            runtime = forge.OnHost {}
            config = forge.FrontendConfig {otel_endpoint = "/_otel", environment = "staging"}
        }
        forge.Frontend {
            name = "hosted", image = "ghcr.io/acme/hosted", path = "hosted", type = "vite"
            runtime = forge.OnHosted {}
            config = forge.FrontendConfig {environment = "prod"}
            env_vars = [forge.EnvVar {name = "VITE_OTEL_REPLAY", value = "true"}]
        }
        forge.Frontend {
            name = "hostedingest", image = "ghcr.io/acme/hostedingest", path = "hostedingest", type = "vite"
            runtime = forge.OnHosted {}
            config = forge.FrontendConfig {otel_endpoint = "/ingest"}
        }
    ]
})

output = _all

_by = {f.name: f for f in _all.frontends}
_rc = lambda n: str -> any { _by[n].runtime_config }
_ev = lambda n: str -> any { {e.name: e.value for e in _by[n].env_vars if e.value} }

assert_config_endpoint_carried = _rc("cfg").OTEL_ENDPOINT == "/_otel"
assert_config_environment_carried = _rc("cfg").ENVIRONMENT == "staging"
assert_config_build_variable_kept = "VITE_OTEL_ENDPOINT" not in _ev("cfg") or _ev("cfg").VITE_OTEL_ENDPOINT == "/_otel"
assert_env_var_outranks_config = _rc("vars").OTEL_ENDPOINT == "/from-env-var"
assert_env_var_environment_carried = _rc("vars").ENVIRONMENT == "qa"
assert_env_var_replay_carried = _rc("vars").OTEL_REPLAY == "true"
assert_author_outranks_derived = _rc("own").ENVIRONMENT == "mine" and _rc("own").OTEL_REPLAY == "false"
assert_secret_never_carried = "ENVIRONMENT" not in _rc("secret")
assert_react_native_gets_nothing = "OTEL_ENDPOINT" not in _rc("mobile") and "ENVIRONMENT" not in _rc("mobile")
assert_hosted_spec_leaves_environment_to_the_control_plane = "ENVIRONMENT" not in _by.hosted.runtime_config_spec
assert_hosted_spec_publishes_no_replay_literal = "OTEL_REPLAY" not in _by.hosted.runtime_config_spec
assert_hosted_still_defaults_ingest = _by.hosted.runtime_config_spec.OTEL_ENDPOINT.value == "/_otel"
assert_hosted_carries_a_declared_ingest = _by.hostedingest.runtime_config_spec.OTEL_ENDPOINT.value == "/ingest"
