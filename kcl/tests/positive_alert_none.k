"""
Positive test — an env that declares no alerts renders no `alerts` key, so its
shape and hash are exactly what they were before alerting existed.
"""

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {local_port = 4318}
})

assert_no_alerts_key = "alerts" not in output.observability
