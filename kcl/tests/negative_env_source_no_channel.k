"""
Negative test — an EnvSource with ZERO channels set is rejected by the
exactly-one-channel check block. The other side of "exactly one": an empty
source is a render-time error, not a silently-dropped var.
"""

# expect: EnvSource must set EXACTLY ONE channel

import forge

_bad = forge.EnvSource {}
