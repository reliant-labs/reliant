"""
Negative test — the BROADCAST selector cannot be mixed with names.

`before = ["*", "api"]` has two readings and one of them is dangerous:
either the "api" is redundant (harmless but confused), or the author
believed the list NARROWED the broadcast to just that name. The second
reading is how a migration step ends up gating one workload out of five
while looking like it gates everything.

There is no safe way to guess which was meant, so the schema rejects it
and says both spellings.
"""

# expect: already gates every workload

import forge.workloads as fw

_bad = fw.Workload {
    kind = "job"
    name = "migrate"
    args = ["db", "migrate", "up"]
    before = [fw.BEFORE_ALL, "api"]
}
