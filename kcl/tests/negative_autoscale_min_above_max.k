"""
Negative test — autoscale.minReplicas above autoscale.maxReplicas.

A range whose floor is above its ceiling has no count the autoscaler could
hold; Kubernetes would refuse the HorizontalPodAutoscaler at apply time, after
everything before it rolled. Refused at the declaration instead.
"""

# expect: autoscale.minReplicas (5) must not exceed autoscale.maxReplicas (3)

import forge
import forge.workloads as fw

_bad = fw.Workload {
    name = "api"
    autoscale = forge.Autoscale {minReplicas = 5, maxReplicas = 3}
}
