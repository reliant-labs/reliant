"""
Negative test — an empty list of trusted proxy ranges would make OpenBao refuse
the Gateway, and so every request. It is refused at render, not discovered as a
wall of 400s.
"""

# expect: trusted_proxy_cidrs is empty
# expect: Name the ranges the proxy pods get addresses from

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        public = forge.OpenBaoPublic {host = "secrets.example.com", trusted_proxy_cidrs = []}
    }
})
