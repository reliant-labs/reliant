"""
Negative test — autoscale on a job.

A job runs to completion as a Job, not a Deployment, so there is nothing for a
HorizontalPodAutoscaler to scale.
"""

# expect: autoscale is not supported for kind='job'

import forge
import forge.workloads as fw

_bad = fw.Workload {
    name = "migrate"
    kind = "job"
    args = ["migrate"]
    autoscale = forge.Autoscale {minReplicas = 1, maxReplicas = 3}
}
