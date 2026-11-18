"""
Negative test — keeping every error trace is a tail decision. Head sampling in
the SDK cannot know a trace will fail, and forge renders no gateway to decide
on the whole trace. A ratio below 1 with errors_always is therefore refused
unless the destination is declared to keep error traces itself.
"""

# expect: needs tail sampling, which forge does not render
# expect: destination_tail_samples

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
        sampling = forge.ObservabilitySampling {traces_ratio = 0.1, errors_always = True}
    }
})
