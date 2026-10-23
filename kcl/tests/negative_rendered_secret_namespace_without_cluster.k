"""
Negative: RenderedSecret.namespace without cluster. A namespace names no
cluster, so on a provider entry it would silently be ignored by inference.
"""

# expect: RenderedSecret.namespace requires cluster

import forge

_s = forge.RenderedSecret {
    name = "half-placed"
    namespace = "somewhere"
    keys = {"k" = forge.RenderedSecretKey {key = "SOME_KEY"}}
}
