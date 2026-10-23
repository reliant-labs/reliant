"""dev-k8s — the whole stack in k3d, assembled through stack.full_stack.

TRIMMED COPY of control-plane's real deploy/kcl/dev-k8s/main.k. Note
`import lib.stack` with NO alias — the unaliased form, which a reader that
assumed an alias would miss.
"""

import forge
import lib.stack

_namespace = "control-plane-dev-k8s"

# The image registry, DECLARED: the local k3d registry. `forge build dev-k8s
# --push` pushes here; nothing else names it.
_registry = "localhost:5051"

_devk8s_k8s = forge.ClusterTarget {
    cluster = "k3d-control-plane-v2"
    namespace = _namespace
    registry = _registry
}

_bundle = stack.full_stack(stack.StackParams {
    cluster = _devk8s_k8s
})

output = forge.render(_bundle)
