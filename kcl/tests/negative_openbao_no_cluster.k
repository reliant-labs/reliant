"""
Negative test — an OpenBao with no cluster to run on is refused, naming where to
set one.
"""

# expect: Bundle.openbao has no cluster to run on
# expect: cluster_target

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    openbao = forge.OpenBao {}
})
