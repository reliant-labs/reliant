"""
Positive test — `telemetry.errors.ignore` renders in declaration order, "re:"
entries (RE2) included, and an unset block renders an empty list.
"""

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [
        forge.Frontend {
            name = "web", path = "web", type = "vite", port = 5173
            runtime = forge.OnHost {}
            telemetry = forge.BrowserTelemetry {
                errors = forge.FrontendErrors {ignore = ["AbortError", "re:^Failed to fetch", "ResizeObserver loop"]}
            }
        }
        forge.Frontend {
            name = "bare", path = "bare", type = "vite", port = 5174
            runtime = forge.OnHost {}
            telemetry = forge.BrowserTelemetry {}
        }
    ]
})

_by = {f.name: f for f in output.frontends}
assert_ignore_in_order = _by.web.telemetry.errors.ignore == ["AbortError", "re:^Failed to fetch", "ResizeObserver loop"]
assert_unset_is_empty = _by.bare.telemetry.errors.ignore == []
assert_other_knobs_default = _by.web.telemetry.console_capture == True and _by.web.telemetry.replay_sample_rate == 1.0
