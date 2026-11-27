"""
Negative test — autoscale.minReplicas of 0.

Scale to zero is not offered: the floor is at least one pod.
"""

# expect: Autoscale.minReplicas must be at least 1

import forge

_bad = forge.Autoscale {minReplicas = 0, maxReplicas = 3}
