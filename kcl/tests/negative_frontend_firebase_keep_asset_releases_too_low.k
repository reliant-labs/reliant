"""
Negative test — OnFirebase.keep_asset_releases = 1 is rejected, not clamped.

Keeping only the release going live deletes the chunks every tab opened
before the deploy lazy-loads next.
"""

# expect: OnFirebase.keep_asset_releases must be at least 2

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {name = "web", type = "vite", path = "web", runtime = forge.OnFirebase {project = "p", site = "web", keep_asset_releases = 1}}]
})
