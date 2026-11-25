"""
Negative test — OnHost.runner must be one of the four runners forge knows
how to launch. Picking "bogus-runner" fails the check block, naming the
allowed values.
"""

# expect: runner must be 'go-run' | 'air' | 'binary' | 'delve'

import forge

_rt = forge.OnHost {
    runner = "bogus-runner"
}
