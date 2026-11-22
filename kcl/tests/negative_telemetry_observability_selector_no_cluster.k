"""
Negative test — a selector in Observability.telemetry needs its cluster.
"""

# expect: an entry with a selector needs the cluster whose collector scrapes it

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {local_port = 4318, telemetry = [forge.Telemetry {service_name = "eso", selector = {app = "eso"}, metrics = [forge.MetricsScrape {port = 8080}]}]}
})
