"""
Negative test — a Raft snapshot with no bucket is refused.
"""

# expect: OpenBaoSnapshot.bucket is required

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    openbao = forge.OpenBao {
        storage = forge.OpenBaoRaft {snapshot = forge.OpenBaoSnapshot {bucket = ""}}
    }

})
