"""
Negative test — one log shipping path per env. The env's collector ships logs;
a workload that also exports OTLP logs would store every line twice.
"""

# expect: api
# expect: stored twice

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {local_port = 4318}
    workloads = [fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/demo"}, env = {OTEL_LOGS_EXPORTER = "otlp"}, runtime = forge.OnHost {}}]
})
