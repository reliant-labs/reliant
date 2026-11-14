"""
Negative test — a Gateway with no listeners fails the schema check.
"""

# expect: Gateway must declare at least one listener

import forge

_gw = forge.Gateway {
    name = "empty"
    listeners = []
}
