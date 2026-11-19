"""
Negative test — a channel rule's host is the referrer HOST, not a URL.
"""

# expect: Channel 'Organic social'.host 'https://news.ycombinator.com/' is not a host name

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {
            channels = [forge.Channel {host = "https://news.ycombinator.com/", channel = "Organic social"}]
        }}
    }]
})
