"""
Negative test — deployPhase on a SCHEDULED (cron) workload is refused.

The phase orders a one-shot Job against the deploy's workloads. A cron runs
on its own cadence and nothing waits for it, so the declaration would be
silently meaningless — which is exactly the kind of dropped declaration
forge refuses rather than ignores.
"""

# expect: deployPhase

import forge.workloads as fw

_c = fw.Workload {
    name = "nightly"
    kind = "cron"
    schedule = "@daily"
    args = ["report"]
    deployPhase = "pre-rollout"
}
