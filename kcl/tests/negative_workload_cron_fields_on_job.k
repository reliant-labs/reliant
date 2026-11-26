"""
Negative test — concurrencyPolicy, startingDeadlineSeconds and timeZone say
how a cron's schedule fires. A job has no schedule, so declaring one of them
on it is refused, naming the fields, rather than silently dropped.
"""

# expect: Workload.concurrencyPolicy, startingDeadlineSeconds and timeZone
# expect: only kind='cron' has a schedule

import forge.workloads as fw

_job = fw.Workload {
    name = "report"
    kind = "job"
    args = ["report"]
    timeZone = "Europe/Berlin"
}
