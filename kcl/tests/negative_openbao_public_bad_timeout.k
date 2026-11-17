"""
Negative test — a request timeout the Gateway API would refuse is refused here,
at the author's keyboard, not by the API server at apply time. Microseconds and
fractions are valid Go durations but not Gateway API ones.
"""

# expect: is not a Gateway API duration
# expect: whole numbers with h, m, s or ms

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        public = forge.OpenBaoPublic {host = "secrets.example.com", request_timeout = "1.5s"}
    }
})
