"""
Negative — a Bundle.overrides key that is not an override key.

Checked at LOAD rather than left to the Go apply, because the two failures
read completely differently to an author: a bare `api` reported as "matches
no rendered object" sounds like forge rendered no such object, when the real
fault is that the key never named a kind. The refusal shows the grammar.
"""

# expect: are not override keys
# expect: Kind/name

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    overrides = {
        # No kind: `Kind/name` is the minimum.
        "api" = {spec.replicas = 2}
    }
})
