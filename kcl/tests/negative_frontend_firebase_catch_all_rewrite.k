"""
Negative test — a catch-all rewrite to an HTML document on OnFirebase.

`** -> /index.html` answers every missing path with the page and a 200,
including a code-split chunk a deploy removed: a stale tab's dynamic import
then fails on a MIME/parse error (Sentry ELECTRON-CC). spa_fallback is the
replacement, and the refusal names it.
"""

# expect: catch-all rewrite
# expect: spa_fallback

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {
        name = "web"
        type = "vite"
        path = "web"
        runtime = forge.OnFirebase {
            project = "p"
            site = "web"
            rewrites = [{source = "**", destination = "/index.html"}]
        }
    }]
})
