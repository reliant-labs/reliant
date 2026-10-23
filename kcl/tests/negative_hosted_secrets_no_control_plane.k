"""
Negative test — HostedSecrets with no control_plane on the Bundle.

Hosted secret values live in the env's control plane; HostedSecrets has no
endpoint of its own. Without a control_plane there is nowhere to put them,
so the Bundle check must reject the declaration at load rather than let
`forge secret set` fail later with a less direct error.
"""

# expect: secret_provider = forge.HostedSecrets {} requires control_plane

import forge

_bundle = forge.Bundle {
    project = "fixture"
    secret_provider = forge.HostedSecrets {}
}

output = forge.render(_bundle)
