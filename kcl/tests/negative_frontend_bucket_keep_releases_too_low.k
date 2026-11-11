"""
Negative test — OnBucket.keep_releases = 1 is rejected, not clamped.

Retaining one release deletes the predecessor's archive while pages loaded
from it moments ago still fetch its hashed assets.
"""

# expect: OnBucket.keep_releases must be 0

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {name = "web", path = "web", runtime = forge.OnBucket {bucket = "b", keep_releases = 1}}]
})
