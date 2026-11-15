"""
Closed-schema test — a Bundle has no env-level `runtime`.

The runtime is chosen PER WORKLOAD (ADR 0002 §2): an env is a list of
bindings, not a runtime. An env-wide default was how "hosted" became an env
mode and one env could not mix runtimes, so the field does not exist; KCL
refuses it at compile time, naming the member and the schema.
"""

# expect: runtime
# expect: Bundle

import forge

_b = forge.Bundle {
    project = "demo"
    runtime = forge.OnHost {}
}
