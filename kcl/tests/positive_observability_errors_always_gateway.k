"""
Positive test — errors_always with a ratio below 1 is accepted when the
destination's gateway is declared to keep error traces itself
(destination_tail_samples). Forge still only head-samples the ratio; the
declaration is the destination's promise, carried in the render for it.
"""

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        local_port = 4318
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
        sampling = forge.ObservabilitySampling {traces_ratio = 0.1, errors_always = True, destination_tail_samples = True}
    }
})

assert_declared_intent_is_projected = output.observability.sampling.errors_always == True and output.observability.sampling.destination_tail_samples == True and output.observability.sampling.traces_ratio == 0.1
