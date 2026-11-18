"""
Negative test — the first listener is the address clients use, so a plaintext
listener may only come after a TLS one.
"""

# expect: listeners[0]
# expect: is plaintext

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo"}
    openbao = forge.OpenBao {
        listeners = [
            forge.OpenBaoListener {port = 8100, tls = False, transitional_reason = "migrating"}
            forge.OpenBaoListener {port = 8200}
        ]
    }
})
