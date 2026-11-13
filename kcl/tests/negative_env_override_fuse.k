"""
NEGATIVE fixture — proves the footgun `forge.env_override` exists to avoid.

Composing env maps with bare `|` where the override merges INTO an existing key
that carries a DIFFERENT channel DEEP-MERGES (fuses) the two EnvSources into ONE
instance carrying BOTH `from_config` AND `from_secret`. That violates
EnvSource's exactly-one-channel check, so the render aborts with `Check failed`.

`env_override` (see positive_env_override.k) sidesteps this by REPLACING each
overridden key's WHOLE EnvSource, so the same re-channel yields a clean
single-channel source. This fixture is the two-channel proof: it MUST fail.
"""

# expect: EnvSource must set EXACTLY ONE channel

import forge

_base: {str: forge.EnvSource} = {
    "APP_URL" = {from_config = {name = "app-config", key = "app-url"}}
}

# The union (`:`) form merges INTO _base["APP_URL"], fusing from_config with
# from_secret into an invalid two-channel EnvSource.
_fused = _base | {"APP_URL": {from_secret = {name = "app-secrets", key = "app-url"}}}

# Forcing evaluation of the fused source triggers the exactly-one-channel check.
out = _fused["APP_URL"]
