"""
Negative test — OnFirebase.spa_fallback must be an absolute HTML path.
"""

# expect: OnFirebase.spa_fallback must be the absolute path of an HTML document

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {name = "web", type = "vite", path = "web", runtime = forge.OnFirebase {project = "p", site = "web", spa_fallback = "index.html"}}]
})
