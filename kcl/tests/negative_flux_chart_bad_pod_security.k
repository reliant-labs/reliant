"""
Negative test — an unknown Pod Security level is refused rather than written into a label the apiserver would reject at apply.
"""

# expect: pod_security must be

import forge

_c = forge.flux_chart(pod_security = "strict")
