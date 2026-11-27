"""
Negative test — autoscale beside storageGiB.

storageGiB is a ReadWriteOnce volume, mounted by one pod at a time, which is
why it requires replicas = 1. A second replica the autoscaler starts could
never mount it.
"""

# expect: autoscale is not allowed with storageGiB

import forge
import forge.workloads as fw

_bad = fw.Workload {
    name = "api"
    storageGiB = 5
    autoscale = forge.Autoscale {minReplicas = 1, maxReplicas = 3}
}
