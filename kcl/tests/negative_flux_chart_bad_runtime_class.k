"""
Negative test — a RuntimeClass that is not a name is refused.
"""

# expect: RuntimeClass name

import forge

_c = forge.flux_chart(placement = forge.flux_placement_delivery_v1(runtime_class_name = "Bad Class"))
