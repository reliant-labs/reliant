"""
Negative test — autoscale on an operator.

Only a service or a worker scales on load. An operator elects one leader, so
more pods buy it nothing; a batch kind or a tool has no Deployment to scale.
"""

# expect: autoscale is not supported for kind='operator'

import forge
import forge.workloads as fw

_bad = fw.Workload {
    name = "controller"
    kind = "operator"
    autoscale = forge.Autoscale {minReplicas = 1, maxReplicas = 3}
}
