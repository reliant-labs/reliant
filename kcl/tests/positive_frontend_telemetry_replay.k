"""
Positive test — session replay is switched on per frontend, per environment,
with `forge.BrowserTelemetry {replay = True}`:

  * the frontend's runtime config carries OTEL_REPLAY = "true", in a hosted
    frontend's StaticSite spec too (the control plane reads it back live);
  * a frontend that does not set it renders no OTEL_REPLAY (unset = off);
  * any frontend with replay on opts the env's observability policy in
    (`opt_in: ["replay"]`, `signals.replay` true), so the platform keeps the
    events;
  * the deprecated env-wide `signals.replay` alone still opts the env in, and
    switches no frontend on;
  * neither: no OTEL_REPLAY, no opt-in;
  * disabling observability, or browser telemetry, cancels the opt-in and
    keeps the hosted frontend from recording: no OTEL_REPLAY = "true" renders,
    the ingest path is "off", and an author's own OTEL_REPLAY is forced to
    "false";
  * telemetry.replay outranks a carried {VITE_,NEXT_PUBLIC_}OTEL_REPLAY build
    variable, and an author's runtime_config entry still outranks both.
"""

import forge

_dest = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}

_hosted = lambda name: str, replay: bool -> forge.Frontend {
    forge.Frontend {
        name = name, image = "ghcr.io/acme/" + name, path = name, type = "vite"
        runtime = forge.OnHosted {}
        telemetry = forge.BrowserTelemetry {replay = replay}
    }
}

_bundle = lambda frontends: [forge.Frontend], observability: forge.Observability -> any {
    forge.render(forge.Bundle {
        project = "demo", env = "prod"
        control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
        frontends = frontends
        observability = observability
    })
}

_on = _bundle([
    _hosted("web", True)
    _hosted("admin", False)
    forge.Frontend {
        name = "local", path = "local", type = "nextjs", port = 3000
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {replay = True}
        env_vars = [forge.EnvVar {name = "NEXT_PUBLIC_OTEL_REPLAY", value = "false"}]
    }
    forge.Frontend {
        name = "own", path = "own", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {replay = True}
        runtime_config = {OTEL_REPLAY = "false"}
    }
], forge.Observability {destination = _dest})

_legacy = _bundle([_hosted("web", False)], forge.Observability {
    destination = _dest, signals = forge.ObservabilitySignals {replay = True}
})
_neither = _bundle([_hosted("web", False)], forge.Observability {destination = _dest})
_disabled = _bundle([_hosted("web", True)], forge.Observability {enabled = False})
_browser_off = _bundle([
    _hosted("web", True)
    _hosted("own", True) | {runtime_config = {OTEL_REPLAY = "true"}}
], forge.Observability {destination = _dest, signals = forge.ObservabilitySignals {browser = False}})

output = _on

_by = {f.name: f for f in _on.frontends}

assert_hosted_spec_carries_replay = _by.web.runtime_config_spec.OTEL_REPLAY.value == "true"
assert_off_frontend_renders_nothing = "OTEL_REPLAY" not in _by.admin.runtime_config_spec
assert_host_runtime_config_carries_replay = _by.local.runtime_config.OTEL_REPLAY == "true"
assert_typed_field_outranks_build_variable = _by.local.runtime_config.OTEL_REPLAY == "true"
assert_author_runtime_config_wins = _by.own.runtime_config.OTEL_REPLAY == "false"
assert_frontend_replay_opts_env_in = _on.observability.policy.opt_in == ["replay"]
assert_frontend_replay_recorded_as_signal = _on.observability.signals.replay == True
assert_telemetry_renders_replay = _by.web.telemetry.replay == True and _by.admin.telemetry.replay == False

assert_legacy_signal_still_opts_in = _legacy.observability.policy.opt_in == ["replay"]
assert_legacy_signal_switches_no_frontend_on = "OTEL_REPLAY" not in _legacy.frontends[0].runtime_config_spec

assert_neither_opts_in = _neither.observability.policy.opt_in == [] and _neither.observability.signals.replay == False
assert_neither_renders_replay = "OTEL_REPLAY" not in _neither.frontends[0].runtime_config_spec

assert_disabled_cancels_opt_in = _disabled.observability.policy.opt_in == []
assert_disabled_renders_no_replay = "OTEL_REPLAY" not in _disabled.frontends[0].runtime_config_spec
assert_disabled_ingest_off = _disabled.frontends[0].runtime_config_spec.OTEL_ENDPOINT.value == "off"

_boff = {f.name: f for f in _browser_off.frontends}
assert_browser_off_cancels_opt_in = _browser_off.observability.policy.opt_in == [] and _browser_off.observability.signals.replay == False
assert_browser_off_renders_no_replay = "OTEL_REPLAY" not in _boff.web.runtime_config_spec
assert_browser_off_forces_author_replay_off = _boff.own.runtime_config_spec.OTEL_REPLAY.value == "false"
assert_browser_off_ingest_off = _boff.web.runtime_config_spec.OTEL_ENDPOINT.value == "off"
