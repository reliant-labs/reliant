"""
Negative test — bundle dirs on a dev-served frontend.

The dev server assembles nothing: a bundle dir there would be a sibling site
the author believes is mounted and is not.
"""

# expect: bundle
# expect: host runtime

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {name = "web", path = "web", bundle = [forge.BundleDir {src = "spa/dist"}], runtime = forge.OnHost {}}]
})
