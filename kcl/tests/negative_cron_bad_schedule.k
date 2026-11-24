"""
Negative test — a cron workload's schedule must be a named expression
('@hourly', ...) or a 5-field cron expression. Anything else fails the
Workload check block, naming the field.
"""

# expect: Workload.schedule must be

import forge.workloads as fw

_cron = fw.Workload {
    name = "bad-schedule"
    kind = "cron"
    args = ["sweep"]
    schedule = "every other tuesday"
}
