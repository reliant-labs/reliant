"""
Negative test — a plaintext store URL is refused.
"""

# expect: HostedStore.server
# expect: must be an https:// URL

import forge

_s = forge.HostedStore {server = "http://secrets.internal"}
output = forge.render(forge.Bundle {project = "demo", env = "dev"})
