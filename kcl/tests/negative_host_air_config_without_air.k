"""
Negative test — OnHost.air_config is only valid when runner = "air".
Setting it with runner = "go-run" must fail the check block: the air config
would be silently ignored and the author would debug the wrong process.
"""

# expect: air_config is only valid when runner = 'air'

import forge

_rt = forge.OnHost {
    runner = "go-run"
    air_config = ".air.toml"
}
