"""
Negative test — the BROADCAST selector is still job-only.

`before` orders a ONE-SHOT: it means "this runs to completion, then the
things it gates start". A service has no completion to wait for, so
`before` on one is not a weaker ordering — it is a declaration with no
possible lowering. The wildcard does not change that, and this pins that
it does not: the broadcast form must not become a back door around the
kind check.
"""

# expect: Workload.before orders a one-shot job; it is only valid for kind='job'

import forge.workloads as fw

_bad = fw.Workload {
    kind = "service"
    name = "api"
    before = [fw.BEFORE_ALL]
}
