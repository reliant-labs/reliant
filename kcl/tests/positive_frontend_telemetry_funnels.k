"""
Positive test — `Frontend.telemetry.funnels` declares product funnels. They
render under the frontend's `telemetry.funnels` with `window` and `by`
defaulted, and a frontend that declares none renders no `telemetry` key.
"""

import forge


output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [
        forge.Frontend {
            name = "web", path = "web", type = "vite", port = 5173
            runtime = forge.OnHost {}
            telemetry = forge.BrowserTelemetry {
                funnels = [
                    forge.Funnel {
                        id = "onboarding"
                        title = "Onboarding"
                        steps = ["signup_started", "workspace_created", "first_chat"]
                        window = "7d"
                        by = "user"
                    }
                    forge.Funnel {id = "checkout", steps = ["cart_viewed", "paid"]}
                ]
            }
        }
        forge.Frontend {
            name = "admin", path = "admin", type = "vite", port = 5174
            runtime = forge.OnHost {}
        }
    ]
})

_web = output.frontends[0]
_admin = output.frontends[1]
assert_two_funnels = len(_web.telemetry.funnels) == 2
assert_declared_values_kept = _web.telemetry.funnels[0].id == "onboarding" and _web.telemetry.funnels[0].title == "Onboarding" and _web.telemetry.funnels[0].window == "7d" and _web.telemetry.funnels[0].by == "user" and _web.telemetry.funnels[0].steps == ["signup_started", "workspace_created", "first_chat"]
assert_window_defaults_to_an_hour = _web.telemetry.funnels[1].window == "1h"
assert_by_defaults_to_session = _web.telemetry.funnels[1].by == "session"
assert_title_unset_is_null = _web.telemetry.funnels[1].title == None
assert_undeclared_telemetry_is_omitted = "telemetry" not in _admin
