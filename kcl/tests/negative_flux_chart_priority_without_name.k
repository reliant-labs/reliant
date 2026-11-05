"""
Negative test — creating a PriorityClass with no name is refused.
"""

# expect: priority_class_create needs a priority_class_name

import forge

_c = forge.flux_chart(priority_class_name = "")
