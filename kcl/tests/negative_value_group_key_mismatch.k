"""
Negative test — ExternalSecrets sharing a `value_group` (the cross-secret
byte-match group) must declare the SAME key set, so the live byte
comparison compares like for like. A group whose members project different
keys can't be byte-matched, so the Bundle check rejects it at parse time.
"""

# expect: ExternalSecrets sharing a value_group must declare the SAME key set

import forge

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = forge.ClusterTarget {
        cluster = "prod", namespace = "ns"
    }
    required_secrets = [
        forge.ExternalSecret {
            name = "token-a"
            namespace = "ns"
            keys = ["api-token"]
            value_group = "shared"
        }
        forge.ExternalSecret {
            name = "token-b"
            namespace = "ns"
            keys = ["different-key"]
            value_group = "shared"
        }
    ]
}

output = forge.render(_bundle)
