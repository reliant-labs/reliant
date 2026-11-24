"""
Closed-schema test — forge.ShellPGO refuses a field it does not declare.

`path` is the name a reader might reach for; the profile's destination is
`out`, relative to the project root.
"""

# expect: path
# expect: ShellPGO

import forge

_b = forge.ShellBuild {cmd = "make", pgo = {"./cmd/demo" = forge.ShellPGO {path = "cmd/demo/default.pgo"}}}
