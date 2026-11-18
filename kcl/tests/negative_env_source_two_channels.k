"""
Negative test — an EnvSource with TWO channels set is rejected by the
exactly-one-channel check block. A malformed source is a render-time error,
NOT a silently-dropped var (the map-shape analogue of EnvVar's own
"must set a source" check).
"""

# expect: EnvSource must set EXACTLY ONE channel

import forge

_bad = forge.EnvSource {
    value = "literal"
    from_secret = {name = "db", key = "url"}
}
