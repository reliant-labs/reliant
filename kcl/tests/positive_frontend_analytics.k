"""
Positive test — `forge.BrowserTelemetry {analytics = forge.Analytics {...}}`
declares a frontend's built-in product analytics:

  * a non-default mode reaches the browser as ANALYTICS_MODE in the runtime
    config, in a hosted frontend's StaticSite spec too, and so does the
    `aggregate` opt-down; the default "identified" renders no key (unset
    means identified), and neither does a frontend with no `analytics`;
  * goals, the activation event and window, channel rules and the dashboards
    switch render under `telemetry.analytics` with the mode and dashboards
    defaults spelled out and channel conditions lowercased, for forge generate's built-in
    dashboards to read;
  * a frontend with no `analytics` renders no `analytics` key;
  * browser telemetry switched off renders no derived ANALYTICS_MODE, and an
    author's own ANALYTICS_MODE is forced to "off";
  * an author's runtime_config entry outranks the derived value;
  * funnels count by "visitor" (a window of at most a day) or "person" too.
"""

import forge

_dest = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}

_analytics = forge.Analytics {
    mode = "full"
    goals = [
        forge.Goal {id = "checkout", title = "Checkout", event = "purchase"}
        forge.Goal {id = "pricing", route = "/pricing"}
    ]
    activation = "order_created"
    activation_window = "14d"
    channels = [
        forge.Channel {host = "News.YCombinator.com", channel = "Organic social"}
        forge.Channel {source = "Partner-X", medium = "referral", channel = "Partners"}
    ]
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
    forge.Frontend {
        name = "web", image = "ghcr.io/acme/web", path = "web", type = "vite"
        runtime = forge.OnHosted {}
        telemetry = forge.BrowserTelemetry {
            analytics = _analytics
            funnels = [
                forge.Funnel {id = "buy", steps = ["view_product", "purchase"], by = "person", window = "7d"}
                forge.Funnel {id = "land", steps = ["page_viewed", "signup_started"], by = "visitor", window = "1d"}
            ]
        }
    }
    forge.Frontend {
        name = "defaults", path = "defaults", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {}}
    }
    forge.Frontend {
        name = "anon", path = "anon", type = "vite", port = 5175
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {mode = "aggregate"}}
    }
    forge.Frontend {
        name = "plain", path = "plain", type = "nextjs", port = 3000
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {console_capture = False}
    }
    forge.Frontend {
        name = "own", path = "own", type = "vite", port = 5174
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {mode = "off"}}
        runtime_config = {ANALYTICS_MODE = "identified"}
    }
], forge.Observability {destination = _dest})

_browser_off = _bundle([
    forge.Frontend {
        name = "web", image = "ghcr.io/acme/web", path = "web", type = "vite"
        runtime = forge.OnHosted {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {mode = "full"}}
    }
    forge.Frontend {
        name = "own", path = "own", type = "vite", port = 5174
        runtime = forge.OnHost {}
        runtime_config = {ANALYTICS_MODE = "full"}
    }
], forge.Observability {destination = _dest, signals = forge.ObservabilitySignals {browser = False}})

output = _on

_by = {f.name: f for f in _on.frontends}
_a = _by.web.telemetry.analytics
_off_by = {f.name: f for f in _browser_off.frontends}

assert_hosted_spec_carries_mode = _by.web.runtime_config_spec.ANALYTICS_MODE.value == "full"
assert_default_mode_renders_no_key = "ANALYTICS_MODE" not in _by.defaults.runtime_config
assert_no_analytics_renders_no_key = "ANALYTICS_MODE" not in _by.plain.runtime_config
assert_aggregate_opt_down_renders = _by.anon.runtime_config.ANALYTICS_MODE == "aggregate"
assert_author_runtime_config_wins = _by.own.runtime_config.ANALYTICS_MODE == "identified"

assert_mode_rendered = _a.mode == "full"
assert_goals_rendered = _a.goals == [
    {id = "checkout", title = "Checkout", event = "purchase", route = None}
    {id = "pricing", title = None, event = None, route = "/pricing"}
]
assert_activation_rendered = _a.activation == "order_created" and _a.activation_window == "14d"
assert_channels_lowercased_in_order = _a.channels == [
    {host = "news.ycombinator.com", source = None, medium = None, channel = "Organic social"}
    {host = None, source = "partner-x", medium = "referral", channel = "Partners"}
]
assert_dashboards_default_on = _a.dashboards == True
assert_defaults_spelled_out = _by.defaults.telemetry.analytics == {
    mode = "identified", goals = [], activation = None, activation_window = None, channels = [], dashboards = True
}
assert_no_analytics_renders_no_block = "analytics" not in _by.plain.telemetry

assert_person_funnel = _by.web.telemetry.funnels[0].by == "person" and _by.web.telemetry.funnels[0].window == "7d"
assert_visitor_funnel = _by.web.telemetry.funnels[1].by == "visitor"

assert_browser_off_derives_nothing = "ANALYTICS_MODE" not in _off_by.web.runtime_config_spec
assert_browser_off_forces_author_off = _off_by.own.runtime_config.ANALYTICS_MODE == "off"
