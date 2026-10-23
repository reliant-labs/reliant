"""
Negative test — a StaticSite runtimeConfig entry that sets BOTH a literal
value and a workloadURL has no correct reading, and the generated tier schema
refuses it (the lowering of the Go type's ExactlyOneOf marker).
"""

# expect: RuntimeConfigValue: exactly one of the fields in [value workloadURL] must be set

import forge.tiers

_bad = tiers.StaticSite {
    runtimeConfig = {
        API_URL = tiers.RuntimeConfigValue { value = "https://x", workloadURL = tiers.WorkloadURLRef { name = "api" } }
    }
}
