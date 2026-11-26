"""
Negative test — autoscale.cpuTargetPercent above 95.

The target is a percent of the CPU request. A CPU limit at the request (the
default) caps utilization near 100%, so a target above 95 is one the pods can
never exceed, and the workload would never scale up.
"""

# expect: Autoscale.cpuTargetPercent must be at most 95

import forge

_bad = forge.Autoscale {minReplicas = 1, maxReplicas = 4, cpuTargetPercent = 120}
