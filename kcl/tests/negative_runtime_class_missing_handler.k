"""
Negative test — `RuntimeClass` with empty `handler` is rejected by
the check block.

Pins the docstring-stated invariant: a RuntimeClass with no handler
would render an invalid node.k8s.io/v1 manifest (the API server
requires handler). Catching it at KCL render time keeps the failure
mode local to the project repo.

Note: KCL also rejects an entirely-missing `handler` at the schema-
required-field layer (different error), but the check block is the
one we own — pin THAT path so a future loosening of the check is
caught loudly.
"""

# expect: RuntimeClass.handler is required

import forge

_bad = forge.RuntimeClass { name = "gvisor", handler = "" }
