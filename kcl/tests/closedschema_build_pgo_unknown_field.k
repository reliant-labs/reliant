"""
Closed-schema test — forge.PGO refuses a field it does not declare.

`min_samples` is the name a reader might reach for; the threshold is
`min_weight`, a duration of CPU time, because a sample count means a
different amount of CPU at every profiling rate.
"""

# expect: min_samples
# expect: PGO

import forge

_b = forge.GoBuild {cmd = "./cmd/demo", pgo = forge.PGO {min_samples = 100}}
