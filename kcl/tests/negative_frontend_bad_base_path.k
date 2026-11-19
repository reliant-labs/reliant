"""
Negative test — Frontend.base_path must start with '/'.

One rule for every runtime: the dev mount and every published site read the
same field.
"""

# expect: base_path must start with '/'

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {name = "admin-web", path = "admin-web", base_path = "admin", runtime = forge.OnFirebase {project = "p", site = "s"}}]
})
