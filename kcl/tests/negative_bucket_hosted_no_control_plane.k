"""
Negative test — a hosted bucket needs a control plane to publish to.
"""

# expect: [uploads] run on the hosted runtime
# expect: needs control_plane = forge.ControlPlane

import forge

output = forge.render(forge.Bundle {
    project = "acme"
    buckets = [forge.Bucket {name = "uploads", runtime = forge.OnHosted {}}]
})
