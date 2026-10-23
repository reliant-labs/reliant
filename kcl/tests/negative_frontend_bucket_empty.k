"""
Negative test — forge.OnBucket needs a bucket.

A static site with no bucket used to MEAN "hosted" — the hidden mode this
runtime split removes. An empty bucket is now just an error, pointing at
forge.OnHosted for platform hosting.
"""

# expect: OnBucket.bucket is required
# expect: forge.OnHosted

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {name = "web", path = "web", runtime = forge.OnBucket {bucket = ""}}]
})
