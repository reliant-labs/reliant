"""
Negative test — sampling runs in the collector that forwards, and the local
ClickStack stores everything, so a ratio below 1 with no destination would be
a promise nothing keeps.
"""

# expect: traces_ratio < 1.0 needs a destination

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {local_port = 4318, sampling = forge.ObservabilitySampling {traces_ratio = 0.5}}
})
