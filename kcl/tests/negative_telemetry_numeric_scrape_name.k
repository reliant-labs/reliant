"""
Negative test — an all-digit scrape name is refused: an unnamed scrape's job
name ends in its index, so `name = "0"` beside an unnamed first scrape would
give Prometheus two jobs of one name.
"""

# expect: MetricsScrape.name '0' needs a letter

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        telemetry = [forge.Telemetry {
            service_name = "sys"
            cluster = "k3d-demo"
            selector = {app = "sys"}
            metrics = [forge.MetricsScrape {port = 9100}, forge.MetricsScrape {name = "0", port = 9101}]
        }]
    }
})
