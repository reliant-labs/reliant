"""
Negative test — autoscale beside strategy = "Recreate".

Recreate is declared when the replica count is a CORRECTNESS bound (one
version, one pod set at a time). A count the autoscaler moves contradicts it.
"""

# expect: autoscale is not allowed with strategy = "Recreate"

import forge
import forge.workloads as fw

_bad = fw.Workload {
    name = "sweeper"
    kind = "worker"
    strategy = "Recreate"
    autoscale = forge.Autoscale {minReplicas = 1, maxReplicas = 3}
}
