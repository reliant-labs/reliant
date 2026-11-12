"""
Negative test — Frontend.type must be 'nextjs' | 'vite' | 'rn'.
Anything else fails the check block.
"""

# expect: Frontend.type must be 'nextjs' | 'vite' | 'rn'

import forge

_fe = forge.Frontend {
    name = "bad-type"
    type = "ember"
    path = "frontends/bad"
}
