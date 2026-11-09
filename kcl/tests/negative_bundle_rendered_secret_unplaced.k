"""
Negative: a Bundle-level rendered Secret with no cluster, in a Bundle with
no cluster_target, has nowhere to land. It is placed by declaration, never
inferred, so this is a load error rather than a Secret that is silently
never applied.
"""

# expect: Bundle.rendered_secrets: every Secret needs a cluster

import forge

_bundle = forge.Bundle {
    project = "fixture"
    rendered_secrets = [forge.RenderedSecret {
        name = "orphan"
        keys = {"k" = forge.RenderedSecretKey {key = "SOME_KEY"}}
    }]
}

output = forge.render(_bundle)
