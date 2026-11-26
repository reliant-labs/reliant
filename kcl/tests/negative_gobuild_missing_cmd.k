"""
Negative test — GoBuild requires `cmd`. Omitting it must fail the
schema check (no hardcoded ./cmd fallback in the schema).
"""

# expect: GoBuild.cmd is required

import forge
import forge.workloads as fw

_w = fw.Workload {name = "x", build = forge.GoBuild {}}
