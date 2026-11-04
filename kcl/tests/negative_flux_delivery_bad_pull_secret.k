"""
Negative test — `pull_secret_from` names an existing Secret as
`<namespace>/<name>`; a bare name says nothing about where to copy it from.
"""

# expect: must be `<namespace>/<name>`

import forge

_fd = forge.FluxDelivery {pull_secret_from = "ghcr-pull"}
