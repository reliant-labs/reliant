"""
Negative test — a per-source limit above the total is refused.
"""

# expect: exceeds requests_per_second

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        public = forge.OpenBaoPublic {host = "secrets.example.com", per_source_requests_per_second = 900}
    }

})
