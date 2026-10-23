"""
Positive test — `Frontend.telemetry` and the frontend's runtime reach the
browser through the runtime config document:

  * a hosted frontend defaults OTEL_ENDPOINT to the platform's /_otel, unless
    the author named an endpoint any way (runtime_config, config.otel_endpoint,
    the build variable) or browser telemetry is off;
  * console_capture = False renders CONSOLE_CAPTURE = "false", and a
    replay_sample_rate below 1 renders REPLAY_SAMPLE_RATE; the defaults render
    nothing;
  * an author's own runtime_config entry for either key wins;
  * sourcemaps renders only when it is not the default "publish".
"""

import forge
import forge.workloads as fw

_hosted = lambda name: str -> any {
    forge.Frontend {
        name = name, image = "ghcr.io/acme/" + name, path = name, type = "vite"
        runtime = forge.OnHosted {}
    }
}

_bundle = lambda frontends: [forge.Frontend], observability = Undefined -> any {
    forge.Bundle {
        project = "demo", env = "prod"
        control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
        frontends = frontends
        observability = observability
    }
}

_plain = forge.Frontend {
    name = "plain", image = "ghcr.io/acme/plain", path = "plain", type = "vite"
    runtime = forge.OnHosted {}
}
_tuned = forge.Frontend {
    name = "tuned", image = "ghcr.io/acme/tuned", path = "tuned", type = "vite"
    runtime = forge.OnHosted {}
    telemetry = forge.BrowserTelemetry {
        console_capture = False
        replay_sample_rate = 0.25
        sourcemaps = "off"
    }
}
_own = forge.Frontend {
    name = "own", image = "ghcr.io/acme/own", path = "own", type = "vite"
    runtime = forge.OnHosted {}
    runtime_config = {OTEL_ENDPOINT = "https://ingest.example/k/abc", REPLAY_SAMPLE_RATE = "0.5"}
    telemetry = forge.BrowserTelemetry {replay_sample_rate = 0.25}
}
_via_config = forge.Frontend {
    name = "viacfg", image = "ghcr.io/acme/viacfg", path = "viacfg", type = "vite"
    runtime = forge.OnHosted {}
    config = forge.FrontendConfig {otel_endpoint = "off"}
}
_default_telemetry = forge.Frontend {
    name = "dflt", image = "ghcr.io/acme/dflt", path = "dflt", type = "vite"
    runtime = forge.OnHosted {}
    telemetry = forge.BrowserTelemetry {}
}
_on_host = forge.Frontend {
    name = "dev", path = "dev", type = "vite", port = 5173
    runtime = forge.OnHost {}
    telemetry = forge.BrowserTelemetry {console_capture = False}
}

_all = forge.render(forge.Bundle {
    project = "demo", env = "prod"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
    frontends = [_plain, _tuned, _own, _via_config, _default_telemetry, _on_host]
})
_off = forge.render(forge.Bundle {
    project = "demo", env = "prod"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
    frontends = [_plain]
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
        signals = forge.ObservabilitySignals {browser = False}
    }
})

output = _all

_by = {f.name: f for f in _all.frontends}
_spec = lambda n: str -> any { _by[n].runtime_config_spec }

assert_hosted_defaults_to_platform_ingest = _spec("plain").OTEL_ENDPOINT.value == "/_otel"
assert_nothing_else_derived_by_default = len(_spec("plain")) == 1
assert_console_capture_false = _spec("tuned").CONSOLE_CAPTURE.value == "false"
assert_replay_sample_rate = _spec("tuned").REPLAY_SAMPLE_RATE.value == "0.25"
assert_tuned_still_gets_ingest = _spec("tuned").OTEL_ENDPOINT.value == "/_otel"
assert_author_endpoint_wins = _spec("own").OTEL_ENDPOINT.value == "https://ingest.example/k/abc"
assert_author_rate_wins = _spec("own").REPLAY_SAMPLE_RATE.value == "0.5"
assert_typed_config_endpoint_blocks_the_default = "OTEL_ENDPOINT" not in _spec("viacfg")
assert_default_telemetry_adds_nothing = len(_spec("dflt")) == 1
assert_browser_off_wins_over_everything = _off.frontends[0].runtime_config_spec.OTEL_ENDPOINT.value == "off"
assert_dev_resolved_config_carries_console_capture = _by.dev.runtime_config.CONSOLE_CAPTURE == "false"
assert_dev_gets_no_ingest_default = "OTEL_ENDPOINT" not in _by.dev.runtime_config
assert_sourcemaps_off_renders = _by.tuned.sourcemaps == "off"
assert_sourcemaps_default_renders_nothing = "sourcemaps" not in _by.plain and "sourcemaps" not in _by.dflt
