"""
Negative test — rules are tried in order and matched case-insensitively, so
a second rule with the same conditions could never match.
"""

# expect: Analytics.channels repeats a rule's conditions

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {
            channels = [
                forge.Channel {medium = "podcast", channel = "Podcasts"}
                forge.Channel {medium = "Podcast", channel = "Audio"}
            ]
        }}
    }]
})
