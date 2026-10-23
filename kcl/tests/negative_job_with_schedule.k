"""
Negative test — a Job may not carry a `schedule`.

A job runs ONCE; a schedule means "repeatedly, forever". Accepting both
would leave the reader unable to tell which one the workload is, and the
two lower to different objects. The kinds stay distinct: `kind = "cron"`
is the scheduled one.
"""

# expect: Workload.schedule is only meaningful for kind='cron'

import forge.workloads as fw

_job = fw.Workload {
    kind = "job"
    name = "provision"
    args = ["provision"]
    schedule = "@hourly"
}
