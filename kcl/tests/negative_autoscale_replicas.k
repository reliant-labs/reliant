"""
Negative test — replicas beside autoscale.

The HorizontalPodAutoscaler owns the replica count, so a stated `replicas`
would be a second answer to the same question. The floor is
autoscale.minReplicas.
"""

# expect: autoscale owns the replica count; set autoscale.minReplicas

import forge
import forge.workloads as fw

_bad = fw.Workload {
    name = "api"
    replicas = 3
    autoscale = forge.Autoscale {minReplicas = 2, maxReplicas = 6}
}
