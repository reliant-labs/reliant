"""
Negative test — HostedSecrets whose discriminator is overridden.

`type` is the discriminator Go dispatches on (secrets.NewProvider); a
HostedSecrets that claims another type would be resolved as that other
provider. The schema check pins it to "hosted".
"""

# expect: HostedSecrets.type must be 'hosted'

import forge

_bundle = forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {
        endpoint = "https://api.example.com"
    }
    secret_provider = forge.HostedSecrets {type = "file"}
}

output = forge.render(_bundle)
