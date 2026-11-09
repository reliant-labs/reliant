"""
Negative test — `registry` is where a release's bundle is pushed (a host and a
path); a scheme or a tag would make forge push somewhere else than the env's
Flux pulls from.
"""

# expect: must be a registry host and path

import forge

_fd = forge.FluxDelivery {registry = "oci://ghcr.io/acme"}
