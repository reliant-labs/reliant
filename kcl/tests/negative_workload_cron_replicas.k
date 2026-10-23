"""
Negative test — a `cron` cannot declare `replicas`.

A CronJob's concurrency is `concurrencyPolicy` on the schedule, not a
replica count. `replicas = 3` on a cron is not three concurrent runs; it
is a value Kubernetes has nowhere to put. Rejecting is what turns a wrong
belief ("I scaled the job") into a message.
"""

# expect: Workload.replicas is not supported for kind='cron'

import forge.workloads as fw

_bad = fw.Workload {
    name = "sweep"
    kind = "cron"
    schedule = "@hourly"
    args = ["sweep"]
    replicas = 3
}
