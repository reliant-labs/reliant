"""
Negative test — a RoleBinding for something that is not a namespace is refused.
"""

# expect: not a namespace name

import forge

_b = forge.flux_namespace_bindings("Not/A Namespace")
