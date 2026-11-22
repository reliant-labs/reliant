"""
Negative test — a ManagedDatabase bound to the Cluster runtime with no
namespace to land in: neither a namespace of its own nor a target to take
one from. Self-hosted there is no platform to allocate one.
"""

# expect: ManagedDatabase

import forge

_bundle = forge.Bundle {
    project = "fixture"
    databases = [forge.ManagedDatabase {name = "orders"}]
}

output = forge.render(_bundle)
