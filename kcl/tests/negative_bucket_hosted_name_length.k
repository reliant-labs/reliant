"""
Negative test — a hosted bucket's name is at most 40 characters: the
platform prefixes it to make the cloud bucket's name.
"""

# expect: on forge.OnHosted it must be at most 40

import forge

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    buckets = [forge.Bucket {name = "a-very-long-bucket-name-for-the-uploads-of-acme", runtime = forge.OnHosted {}}]
})
