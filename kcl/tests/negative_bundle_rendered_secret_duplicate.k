"""
Negative: the same Secret name declared twice for one cluster/namespace.
The two would apply in turn and the later would silently overwrite the
earlier, so the ambiguity is refused at load.
"""

# expect: Bundle.rendered_secrets: a Secret name is declared twice for the same cluster/namespace

import forge

_target = forge.ClusterTarget {
    cluster = "k3d-cp"
    namespace = "cp-dev"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    rendered_secrets = [
        forge.RenderedSecret {name = "dup", keys = {"a" = forge.RenderedSecretKey {key = "A"}}}
        forge.RenderedSecret {name = "dup", namespace = "cp-dev", cluster = "k3d-cp", keys = {"b" = forge.RenderedSecretKey {key = "B"}}}
    ]
}

output = forge.render(_bundle)
