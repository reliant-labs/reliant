"""
Negative test — a zero worker pool is refused (a controller with no workers reconciles nothing).
"""

# expect: must be >= 1

import forge

_c = forge.flux_chart(concurrency = 0)
